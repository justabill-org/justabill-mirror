package aggregates_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/obs/obstest"
	"github.com/justabill-org/justabill/pipeline/internal/aggregates"
)

func TestParseCell(t *testing.T) {
	for name, want := range map[string]aggregates.CellKey{
		"national": {BillID: "hr-119-1", Scope: model.AggregateScopeNational},
		"ca":       {BillID: "hr-119-1", Scope: model.AggregateScopeState, ScopeKey: "CA"},
		"CA-12":    {BillID: "hr-119-1", Scope: model.AggregateScopeDistrict, ScopeKey: "CA-12"},
		"ak-0":     {BillID: "hr-119-1", Scope: model.AggregateScopeDistrict, ScopeKey: "AK-0"},
	} {
		got, err := aggregates.ParseCell("hr-119-1", name)
		if err != nil || got != want {
			t.Errorf("ParseCell(%q) = %+v, %v; want %+v", name, got, err, want)
		}
	}
	for _, bad := range []string{"", "C", "CAL", "CA-", "CA-123", "CA12", "12"} {
		if got, err := aggregates.ParseCell("hr-119-1", bad); err == nil {
			t.Errorf("ParseCell(%q) = %+v, want an error", bad, got)
		}
	}
}

// reviewStore holds one bill's district cell in each state a hold or release can meet, and a
// national cell on another bill.
func reviewStore() *fakeStore {
	cells := []*model.VoteAggregate{
		withKey(publishedCell(), "CA-1"),
		withKey(suppressedCell(""), "CA-2"),
		withKey(withStatus(publishedCell(), model.AggregateStatusHeld, model.HoldReasonBurst), "CA-3"),
		withKey(suppressedCell(model.HoldReasonYoungAccounts), "CA-4"),
		withKey(withStatus(publishedCell(), model.AggregateStatusPublished, model.HoldReasonReleased), "CA-5"),
	}
	other := publishedCell()
	other.BillID, other.Scope, other.ScopeKey = "s-119-1", model.AggregateScopeNational, ""
	cells = append(cells, other)
	f := &fakeStore{cells: map[aggregates.CellKey]model.VoteAggregate{}}
	for _, c := range cells {
		f.cells[aggregates.CellKey{BillID: c.BillID, Scope: c.Scope, ScopeKey: c.ScopeKey}] = *c
	}
	return f
}

func withKey(c *model.VoteAggregate, scopeKey string) *model.VoteAggregate {
	c.ScopeKey = scopeKey
	return c
}

// changeSummary is each change as "key before -> after changed".
func changeSummary(changes []aggregates.Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		s := c.Before.ScopeKey + " " + brief(&c.Before) + " -> " + brief(&c.After)
		if !c.Changed {
			s += " (unchanged)"
		}
		out = append(out, s)
	}
	return out
}

func brief(c *model.VoteAggregate) string {
	s := c.Status
	if c.HoldReason != nil {
		s += "/" + *c.HoldReason
	}
	return s
}

func TestHoldAndRelease(t *testing.T) {
	store := reviewStore()
	changes, err := aggregates.Hold(t.Context(), store, "hr-119-1", nil)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	want := []string{
		"CA-1 published -> held/manual",
		"CA-2 suppressed -> suppressed/manual",
		"CA-3 held/burst -> held/burst (unchanged)",
		"CA-4 suppressed/young_accounts -> suppressed/young_accounts (unchanged)",
		"CA-5 published/released -> held/manual",
	}
	if got := changeSummary(sorted(changes)); !reflect.DeepEqual(got, want) {
		t.Errorf("Hold:\n got %v\nwant %v", got, want)
	}
	if store.upserts != 1 {
		t.Errorf("Hold wrote %d times, want 1 transaction", store.upserts)
	}
	held := store.cells[aggregates.CellKey{BillID: "hr-119-1", Scope: model.AggregateScopeDistrict, ScopeKey: "CA-1"}]
	if held.YeaPct == nil || *held.YeaPct != 50 || *held.BasisYea != 30 {
		t.Errorf("held CA-1 = %+v, want its numbers and basis kept", held)
	}

	changes, err = aggregates.Release(t.Context(), store, "hr-119-1", nil)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	want = []string{
		"CA-1 held/manual -> published/released",
		"CA-2 suppressed/manual -> suppressed/released",
		"CA-3 held/burst -> published/released",
		"CA-4 suppressed/young_accounts -> suppressed/released",
		"CA-5 held/manual -> published/released",
	}
	if got := changeSummary(sorted(changes)); !reflect.DeepEqual(got, want) {
		t.Errorf("Release:\n got %v\nwant %v", got, want)
	}
	other := store.cells[aggregates.CellKey{BillID: "s-119-1", Scope: model.AggregateScopeNational}]
	if other.Status != model.AggregateStatusPublished || other.HoldReason != nil {
		t.Errorf("other bill's cell = %+v, want untouched", other)
	}
}

func sorted(changes []aggregates.Change) []aggregates.Change {
	out := slices.Clone(changes)
	slices.SortFunc(
		out,
		func(a, b aggregates.Change) int { return strings.Compare(a.Before.ScopeKey, b.Before.ScopeKey) },
	)
	return out
}

