package spannerdb_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

func newAggregateRepos(t *testing.T) (*spannerdb.AggregateRepository, *spannerdb.PipelineStoreImpl) {
	t.Helper()
	store, client := newLinkStore(t)
	return spannerdb.NewAggregateReader(&spannerdb.Client{Spanner: client}), store
}

// aggregateCells returns one bill's cells in every status, plus a national cell on another bill.
func aggregateCells(at time.Time) []model.VoteAggregate {
	return []model.VoteAggregate{
		{
			BillID: testdb.FixtureHouseBill, Scope: model.AggregateScopeDistrict, ScopeKey: "CA-12",
			Status: model.AggregateStatusPublished, YeaPct: new(61), NayPct: new(39),
			VotersFloor: new(80), PublishedAt: &at, ComputedAt: at,
			BasisYea: new(52), BasisNay: new(33),
		},
		{
			BillID: testdb.FixtureHouseBill, Scope: model.AggregateScopeState, ScopeKey: "TX",
			Status: model.AggregateStatusSuppressed, ComputedAt: at,
			BasisYea: new(20), BasisNay: new(9),
		},
		{
			BillID: testdb.FixtureHouseBill, Scope: model.AggregateScopeState, ScopeKey: "CA",
			Status: model.AggregateStatusHeld, YeaPct: new(55), NayPct: new(45),
			VotersFloor: new(310), PublishedAt: &at, ComputedAt: at,
			BasisYea: new(175), BasisNay: new(140), HoldReason: new("burst"),
		},
		{
			BillID: testdb.FixtureHouseBill, Scope: model.AggregateScopeNational,
			Status: model.AggregateStatusPublished, YeaPct: new(50), NayPct: new(50),
			VotersFloor: new(1200), PublishedAt: &at, ComputedAt: at,
			BasisYea: new(601), BasisNay: new(603),
		},
		{
			BillID: testdb.FixtureSenateBill, Scope: model.AggregateScopeNational,
			Status: model.AggregateStatusPublished, YeaPct: new(70), NayPct: new(30),
			VotersFloor: new(100), PublishedAt: &at, ComputedAt: at,
			BasisYea: new(75), BasisNay: new(32),
		},
	}
}

func cellKeys(cells []model.VoteAggregate) []string {
	keys := make([]string, 0, len(cells))
	for _, c := range cells {
		keys = append(keys, c.BillID+"|"+c.Scope+"|"+c.ScopeKey+"|"+c.Status)
	}
	return keys
}

