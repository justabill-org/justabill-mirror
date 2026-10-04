package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

// congressStore keeps congress rows and member-term counts in memory. Any other PipelineStore
// method panics through the nil embedded interface.
type congressStore struct {
	repository.PipelineStore

	rows    map[int]repository.CongressRow
	current int
	terms   map[int]int
	err     error
}

func (f *congressStore) UpsertCongress(_ context.Context, c repository.CongressRow) error {
	if f.err != nil {
		return f.err
	}
	if f.rows == nil {
		f.rows = map[int]repository.CongressRow{}
	}
	f.rows[c.Number] = c
	return nil
}

func (f *congressStore) SetCurrentCongress(_ context.Context, congress int) (bool, error) {
	if _, ok := f.rows[congress]; !ok {
		return false, errors.New("no row")
	}
	changed := f.current != congress
	f.current = congress
	return changed, nil
}

func (f *congressStore) CountMemberTerms(_ context.Context, congress int) (int, error) {
	return f.terms[congress], f.err
}

func TestEnsureCongressWritesItsDates(t *testing.T) {
	store := &congressStore{}
	if err := newBackfillService(store).EnsureCongress(t.Context(), 120); err != nil {
		t.Fatalf("EnsureCongress: %v", err)
	}
	want := repository.CongressRow{
		Number:    120,
		StartDate: time.Date(2027, time.January, 3, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2029, time.January, 3, 0, 0, 0, 0, time.UTC),
	}
	if got := store.rows[120]; got != want {
		t.Errorf("row = %+v, want %+v", got, want)
	}
}

func TestPromoteCongress(t *testing.T) {
	tests := []struct {
		name        string
		terms       int
		wantCurrent bool
	}{
		{"no members yet", 0, false},
		{"members sync under way", MinCurrentMemberTerms - 1, false},
		{"enough members", MinCurrentMemberTerms, true},
		{"every seat", 541, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &congressStore{
				rows:    map[int]repository.CongressRow{119: {Number: 119}, 120: {Number: 120}},
				current: 119,
				terms:   map[int]int{119: 541, 120: tt.terms},
			}
			current, err := newBackfillService(store).PromoteCongress(t.Context(), 120)
			if err != nil {
				t.Fatalf("PromoteCongress: %v", err)
			}
			if current != tt.wantCurrent {
				t.Errorf("PromoteCongress = %t, want %t", current, tt.wantCurrent)
			}
			wantStored := 119
			if tt.wantCurrent {
				wantStored = 120
			}
			if store.current != wantStored {
				t.Errorf("current congress = %d, want %d", store.current, wantStored)
			}
		})
	}
}

func TestPromoteCongressErrors(t *testing.T) {
	boom := errors.New("spanner down")
	if _, err := newBackfillService(
		&congressStore{err: boom},
	).PromoteCongress(t.Context(), 120); !errors.Is(
		err,
		boom,
	) {
		t.Errorf("count failure: err = %v, want %v", err, boom)
	}
	// Enough member terms but no congresses row: SetCurrentCongress's error comes back.
	store := &congressStore{terms: map[int]int{120: MinCurrentMemberTerms}}
	if current, err := newBackfillService(store).PromoteCongress(t.Context(), 120); err == nil || current {
		t.Errorf("missing row: PromoteCongress = %t, %v; want false and an error", current, err)
	}
}
