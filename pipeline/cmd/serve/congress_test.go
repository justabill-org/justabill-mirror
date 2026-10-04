package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"
)

// fakeCongresses records the tracker's writes. promoteOK is PromoteCongress's answer.
type fakeCongresses struct {
	ensured   []int
	promoted  []int
	promoteOK bool
	err       error
}

func (f *fakeCongresses) EnsureCongress(_ context.Context, congress int) error {
	if f.err != nil {
		return f.err
	}
	f.ensured = append(f.ensured, congress)
	return nil
}

func (f *fakeCongresses) PromoteCongress(_ context.Context, congress int) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.promoted = append(f.promoted, congress)
	return f.promoteOK, nil
}

func trackerAt(store congressWriter, pinned int, now time.Time) *congressTracker {
	tr := newCongressTracker(store, pinned, slog.New(slog.DiscardHandler))
	tr.now = func() time.Time { return now }
	return tr
}

func utc(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }

func TestTrackerTargets(t *testing.T) {
	tests := []struct {
		name   string
		pinned int
		now    time.Time
		want   []int
	}{
		{"launch", 0, utc(2026, time.October, 4, 12), []int{119}},
		{"new year's day is still the 119th", 0, utc(2027, time.January, 2, 23), []int{119}},
		{"the 120th starts; the 119th's last days follow", 0, utc(2027, time.January, 3, 0), []int{120, 119}},
		{"last day of the grace period", 0, utc(2027, time.January, 31, 23), []int{120, 119}},
		{"after the grace period", 0, utc(2027, time.February, 1, 0), []int{120}},
		{"second session", 0, utc(2028, time.January, 10, 0), []int{120}},
		{"pinned", 118, utc(2027, time.January, 5, 0), []int{118}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeCongresses{}
			got, err := trackerAt(store, tt.pinned, tt.now).Targets(t.Context())
			if err != nil {
				t.Fatalf("Targets: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Targets = %v, want %v", got, tt.want)
			}
			wantEnsured := tt.want
			if tt.pinned != 0 {
				wantEnsured = nil // --congress writes nothing
			}
			if !slices.Equal(store.ensured, wantEnsured) {
				t.Errorf("ensured %v, want %v", store.ensured, wantEnsured)
			}
		})
	}
}

// Each congress's row is written once per process, and a failed write is retried next time.
func TestTrackerEnsuresOnce(t *testing.T) {
	store := &fakeCongresses{err: errors.New("spanner down")}
	now := utc(2027, time.January, 3, 1)
	tr := trackerAt(store, 0, now)
	if _, err := tr.Targets(t.Context()); err == nil {
		t.Fatal("Targets with a failing store: no error")
	}

	store.err = nil
	for range 3 {
		if _, err := tr.Targets(t.Context()); err != nil {
			t.Fatalf("Targets: %v", err)
		}
	}
	if !slices.Equal(store.ensured, []int{120, 119}) {
		t.Errorf("ensured %v, want [120 119] once", store.ensured)
	}

	// The process runs on into the next congress: its row is written then.
	tr.now = func() time.Time { return utc(2029, time.January, 3, 0) }
	if _, err := tr.Targets(t.Context()); err != nil {
		t.Fatalf("Targets: %v", err)
	}
	if !slices.Equal(store.ensured, []int{120, 119, 121}) {
		t.Errorf("ensured %v, want [120 119 121]", store.ensured)
	}
}

func TestTrackerMembersSynced(t *testing.T) {
	ctx := t.Context()
	now := utc(2027, time.January, 4, 0)

	// The previous congress and a pinned one are never promoted.
	store := &fakeCongresses{promoteOK: true}
	if err := trackerAt(store, 0, now).MembersSynced(ctx, 119); err != nil {
		t.Fatal(err)
	}
	if err := trackerAt(store, 118, now).MembersSynced(ctx, 118); err != nil {
		t.Fatal(err)
	}
	if len(store.promoted) != 0 {
		t.Errorf("promoted %v, want none", store.promoted)
	}

	// Too few members: it tries again after the next members sync. Once promoted, it stops.
	tr := trackerAt(store, 0, now)
	store.promoteOK = false
	for _, ok := range []bool{false, true, true} {
		store.promoteOK = ok
		if err := tr.MembersSynced(ctx, 120); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(store.promoted, []int{120, 120}) {
		t.Errorf("promoted %v, want [120 120]", store.promoted)
	}

	store.err = errors.New("spanner down")
	if err := trackerAt(store, 0, now).MembersSynced(ctx, 120); !errors.Is(err, store.err) {
		t.Errorf("MembersSynced with a failing store = %v, want %v", err, store.err)
	}
}

// forEach syncs every target even when one fails, and reports each failure.
func TestTrackerForEach(t *testing.T) {
	tr := trackerAt(&fakeCongresses{}, 0, utc(2027, time.January, 10, 0))
	boom := errors.New("boom")
	var synced []int
	err := tr.forEach(t.Context(), func(_ context.Context, c int) error {
		synced = append(synced, c)
		if c == 120 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) || err.Error() != "congress 120: boom" {
		t.Errorf("forEach = %v, want congress 120: boom", err)
	}
	if !slices.Equal(synced, []int{120, 119}) {
		t.Errorf("synced %v, want [120 119]", synced)
	}

	failing := trackerAt(&fakeCongresses{err: boom}, 0, utc(2027, time.January, 10, 0))
	if err = failing.forEach(t.Context(), func(context.Context, int) error {
		t.Error("synced a congress without its row")
		return nil
	}); !errors.Is(err, boom) {
		t.Errorf("forEach with a failing store = %v, want %v", err, boom)
	}
}

// Every sync job asks the tracker first, so none syncs a congress whose row couldn't be written.
// load-uscode syncs no congress.
func TestSyncJobsGoThroughTheTracker(t *testing.T) {
	boom := errors.New("spanner down")
	tr := newCongressTracker(&fakeCongresses{err: boom}, 0, slog.New(slog.DiscardHandler))
	for _, j := range syncJobs(nil, tr, nil) {
		if j.Name == "load-uscode" {
			continue
		}
		if err := j.Run(t.Context()); !errors.Is(err, boom) {
			t.Errorf("%s: Run = %v, want %v", j.Name, err, boom)
		}
	}
}
