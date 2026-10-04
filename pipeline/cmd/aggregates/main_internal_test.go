package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

func t0() time.Time { return time.Date(2026, time.October, 20, 15, 0, 0, 0, time.UTC) }

// fakeStore holds cells and one cohort count; it records what's written.
type fakeStore struct {
	snap      *repository.AggregateSnapshot
	cells     []model.VoteAggregate
	writes    int
	cohort    int64
	excluded  *repository.AggregateCohort
	excludeAt time.Time
	closed    bool
}

func (f *fakeStore) AggregateSnapshot(
	context.Context, repository.AggregateSnapshotParams,
) (*repository.AggregateSnapshot, error) {
	return f.snap, nil
}

func (f *fakeStore) ListVoteAggregates(_ context.Context, billIDs []string) ([]model.VoteAggregate, error) {
	var out []model.VoteAggregate
	for _, c := range f.cells {
		for _, id := range billIDs {
			if c.BillID == id {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func (f *fakeStore) ReviseVoteAggregates(
	ctx context.Context,
	billIDs []string,
	revise func([]model.VoteAggregate) ([]model.VoteAggregate, error),
) error {
	stored, _ := f.ListVoteAggregates(ctx, billIDs)
	cells, err := revise(stored)
	if err != nil {
		return err
	}
	f.writes++
	for _, c := range cells {
		for i := range f.cells {
			if f.cells[i].BillID == c.BillID && f.cells[i].Scope == c.Scope && f.cells[i].ScopeKey == c.ScopeKey {
				f.cells[i] = c
			}
		}
	}
	return nil
}

func (f *fakeStore) BillSeatPositions(context.Context, []string) ([]repository.SeatPosition, error) {
	return nil, nil
}

func (f *fakeStore) UpsertRepAlignments(context.Context, []model.RepAlignment) error {
	f.writes++
	return nil
}

func (f *fakeStore) CountAggregateCohort(context.Context, repository.AggregateCohort) (int64, error) {
	return f.cohort, nil
}

func (f *fakeStore) ExcludeAggregateCohort(
	_ context.Context,
	c repository.AggregateCohort,
	at time.Time,
) (int64, error) {
	f.excluded, f.excludeAt = &c, at
	return f.cohort, nil
}

func newStore() *fakeStore {
	at := t0().Add(-2 * time.Hour)
	return &fakeStore{
		snap: &repository.AggregateSnapshot{
			Groups: []repository.AggregateVoteGroup{{
				BillID: "hr-119-1", State: "CA", District: new(12), Yea: 90, Nay: 30,
				New:   map[string]int{"national": 120, "state": 120, "district": 120},
				Young: map[string]int{"national": 0, "state": 0, "district": 50},
			}},
			Ineligible: map[string]int{repository.IneligibleYoung: 4},
		},
		cells: []model.VoteAggregate{
			{
				BillID: "hr-119-1", Scope: "state", ScopeKey: "CA", Status: "published", YeaPct: new(70),
				NayPct: new(30), VotersFloor: new(70), PublishedAt: &at, ComputedAt: at,
				BasisYea: new(52), BasisNay: new(22),
			},
			{
				BillID: "hr-119-1", Scope: "national", Status: "held", YeaPct: new(71), NayPct: new(29),
				VotersFloor: new(100), PublishedAt: &at, ComputedAt: at, BasisYea: new(75), BasisNay: new(30),
				HoldReason: new("swing"),
			},
		},
		cohort: 42,
	}
}

// run runs the command line against store and returns its output.
func run(t *testing.T, s *fakeStore, args ...string) (string, error) {
	t.Helper()
	open := func(context.Context) (store, func(), error) {
		return s, func() { s.closed = true }, nil
	}
	cmd := newCommand(slog.New(slog.DiscardHandler), open, t0)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

func TestReportPrintsHoldsAndWritesNothing(t *testing.T) {
	s := newStore()
	out, err := run(t, s, "report")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	for _, want := range []string{
		"Dry run of aggregate-votes at 2026-10-20T15:00:00Z (nothing written)",
		"Cells: 1 published, 1 suppressed, 1 held",
		"New holds: young_accounts 1",
		"Votes: 120 eligible; ineligible: young_account 4",
		"hr-119-1  national  held",
		"swing",
		"CA-12     suppressed  -    -    -       young_accounts (new)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hr-119-1  CA  ") {
		t.Errorf("report lists the CA cell, which isn't on hold, without --all:\n%s", out)
	}
	if s.writes != 0 || !s.closed {
		t.Errorf("report wrote %d times, closed %v; want 0 writes and a closed store", s.writes, s.closed)
	}

	out, err = run(t, s, "report", "--all")
	if err != nil || !strings.Contains(out, "hr-119-1  CA        published") {
		t.Errorf("report --all = %v:\n%s\nwant the CA cell listed", err, out)
	}
}

func TestReportRejectsBadRules(t *testing.T) {
	t.Setenv("AGG_MIN_CELL_VOTES", "0")
	viper.AutomaticEnv() // main sets it; the rules come from the environment
	if _, err := run(t, newStore(), "report"); err == nil || !strings.Contains(err.Error(), "AGG_MIN_CELL_VOTES") {
		t.Errorf("report with AGG_MIN_CELL_VOTES=0 = %v, want it rejected", err)
	}
}

func TestHoldAndRelease(t *testing.T) {
	s := newStore()
	out, err := run(t, s, "hold", "--bill", "hr-119-1")
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	want := "hr-119-1 national: unchanged, held (swing)\nhr-119-1 CA: published -> held (manual)\n"
	if out != want {
		t.Errorf("hold output:\n%s\nwant\n%s", out, want)
	}
	out, err = run(t, s, "release", "--bill", "hr-119-1", "--cell", "national")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if want = "hr-119-1 national: held (swing) -> published (released)\n"; out != want {
		t.Errorf("release output = %q, want %q", out, want)
	}
	if s.cells[0].Status != "held" || s.cells[1].Status != "published" {
		t.Errorf("cells = %s, %s; want CA held and national published", s.cells[0].Status, s.cells[1].Status)
	}
}

func TestHoldRejectsBadFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"hold"}, "--bill is required"},
		{[]string{"release", "--bill", "hr-119-1", "--cell", "California"}, "--cell"},
		{[]string{"hold", "--bill", "hr-119-9"}, "no stored aggregate cell"},
	} {
		if _, err := run(t, newStore(), tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v = %v, want an error containing %q", tc.args, err, tc.want)
		}
	}
}

func TestExclude(t *testing.T) {
	s := newStore()
	cohort := []string{"exclude", "--provider", "password",
		"--created-from", "2026-10-05T14:00:00Z", "--created-to", "2026-10-05T16:00:00Z"}
	out, err := run(t, s, append(cohort, "--dry-run")...)
	if err != nil || !strings.Contains(out, "Would exclude 42 accounts (sign-in provider password") ||
		s.excluded != nil {
		t.Errorf("exclude --dry-run = %v, %q, excluded %v; want a count and nothing written", err, out, s.excluded)
	}

	out, err = run(t, s, cohort...)
	if err != nil || !strings.Contains(out, "Excluded 42 accounts") {
		t.Fatalf("exclude = %v, %q", err, out)
	}
	wantFrom := time.Date(2026, time.October, 5, 14, 0, 0, 0, time.UTC)
	if s.excluded == nil || s.excluded.Provider != "password" || !s.excluded.CreatedFrom.Equal(wantFrom) ||
		!s.excluded.CreatedTo.Equal(wantFrom.Add(2*time.Hour)) || !s.excludeAt.Equal(t0()) {
		t.Errorf("excluded %+v at %v, want password 14:00-16:00 at %v", s.excluded, s.excludeAt, t0())
	}
}

func TestExcludeRejectsBadFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"exclude", "--created-from", "2026-10-05T14:00:00Z", "--created-to", "2026-10-05T16:00:00Z"},
			"--provider is required"},
		{[]string{"exclude", "--provider", "password", "--created-from", "yesterday",
			"--created-to", "2026-10-05T16:00:00Z"}, "--created-from"},
		{[]string{"exclude", "--provider", "password", "--created-from", "2026-10-05T16:00:00Z",
			"--created-to", "2026-10-05T14:00:00Z"}, "must be before"},
	} {
		s := newStore()
		if _, err := run(t, s, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) || s.excluded != nil {
			t.Errorf("%v = %v, want an error containing %q and nothing excluded", tc.args, err, tc.want)
		}
	}
}

func TestOpenErrorStopsTheCommand(t *testing.T) {
	boom := errors.New("no spanner")
	cmd := newCommand(slog.New(slog.DiscardHandler),
		func(context.Context) (store, func(), error) { return nil, nil, boom }, t0)
	cmd.SetArgs([]string{"hold", "--bill", "hr-119-1"})
	if err := cmd.ExecuteContext(t.Context()); !errors.Is(err, boom) {
		t.Errorf("hold with no store = %v, want %v", err, boom)
	}
}