func TestHoldOneCell(t *testing.T) {
	store := reviewStore()
	cell, err := aggregates.ParseCell("hr-119-1", "CA-1")
	if err != nil {
		t.Fatal(err)
	}
	changes, err := aggregates.Hold(t.Context(), store, "hr-119-1", &cell)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if got := changeSummary(changes); !reflect.DeepEqual(got, []string{"CA-1 published -> held/manual"}) {
		t.Errorf("Hold(CA-1) = %v", got)
	}
	if c := store.cells[aggregates.CellKey{BillID: "hr-119-1", Scope: model.AggregateScopeDistrict,
		ScopeKey: "CA-2"}]; c.HoldReason != nil {
		t.Errorf("CA-2 = %+v, want untouched", c)
	}

	// Nothing to act on, or releasing a cell that isn't on hold: no write.
	store.upserts = 0
	missing, _ := aggregates.ParseCell("hr-119-1", "NY-3")
	if _, err = aggregates.Release(t.Context(), store, "hr-119-1", &missing); !errors.Is(err, aggregates.ErrNoCells) {
		t.Errorf("Release(NY-3) = %v, want ErrNoCells", err)
	}
	if _, err = aggregates.Hold(t.Context(), store, "hr-119-9", nil); !errors.Is(err, aggregates.ErrNoCells) {
		t.Errorf("Hold(no cells) = %v, want ErrNoCells", err)
	}
	two, _ := aggregates.ParseCell("hr-119-1", "CA-2")
	changes, err = aggregates.Release(t.Context(), store, "hr-119-1", &two)
	if err != nil || len(changes) != 1 || changes[0].Changed {
		t.Errorf("Release(CA-2, not on hold) = %v, %v; want one unchanged cell", changeSummary(changes), err)
	}
}

// A manual hold stops the next run from republishing; a release lets it publish numbers the
// anomaly rules would otherwise hold (here a 30-point swing).
func TestHoldAndReleaseAgainstPublish(t *testing.T) {
	store := reviewStore()
	cell, _ := aggregates.ParseCell("hr-119-1", "CA-1")
	swing := aggregates.Counts{Yea: 64, Nay: 16, New: 20}
	cfg := aggregates.DefaultConfig()

	if _, err := aggregates.Hold(t.Context(), store, "hr-119-1", &cell); err != nil {
		t.Fatal(err)
	}
	prev := store.cells[cell]
	out := aggregates.Publish(cell, &prev, swing, cfg, t0())
	if out.Cell.Status != model.AggregateStatusHeld || *out.Cell.YeaPct != 50 || out.NewHold != "" {
		t.Errorf("run after a manual hold = %+v, want still held at 50%%", out)
	}

	if _, err := aggregates.Release(t.Context(), store, "hr-119-1", &cell); err != nil {
		t.Fatal(err)
	}
	prev = store.cells[cell]
	out = aggregates.Publish(cell, &prev, swing, cfg, t0())
	if out.Cell.Status != model.AggregateStatusPublished || *out.Cell.YeaPct != 80 || out.Cell.HoldReason != nil {
		t.Errorf("run after a release = %+v, want published at 80%% with no hold", out)
	}
}

func TestReportWritesNothing(t *testing.T) {
	tel := obstest.New(t)
	store := newFakeStore()
	before := cellSummary(store.cells)
	job, err := aggregates.New(store, aggregates.DefaultConfig(), tel.Logger, aggregates.WithClock(t0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := job.Report(t.Context())
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if after := cellSummary(store.cells); !reflect.DeepEqual(after, before) || store.upserts != 0 ||
		store.alignments != nil {
		t.Errorf("Report wrote: cells %v (%d upserts), %d alignments", after, store.upserts, len(store.alignments))
	}

	// It decides what RunOnce would write, in bill and scope order.
	decided := map[aggregates.CellKey]model.VoteAggregate{}
	for _, d := range res.Decided {
		decided[aggregates.CellKey{BillID: d.Cell.BillID, Scope: d.Cell.Scope, ScopeKey: d.Cell.ScopeKey}] = d.Cell
	}
	if len(res.Decided) != 9 || res.Decided[0].Cell.BillID != "hr-119-1" ||
		res.Decided[0].Cell.Scope != model.AggregateScopeDistrict {
		t.Errorf("Decided = %d cells starting %+v, want 9 from hr-119-1's districts", len(res.Decided), res.Decided[0])
	}
	runStore := newFakeStore()
	runJob, err := aggregates.New(runStore, aggregates.DefaultConfig(), tel.Logger, aggregates.WithClock(t0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runJob.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, want := cellSummary(decided), cellSummary(runStore.cells); !reflect.DeepEqual(got, want) {
		t.Errorf("Report decided %v, RunOnce wrote %v", got, want)
	}
	if res.Alignments != 3 || res.Holds[model.HoldReasonYoungAccounts] != 2 || res.Eligible != 190 {
		t.Errorf("Report result = %+v, want 3 alignments, 2 young-account holds, 190 eligible", res)
	}
}