func TestAggregateReaderServesPublishedAndHeldCellsOnly(t *testing.T) {
	reader, store := newAggregateRepos(t)
	ctx := t.Context()
	at := time.Date(2026, time.October, 20, 15, 0, 0, 0, time.UTC)
	if err := store.UpsertVoteAggregates(ctx, aggregateCells(at)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	cells, err := reader.BillAggregates(ctx, testdb.FixtureHouseBill)
	if err != nil {
		t.Fatalf("BillAggregates: %v", err)
	}
	want := []string{
		"hr-119-1|national||published",
		"hr-119-1|state|CA|held",
		"hr-119-1|district|CA-12|published",
	}
	if got := cellKeys(cells); !reflect.DeepEqual(got, want) {
		t.Fatalf("BillAggregates = %v, want %v (national first, no suppressed cells)", got, want)
	}
	for _, c := range cells {
		if c.BasisYea != nil || c.BasisNay != nil || c.HoldReason != nil {
			t.Errorf("%s: reader returned bookkeeping %v/%v/%v; it must never read it",
				c.ScopeKey, c.BasisYea, c.BasisNay, c.HoldReason)
		}
	}
	district := cells[2]
	if *district.YeaPct != 61 || *district.NayPct != 39 || *district.VotersFloor != 80 ||
		!district.PublishedAt.Equal(at) || !district.ComputedAt.Equal(at) {
		t.Errorf("district cell = %+v, want 61/39, 80 voters, published and computed at %v", district, at)
	}

	for key, wantStatus := range map[string]string{
		"":      model.AggregateStatusPublished,
		"CA":    model.AggregateStatusHeld,
		"CA-12": model.AggregateStatusPublished,
		"TX":    "",
		"NY-3":  "",
	} {
		cell, getErr := reader.BillAggregate(ctx, testdb.FixtureHouseBill, key)
		if getErr != nil {
			t.Fatalf("BillAggregate(%q): %v", key, getErr)
		}
		switch {
		case wantStatus == "" && cell != nil:
			t.Errorf("BillAggregate(%q) = %+v, want nil", key, cell)
		case wantStatus != "" && (cell == nil || cell.Status != wantStatus || cell.BasisYea != nil):
			t.Errorf("BillAggregate(%q) = %+v, want a %s cell without bookkeeping", key, cell, wantStatus)
		}
	}

	if none, getErr := reader.BillAggregates(ctx, testdb.FixturePrevHouseBill); getErr != nil || none != nil {
		t.Errorf("BillAggregates(no cells) = %v, %v; want nil, nil", none, getErr)
	}
}

func TestListVoteAggregatesReadsBookkeeping(t *testing.T) {
	_, store := newAggregateRepos(t)
	ctx := t.Context()
	at := time.Date(2026, time.October, 20, 15, 0, 0, 0, time.UTC)
	if err := store.UpsertVoteAggregates(ctx, aggregateCells(at)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	cells, err := store.ListVoteAggregates(ctx, []string{testdb.FixtureHouseBill, testdb.FixturePrevHouseBill})
	if err != nil {
		t.Fatalf("ListVoteAggregates: %v", err)
	}
	want := []string{
		"hr-119-1|national||published",
		"hr-119-1|state|CA|held",
		"hr-119-1|state|TX|suppressed",
		"hr-119-1|district|CA-12|published",
	}
	if got := cellKeys(cells); !reflect.DeepEqual(got, want) {
		t.Fatalf("ListVoteAggregates = %v, want %v (every status, only the bills asked for)", got, want)
	}
	held, suppressed := cells[1], cells[2]
	if held.HoldReason == nil || *held.HoldReason != "burst" || *held.BasisYea != 175 || *held.BasisNay != 140 {
		t.Errorf("held cell = %+v, want hold reason burst and basis 175/140", held)
	}
	if suppressed.YeaPct != nil || suppressed.VotersFloor != nil || suppressed.PublishedAt != nil ||
		*suppressed.BasisYea != 20 {
		t.Errorf("suppressed cell = %+v, want no published numbers and basis yea 20", suppressed)
	}

	// Republishing a cell overwrites it in place.
	later := at.Add(time.Hour)
	released := held
	released.Status, released.HoldReason, released.YeaPct, released.ComputedAt =
		model.AggregateStatusPublished, nil, new(58), later
	if err = store.UpsertVoteAggregates(ctx, []model.VoteAggregate{released}); err != nil {
		t.Fatalf("republish: %v", err)
	}
	cells, err = store.ListVoteAggregates(ctx, []string{testdb.FixtureHouseBill})
	if err != nil {
		t.Fatalf("ListVoteAggregates after republish: %v", err)
	}
	if got := cells[1]; got.Status != model.AggregateStatusPublished || got.HoldReason != nil ||
		*got.YeaPct != 58 || !got.ComputedAt.Equal(later) || len(cells) != len(want) {
		t.Errorf("after republish CA = %+v (of %d cells), want published at 58%% with no hold", got, len(cells))
	}

	if none, listErr := store.ListVoteAggregates(ctx, nil); listErr != nil || none != nil {
		t.Errorf("ListVoteAggregates(nil) = %v, %v; want nil, nil", none, listErr)
	}
}

func TestUpsertVoteAggregatesRejectsBadCells(t *testing.T) {
	_, store := newAggregateRepos(t)
	ctx := t.Context()
	at := time.Now().UTC()
	good := model.VoteAggregate{
		BillID: testdb.FixtureHouseBill, Scope: model.AggregateScopeNational,
		Status: model.AggregateStatusSuppressed, ComputedAt: at,
	}

	tests := map[string]struct {
		mutate  func(c *model.VoteAggregate)
		wantErr string
	}{
		"unknown scope":       {func(c *model.VoteAggregate) { c.Scope = "county" }, `unknown scope "county"`},
		"national with a key": {func(c *model.VoteAggregate) { c.ScopeKey = "CA" }, "want empty"},
		"state without a key": {
			func(c *model.VoteAggregate) { c.Scope = model.AggregateScopeState },
			"no scope key",
		},
		"district without a key": {
			func(c *model.VoteAggregate) { c.Scope = model.AggregateScopeDistrict },
			"no scope key",
		},
		"unknown status": {func(c *model.VoteAggregate) { c.Status = "draft" }, `unknown status "draft"`},
		"empty status":   {func(c *model.VoteAggregate) { c.Status = "" }, `unknown status ""`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			bad := good
			tt.mutate(&bad)
			err := store.UpsertVoteAggregates(ctx, []model.VoteAggregate{good, bad})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("upsert = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}

	// A rejected batch writes nothing, not even its valid cells.
	if cells, err := store.ListVoteAggregates(ctx, []string{testdb.FixtureHouseBill}); err != nil || cells != nil {
		t.Errorf("after rejected batches = %v, %v; want no cells", cells, err)
	}
	if err := store.UpsertVoteAggregates(ctx, nil); err != nil {
		t.Errorf("upsert(nil) = %v, want nil", err)
	}
	// Cells hang off bills: one for a bill that isn't synced fails.
	orphan := good
	orphan.BillID = "hr-119-9999"
	if err := store.UpsertVoteAggregates(ctx, []model.VoteAggregate{orphan}); err == nil {
		t.Error("upsert for a missing bill: want an error (vote_aggregates is interleaved in bills)")
	}
}

func TestVoteAggregateJSONOmitsBookkeeping(t *testing.T) {
	at := time.Date(2026, time.October, 20, 15, 0, 0, 0, time.UTC)
	data, err := json.Marshal(aggregateCells(at)[2])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"basis", "hold", "175", "140", "burst"} {
		if strings.Contains(string(data), field) {
			t.Errorf("JSON %s contains %q; bookkeeping must never be served", data, field)
		}
	}
	if !strings.Contains(string(data), `"yea_pct":55`) {
		t.Errorf("JSON %s lacks yea_pct", data)
	}
}

func TestRepAlignmentRoundTrip(t *testing.T) {
	reader, store := newAggregateRepos(t)
	ctx := t.Context()
	at := time.Date(2026, time.October, 20, 15, 0, 0, 0, time.UTC)
	rows := []model.RepAlignment{
		{MemberID: testdb.FixtureHouseDem, Congress: testdb.FixturePrevCongress, ScopeKey: "CA-12",
			BillsCompared: 5, BillsAgreed: 2, ComputedAt: at},
		{MemberID: testdb.FixtureHouseDem, Congress: testdb.FixtureCongress, ScopeKey: "CA-12",
			BillsCompared: 22, BillsAgreed: 14, ComputedAt: at},
		{MemberID: testdb.FixtureSenatorRep, Congress: testdb.FixtureCongress, ScopeKey: "OH",
			BillsCompared: 3, BillsAgreed: 3, ComputedAt: at},
	}
	if err := store.UpsertRepAlignments(ctx, rows); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows[1].BillsCompared, rows[1].BillsAgreed = 23, 15
	if err := store.UpsertRepAlignments(ctx, rows[1:2]); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := reader.MemberAlignment(ctx, testdb.FixtureHouseDem)
	if err != nil {
		t.Fatalf("MemberAlignment: %v", err)
	}
	if len(got) != 2 || !reflect.DeepEqual(got[0], rows[1]) || !reflect.DeepEqual(got[1], rows[0]) {
		t.Errorf("MemberAlignment = %+v, want the 119th row (23/15) then the 118th", got)
	}
	if none, getErr := reader.MemberAlignment(ctx, testdb.FixtureHouseRep); getErr != nil || none != nil {
		t.Errorf("MemberAlignment(no rows) = %v, %v; want nil, nil", none, getErr)
	}

	bad := rows[2]
	bad.BillsAgreed = 4
	if err = store.UpsertRepAlignments(ctx, []model.RepAlignment{bad}); err == nil ||
		!strings.Contains(err.Error(), "agreed on 4 of 3") {
		t.Errorf("upsert with agreed > compared = %v, want an error", err)
	}
	if err = store.UpsertRepAlignments(ctx, nil); err != nil {
		t.Errorf("upsert(nil) = %v, want nil", err)
	}
}

// The eligibility columns are nullable, so rows written by code that predates them read as NULL.
func TestEligibilityColumnsDefaultToNull(t *testing.T) {
	_, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	testdb.SeedCongress(ctx, t, client, testdb.FixtureCongress)
	testdb.SeedBill(ctx, t, client, "hr-119-7", testdb.FixtureCongress, "hr", 7, "A bill")
	testdb.SeedUserVote(ctx, t, client, "u1", "hr-119-7", "yea")

	row, err := client.Single().ReadRow(ctx, "users", spanner.Key{"u1"},
		[]string{"sign_in_provider", "district_changed_at", "agg_excluded_at"})
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	var provider spanner.NullString
	var changed, excluded spanner.NullTime
	if err = row.Columns(&provider, &changed, &excluded); err != nil {
		t.Fatal(err)
	}
	if provider.Valid || changed.Valid || excluded.Valid {
		t.Errorf("new user eligibility columns = %v, %v, %v; want all NULL", provider, changed, excluded)
	}

	row, err = client.Single().ReadRow(ctx, "user_votes", spanner.Key{"u1", "hr-119-7"}, []string{"app_check_ok"})
	if err != nil {
		t.Fatalf("read vote: %v", err)
	}
	var appCheck spanner.NullBool
	if err = row.Columns(&appCheck); err != nil {
		t.Fatal(err)
	}
	if appCheck.Valid {
		t.Errorf("app_check_ok = %v, want NULL", appCheck)
	}
}
