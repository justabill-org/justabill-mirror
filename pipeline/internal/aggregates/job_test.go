package aggregates_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs/obstest"
	"github.com/justabill-org/justabill/obs/semconv"
	"github.com/justabill-org/justabill/pipeline/internal/aggregates"
)

// fakeStore serves a fixed snapshot and keeps the cells and alignment rows written to it.
type fakeStore struct {
	snap       *repository.AggregateSnapshot
	cells      map[aggregates.CellKey]model.VoteAggregate
	positions  []repository.SeatPosition
	alignments []model.RepAlignment
	upserts    int
	failList   error
}

func (f *fakeStore) AggregateSnapshot(
	_ context.Context,
	_ repository.AggregateSnapshotParams,
) (*repository.AggregateSnapshot, error) {
	return f.snap, nil
}

func (f *fakeStore) ListVoteAggregates(_ context.Context, billIDs []string) ([]model.VoteAggregate, error) {
	if f.failList != nil {
		return nil, f.failList
	}
	var out []model.VoteAggregate
	for k, c := range f.cells {
		if slices.Contains(billIDs, k.BillID) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) UpsertVoteAggregates(_ context.Context, cells []model.VoteAggregate) error {
	f.upserts++
	for _, c := range cells {
		f.cells[aggregates.CellKey{BillID: c.BillID, Scope: c.Scope, ScopeKey: c.ScopeKey}] = c
	}
	return nil
}

func (f *fakeStore) ReviseVoteAggregates(
	ctx context.Context,
	billIDs []string,
	revise func(stored []model.VoteAggregate) ([]model.VoteAggregate, error),
) error {
	stored, err := f.ListVoteAggregates(ctx, billIDs)
	if err != nil {
		return err
	}
	cells, err := revise(stored)
	if err != nil {
		return err
	}
	return f.UpsertVoteAggregates(ctx, cells)
}

func (f *fakeStore) BillSeatPositions(_ context.Context, billIDs []string) ([]repository.SeatPosition, error) {
	var out []repository.SeatPosition
	for _, p := range f.positions {
		if slices.Contains(billIDs, p.BillID) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) UpsertRepAlignments(_ context.Context, rows []model.RepAlignment) error {
	f.alignments = append(f.alignments, rows...)
	return nil
}

func group(bill, state string, district *int, yea, nay, young int) repository.AggregateVoteGroup {
	all := map[string]int{
		model.AggregateScopeNational: yea + nay, model.AggregateScopeState: yea + nay,
		model.AggregateScopeDistrict: yea + nay,
	}
	youngs := map[string]int{
		model.AggregateScopeNational: young, model.AggregateScopeState: young, model.AggregateScopeDistrict: young,
	}
	return repository.AggregateVoteGroup{
		BillID: bill, State: state, District: district, Yea: yea, Nay: nay, New: all, Young: youngs,
	}
}

func newFakeStore() *fakeStore {
	heldAt := t0().Add(-48 * time.Hour)
	return &fakeStore{
		snap: &repository.AggregateSnapshot{
			Groups: []repository.AggregateVoteGroup{
				group("hr-119-1", "CA", new(12), 40, 20, 0),
				group("hr-119-1", "CA", nil, 10, 0, 0),
				group("hr-119-1", "TX", new(7), 5, 50, 0),
				group("hr-119-1", "", nil, 5, 0, 0),
				group("hr-119-2", "NY", new(1), 50, 10, 60),
			},
			Ineligible: map[string]int{
				repository.IneligibleYoung: 7, repository.IneligibleNoAppCheck: 3, repository.IneligibleExcluded: 2,
			},
			CellBills: []string{"s-119-1"},
		},
		cells: map[aggregates.CellKey]model.VoteAggregate{
			// Every S 1 voter deleted their account since this cell was held.
			{BillID: "s-119-1", Scope: model.AggregateScopeNational}: {
				BillID: "s-119-1", Scope: model.AggregateScopeNational, Status: model.AggregateStatusHeld,
				YeaPct: new(70), NayPct: new(30), VotersFloor: new(100), PublishedAt: &heldAt, ComputedAt: heldAt,
				BasisYea: new(75), BasisNay: new(32), HoldReason: new(model.HoldReasonBurst),
			},
		},
		positions: []repository.SeatPosition{
			{MemberID: "A000001", Congress: 119, ScopeKey: "CA-12", BillID: "hr-119-1", Vote: "yea"},
			{MemberID: "B000002", Congress: 119, ScopeKey: "TX-7", BillID: "hr-119-1", Vote: "yea"},
			{MemberID: "C000003", Congress: 119, ScopeKey: "CA", BillID: "hr-119-1", Vote: "yea"},
			{MemberID: "D000004", Congress: 119, ScopeKey: "CA", BillID: "hr-119-1", Vote: "not_voting"},
			{MemberID: "E000005", Congress: 119, ScopeKey: "OH", BillID: "hr-119-1", Vote: "nay"},
		},
	}
}

func cellSummary(cells map[aggregates.CellKey]model.VoteAggregate) []string {
	out := make([]string, 0, len(cells))
	for k, c := range cells {
		s := fmt.Sprintf("%s|%s|%s|%s", k.BillID, k.Scope, k.ScopeKey, c.Status)
		if c.YeaPct != nil {
			s += fmt.Sprintf("|%d%%|%d+", *c.YeaPct, *c.VotersFloor)
		}
		if c.HoldReason != nil {
			s += "|" + *c.HoldReason
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func TestRunWritesCellsAndAlignment(t *testing.T) {
	tel := obstest.New(t)
	store := newFakeStore()
	job, err := aggregates.New(store, aggregates.DefaultConfig(), tel.Logger, aggregates.WithClock(t0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := job.RunOnce(t.Context())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	want := []string{
		"hr-119-1|district|CA-12|published|67%|60+",
		"hr-119-1|district|TX-7|published|9%|50+",
		"hr-119-1|national||published|46%|130+",
		"hr-119-1|state|CA|published|71%|70+",
		"hr-119-1|state|TX|published|9%|50+",
		"hr-119-2|district|NY-1|suppressed|young_accounts",
		"hr-119-2|national||suppressed",
		"hr-119-2|state|NY|suppressed|young_accounts",
		"s-119-1|national||suppressed",
	}
	if got := cellSummary(store.cells); !reflect.DeepEqual(got, want) {
		t.Errorf("cells:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}

	wantAlign := []model.RepAlignment{
		{MemberID: "A000001", Congress: 119, ScopeKey: "CA-12", BillsCompared: 1, BillsAgreed: 1, ComputedAt: t0()},
		{MemberID: "B000002", Congress: 119, ScopeKey: "TX-7", BillsCompared: 1, BillsAgreed: 0, ComputedAt: t0()},
		{MemberID: "C000003", Congress: 119, ScopeKey: "CA", BillsCompared: 1, BillsAgreed: 1, ComputedAt: t0()},
	}
	if !reflect.DeepEqual(store.alignments, wantAlign) {
		t.Errorf("alignments = %+v, want %+v", store.alignments, wantAlign)
	}
	if res.Eligible != 190 || res.Alignments != 3 || res.Holds[model.HoldReasonYoungAccounts] != 2 {
		t.Errorf("result = %+v, want 190 eligible votes, 3 alignments, 2 young-account holds", res)
	}

	checkMetric(t, tel, semconv.AggregatesCellsName, semconv.AggregatesStatusKey, map[string]int64{
		semconv.AggregatesStatusPublished: 5, semconv.AggregatesStatusSuppressed: 4, semconv.AggregatesStatusHeld: 0,
	})
	checkMetric(t, tel, semconv.AggregatesHoldsName, semconv.AggregatesHoldReasonKey, map[string]int64{
		semconv.AggregatesHoldReasonYoungAccounts: 2,
	})
	checkMetric(t, tel, semconv.AggregatesVotesName, semconv.AggregatesEligibilityKey, map[string]int64{
		semconv.AggregatesEligibilityEligible: 190, semconv.AggregatesEligibilityYoungAccount: 7,
		semconv.AggregatesEligibilityAppCheck: 3, semconv.AggregatesEligibilityExcluded: 2,
	})

	held := 0
	for _, r := range tel.Logs() {
		if r.Body().AsString() == "aggregate cell held" {
			held++
		}
	}
	if held != 2 {
		t.Errorf("held log lines = %d, want 2", held)
	}
}

// TestRunHidesOneVote is the privacy test end to end: a run an hour after a publish, with one
// more vote on a published cell, writes the same numbers.
func TestRunHidesOneVote(t *testing.T) {
	store := newFakeStore()
	now := t0()
	job, err := aggregates.New(store, aggregates.DefaultConfig(), slog.New(slog.DiscardHandler),
		aggregates.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err = job.RunOnce(t.Context()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	before := cellSummary(store.cells)

	store.snap.Groups[0].Yea++ // one more CA-12 Yea vote
	for scope := range store.snap.Groups[0].New {
		store.snap.Groups[0].New[scope] = 1
	}
	now = t0().Add(2 * time.Hour)
	if _, err = job.RunOnce(t.Context()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if after := cellSummary(store.cells); !reflect.DeepEqual(after, before) {
		t.Errorf("one vote changed the published cells:\nbefore %v\n after %v", before, after)
	}
	cell := store.cells[aggregates.CellKey{BillID: "hr-119-1", Scope: model.AggregateScopeDistrict, ScopeKey: "CA-12"}]
	if !cell.PublishedAt.Equal(t0()) || !cell.ComputedAt.Equal(now) {
		t.Errorf("CA-12 published %v, computed %v; want published %v, computed %v", cell.PublishedAt,
			cell.ComputedAt, t0(), now)
	}
}

func TestRunStopsOnStoreError(t *testing.T) {
	store := newFakeStore()
	store.failList = errors.New("spanner down")
	job, err := aggregates.New(store, aggregates.DefaultConfig(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err = job.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "spanner down") {
		t.Errorf("Run = %v, want the store's error", err)
	}
	if store.upserts != 0 || store.alignments != nil {
		t.Errorf("wrote %d cell batches and %d alignments after a failed read", store.upserts, len(store.alignments))
	}
}

func checkMetric(t *testing.T, tel *obstest.Telemetry, name string, key attribute.Key, want map[string]int64) {
	t.Helper()
	m, ok := tel.Metric(t, name)
	if !ok {
		t.Fatalf("metric %s not recorded", name)
	}
	got := map[string]int64{}
	var points []metricdata.DataPoint[int64]
	switch data := m.Data.(type) {
	case metricdata.Gauge[int64]:
		points = data.DataPoints
	case metricdata.Sum[int64]:
		points = data.DataPoints
	default:
		t.Fatalf("metric %s has data %T", name, m.Data)
	}
	for _, p := range points {
		v, _ := p.Attributes.Value(key)
		got[v.AsString()] = p.Value
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
