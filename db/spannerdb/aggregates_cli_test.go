package spannerdb_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/testdb"
)

func TestReviseVoteAggregatesWritesWhatReviseReturns(t *testing.T) {
	_, store := newAggregateRepos(t)
	ctx := t.Context()
	at := time.Date(2026, time.October, 20, 15, 0, 0, 0, time.UTC)
	if err := store.UpsertVoteAggregates(ctx, aggregateCells(at)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var seen []string
	err := store.ReviseVoteAggregates(ctx, []string{testdb.FixtureHouseBill},
		func(stored []model.VoteAggregate) ([]model.VoteAggregate, error) {
			seen = cellKeys(stored)
			var out []model.VoteAggregate
			for _, c := range stored {
				if c.ScopeKey == "CA-12" {
					c.Status, c.HoldReason = model.AggregateStatusHeld, new(model.HoldReasonManual)
					out = append(out, c)
				}
			}
			return out, nil
		})
	if err != nil {
		t.Fatalf("ReviseVoteAggregates: %v", err)
	}
	wantSeen := []string{
		"hr-119-1|national||published", "hr-119-1|state|CA|held",
		"hr-119-1|state|TX|suppressed", "hr-119-1|district|CA-12|published",
	}
	if !reflect.DeepEqual(seen, wantSeen) {
		t.Errorf("revise saw %v, want %v (only the bill asked for)", seen, wantSeen)
	}
	cells, err := store.ListVoteAggregates(ctx, []string{testdb.FixtureHouseBill})
	if err != nil {
		t.Fatalf("ListVoteAggregates: %v", err)
	}
	district := cells[3]
	if district.Status != model.AggregateStatusHeld || district.HoldReason == nil ||
		*district.HoldReason != model.HoldReasonManual || *district.YeaPct != 61 || *district.BasisYea != 52 {
		t.Errorf("CA-12 = %+v, want held (manual) with its numbers and basis kept", district)
	}

	// An error from revise, or an invalid cell, writes nothing.
	boom := errors.New("boom")
	err = store.ReviseVoteAggregates(ctx, []string{testdb.FixtureHouseBill},
		func(stored []model.VoteAggregate) ([]model.VoteAggregate, error) {
			stored[0].Status = model.AggregateStatusSuppressed
			return stored, boom
		})
	if !errors.Is(err, boom) {
		t.Errorf("revise error = %v, want boom", err)
	}
	err = store.ReviseVoteAggregates(ctx, []string{testdb.FixtureHouseBill},
		func(stored []model.VoteAggregate) ([]model.VoteAggregate, error) {
			stored[0].Status = model.AggregateStatusSuppressed
			stored[1].Status = "gone"
			return stored, nil
		})
	if err == nil {
		t.Error("revise with an unknown status succeeded, want an error")
	}
	after, err := store.ListVoteAggregates(ctx, []string{testdb.FixtureHouseBill})
	if err != nil {
		t.Fatalf("ListVoteAggregates: %v", err)
	}
	if !reflect.DeepEqual(cellKeys(after), cellKeys(cells)) {
		t.Errorf("after failed revisions = %v, want %v unchanged", cellKeys(after), cellKeys(cells))
	}

	called := false
	if err = store.ReviseVoteAggregates(ctx, nil, func([]model.VoteAggregate) ([]model.VoteAggregate, error) {
		called = true
		return nil, nil
	}); err != nil || called {
		t.Errorf("ReviseVoteAggregates(nil) = %v, called %v; want nil, not called", err, called)
	}
}

func TestExcludeAggregateCohort(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	base := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	users := []struct {
		id       string
		provider any
		created  time.Time
	}{
		{"in-1", "password", base},
		{"in-2", "password", base.Add(59 * time.Minute)},
		{"at-end", "password", base.Add(time.Hour)},
		{"before", "password", base.Add(-time.Second)},
		{"google", "google.com", base.Add(time.Minute)},
		{"no-provider", nil, base.Add(time.Minute)},
	}
	cols := []string{"user_id", "auth_uid", "created_at", "sign_in_provider"}
	ms := make([]*spanner.Mutation, 0, len(users))
	for _, u := range users {
		ms = append(ms, spanner.Insert("users", cols, []any{u.id, testdb.TestAuthUID(u.id), u.created, u.provider}))
	}
	if _, err := client.Apply(ctx, ms); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	cohort := repository.AggregateCohort{Provider: "password", CreatedFrom: base, CreatedTo: base.Add(time.Hour)}
	if n, err := store.CountAggregateCohort(ctx, cohort); err != nil || n != 2 {
		t.Fatalf("CountAggregateCohort = %d, %v; want 2 (the range excludes its end)", n, err)
	}
	at := base.Add(24 * time.Hour)
	if n, err := store.ExcludeAggregateCohort(ctx, cohort, at); err != nil || n != 2 {
		t.Fatalf("ExcludeAggregateCohort = %d, %v; want 2", n, err)
	}
	// Idempotent: the second run finds nobody left, and the first time stays.
	if n, err := store.ExcludeAggregateCohort(ctx, cohort, at.Add(time.Hour)); err != nil || n != 0 {
		t.Errorf("second ExcludeAggregateCohort = %d, %v; want 0", n, err)
	}
	if n, err := store.CountAggregateCohort(ctx, cohort); err != nil || n != 0 {
		t.Errorf("CountAggregateCohort after exclude = %d, %v; want 0", n, err)
	}

	for _, u := range users {
		excluded := excludedAt(t, client, u.id)
		want := u.id == "in-1" || u.id == "in-2"
		if excluded.Valid != want || (want && !excluded.Time.Equal(at)) {
			t.Errorf("%s agg_excluded_at = %v, want set to %v: %v", u.id, excluded, at, want)
		}
	}
}

func TestAggregateCohortRejectsBadCohorts(t *testing.T) {
	store, _ := newLinkStore(t)
	base := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	for name, bad := range map[string]repository.AggregateCohort{
		"no provider": {CreatedFrom: base, CreatedTo: base.Add(time.Hour)},
		"empty range": {Provider: "password", CreatedFrom: base, CreatedTo: base},
	} {
		if _, err := store.ExcludeAggregateCohort(t.Context(), bad, base); err == nil {
			t.Errorf("%s: ExcludeAggregateCohort succeeded, want an error", name)
		}
		if _, err := store.CountAggregateCohort(t.Context(), bad); err == nil {
			t.Errorf("%s: CountAggregateCohort succeeded, want an error", name)
		}
	}
}

func excludedAt(t *testing.T, client *spanner.Client, userID string) spanner.NullTime {
	t.Helper()
	row, err := client.Single().ReadRow(t.Context(), "users", spanner.Key{userID}, []string{"agg_excluded_at"})
	if err != nil {
		t.Fatalf("read %s: %v", userID, err)
	}
	var excluded spanner.NullTime
	if err = row.Columns(&excluded); err != nil {
		t.Fatal(err)
	}
	return excluded
}
