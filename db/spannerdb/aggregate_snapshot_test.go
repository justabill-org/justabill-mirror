package spannerdb_test

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/testdb"
)

// snapshotUser is a user with one vote on the fixture's House bill.
type snapshotUser struct {
	id         string
	state      string // "" for none
	district   *int
	age        time.Duration // how long before asOf the account was created
	excluded   bool
	changed    bool // district_changed_at is set
	vote       string
	votedAgo   time.Duration
	appCheckOK *bool
}

func seedSnapshotUsers(t *testing.T, client *spanner.Client, asOf time.Time, users []snapshotUser) {
	t.Helper()
	var ms []*spanner.Mutation
	for _, u := range users {
		var (
			state             spanner.NullString
			district          spanner.NullInt64
			excluded, changed spanner.NullTime
			appCheck          spanner.NullBool
		)
		if u.state != "" {
			state = spanner.NullString{StringVal: u.state, Valid: true}
		}
		if u.district != nil {
			district = spanner.NullInt64{Int64: int64(*u.district), Valid: true}
		}
		if u.excluded {
			excluded = spanner.NullTime{Time: asOf.Add(-time.Hour), Valid: true}
		}
		if u.changed {
			changed = spanner.NullTime{Time: asOf.Add(-24 * time.Hour), Valid: true}
		}
		if u.appCheckOK != nil {
			appCheck = spanner.NullBool{Bool: *u.appCheckOK, Valid: true}
		}
		ms = append(ms,
			spanner.Insert("users",
				[]string{"user_id", "auth_uid", "state", "district", "created_at", "agg_excluded_at",
					"district_changed_at"},
				[]any{u.id, testdb.TestAuthUID(u.id), state, district, asOf.Add(-u.age), excluded, changed}),
			spanner.Insert("user_votes",
				[]string{"user_id", "bill_id", "vote", "voted_at", "app_check_ok"},
				[]any{u.id, testdb.FixtureHouseBill, u.vote, asOf.Add(-u.votedAgo), appCheck}),
		)
	}
	if _, err := client.Apply(t.Context(), ms); err != nil {
		t.Fatalf("seed users: %v", err)
	}
}

func snapshotParams(asOf time.Time, requireAppCheck bool) repository.AggregateSnapshotParams {
	return repository.AggregateSnapshotParams{
		AsOf: asOf, MinAccountAge: 48 * time.Hour, YoungAccountAge: 7 * 24 * time.Hour,
		RequireAppCheck: requireAppCheck, BurstWindow: time.Hour, BaselineWindow: 7 * 24 * time.Hour,
	}
}

// groupString renders a group as bill|state|district|yea/nay|recent/baseline|new n,s,d|young n,s,d.
func groupString(g repository.AggregateVoteGroup) string {
	district := "-"
	if g.District != nil {
		district = strconv.Itoa(*g.District)
	}
	n, s, d := model.AggregateScopeNational, model.AggregateScopeState, model.AggregateScopeDistrict
	return fmt.Sprintf("%s|%s|%s|%d/%d|%d/%d|new %d,%d,%d|young %d,%d,%d", g.BillID, g.State, district,
		g.Yea, g.Nay, g.Recent, g.Baseline, g.New[n], g.New[s], g.New[d], g.Young[n], g.Young[s], g.Young[d])
}

func snapshotStrings(snap *repository.AggregateSnapshot) []string {
	out := make([]string, 0, len(snap.Groups))
	for _, g := range snap.Groups {
		out = append(out, groupString(g))
	}
	slices.Sort(out)
	return out
}

func TestAggregateSnapshotCountsOnlyEligibleVotes(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	asOf := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	ok, notOK := true, false
	seedSnapshotUsers(t, client, asOf, []snapshotUser{
		{id: "old-yea", state: "CA", district: new(12), age: 10 * day, vote: "yea", votedAgo: 3 * day, appCheckOK: &ok},
		{id: "old-nay", state: "CA", district: new(12), age: 10 * day, vote: "nay", votedAgo: 10 * time.Minute,
			appCheckOK: &ok},
		{id: "young", state: "CA", age: 3 * day, vote: "yea", votedAgo: 2 * day, appCheckOK: &ok},
		{id: "no-state", age: 10 * day, vote: "yea", votedAgo: 5 * time.Hour, appCheckOK: &ok},
		{id: "moved", state: "TX", district: new(7), age: 40 * day, changed: true, vote: "nay", votedAgo: 20 * day,
			appCheckOK: &ok},
		{id: "brand-new", state: "CA", district: new(12), age: time.Hour, vote: "yea", votedAgo: time.Minute,
			appCheckOK: &ok},
		{id: "failed-check", state: "TX", district: new(7), age: 10 * day, vote: "yea", votedAgo: day,
			appCheckOK: &notOK},
		{id: "no-check", state: "TX", district: new(7), age: 10 * day, vote: "yea", votedAgo: day},
		{id: "excluded", state: "CA", district: new(12), age: 10 * day, excluded: true, vote: "yea", votedAgo: day,
			appCheckOK: &ok},
		{id: "skipper", state: "CA", district: new(12), age: 10 * day, vote: "skip", votedAgo: day, appCheckOK: &ok},
		{id: "deleted", state: "CA", district: new(12), age: 10 * day, vote: "yea", votedAgo: day, appCheckOK: &ok},
	})
	// A deleted account's votes go with it (the interleave cascades).
	if _, err := client.Apply(ctx, []*spanner.Mutation{spanner.Delete("users", spanner.Key{"deleted"})}); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	// The national cell was published a day ago, the state cell is suppressed (never published).
	published := asOf.Add(-day)
	if err := store.UpsertVoteAggregates(ctx, []model.VoteAggregate{
		{
			BillID:      testdb.FixtureHouseBill,
			Scope:       model.AggregateScopeNational,
			Status:      model.AggregateStatusPublished,
			YeaPct:      new(50),
			NayPct:      new(50),
			VotersFloor: new(100),
			PublishedAt: &published,
			ComputedAt:  published,
		},
		{
			BillID: testdb.FixtureSenateBill, Scope: model.AggregateScopeState, ScopeKey: "CA",
			Status: model.AggregateStatusSuppressed, ComputedAt: published,
		},
	}); err != nil {
		t.Fatalf("upsert cells: %v", err)
	}

	snap, err := store.AggregateSnapshot(ctx, snapshotParams(asOf, true))
	if err != nil {
		t.Fatalf("AggregateSnapshot: %v", err)
	}
	want := []string{
		"hr-119-1|CA|-|1/0|0/1|new 0,1,1|young 0,1,1",
		"hr-119-1|CA|12|1/1|1/1|new 1,2,2|young 0,0,0",
		"hr-119-1|TX|7|0/1|0/0|new 0,1,1|young 0,0,0",
		"hr-119-1||-|1/0|0/1|new 1,1,1|young 0,0,0",
	}
	if got := snapshotStrings(snap); !reflect.DeepEqual(got, want) {
		t.Errorf("groups:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
	wantIneligible := map[string]int{
		repository.IneligibleExcluded: 1, repository.IneligibleYoung: 1, repository.IneligibleNoAppCheck: 2,
	}
	if !reflect.DeepEqual(snap.Ineligible, wantIneligible) {
		t.Errorf("ineligible = %v, want %v", snap.Ineligible, wantIneligible)
	}
	if wantBills := []string{testdb.FixtureHouseBill}; !reflect.DeepEqual(snap.CellBills, wantBills) {
		t.Errorf("cell bills = %v, want %v", snap.CellBills, wantBills)
	}

	// Without the App Check requirement, the two TX votes without a passing check count.
	snap, err = store.AggregateSnapshot(ctx, snapshotParams(asOf, false))
	if err != nil {
		t.Fatalf("AggregateSnapshot without App Check: %v", err)
	}
	if got := snapshotStrings(snap); !slices.Contains(got, "hr-119-1|TX|7|2/1|0/2|new 0,3,3|young 0,0,0") {
		t.Errorf("groups without App Check = %v, want TX-7 with 2 yea and 1 nay", got)
	}
	if snap.Ineligible[repository.IneligibleNoAppCheck] != 0 {
		t.Errorf("no-App-Check ineligible = %d, want 0", snap.Ineligible[repository.IneligibleNoAppCheck])
	}
}

func TestBillSeatPositions(t *testing.T) {
	store, _ := newLinkStore(t)
	positions, err := store.BillSeatPositions(t.Context(),
		[]string{testdb.FixtureHouseBill, testdb.FixtureSenateBill, "hr-119-999"})
	if err != nil {
		t.Fatalf("BillSeatPositions: %v", err)
	}
	want := []repository.SeatPosition{
		{
			MemberID: testdb.FixtureHouseDem,
			Congress: 119,
			ScopeKey: "CA-12",
			BillID:   testdb.FixtureHouseBill,
			Vote:     "yea",
		},
		{
			MemberID: testdb.FixtureHouseRep,
			Congress: 119,
			ScopeKey: "TX-7",
			BillID:   testdb.FixtureHouseBill,
			Vote:     "nay",
		},
		{
			MemberID: testdb.FixtureSenatorRep,
			Congress: 119,
			ScopeKey: "OH",
			BillID:   testdb.FixtureSenateBill,
			Vote:     "yea",
		},
	}
	if !reflect.DeepEqual(positions, want) {
		t.Errorf("positions = %+v, want %+v", positions, want)
	}

	if positions, err = store.BillSeatPositions(t.Context(), nil); err != nil || positions != nil {
		t.Errorf("no bills: %v, %v; want nil, nil", positions, err)
	}
}

// An imported vote keeps the browser's voted_at, but the burst and new-since-publish windows
// count it when the server stored it (#458).
func TestAggregateSnapshotWindowsUseRecordedAt(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	asOf := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	ok := true
	seedSnapshotUsers(t, client, asOf, []snapshotUser{
		{id: "cast", state: "NY", district: new(3), age: 10 * day, vote: "yea", votedAgo: 3 * day, appCheckOK: &ok},
		{id: "imported", state: "NY", district: new(3), age: 10 * day, vote: "yea", votedAgo: 3 * day,
			appCheckOK: &ok},
	})
	if _, err := client.Apply(ctx, []*spanner.Mutation{spanner.Update("user_votes",
		[]string{"user_id", "bill_id", "recorded_at"},
		[]any{"imported", testdb.FixtureHouseBill, asOf.Add(-10 * time.Minute)})}); err != nil {
		t.Fatalf("set recorded_at: %v", err)
	}
	published := asOf.Add(-day)
	if err := store.UpsertVoteAggregates(ctx, []model.VoteAggregate{{
		BillID: testdb.FixtureHouseBill, Scope: model.AggregateScopeNational, Status: model.AggregateStatusPublished,
		YeaPct: new(100), NayPct: new(0), VotersFloor: new(100), PublishedAt: &published, ComputedAt: published,
	}}); err != nil {
		t.Fatalf("upsert cell: %v", err)
	}

	snap, err := store.AggregateSnapshot(ctx, snapshotParams(asOf, true))
	if err != nil {
		t.Fatalf("AggregateSnapshot: %v", err)
	}
	want := []string{"hr-119-1|NY|3|2/0|1/1|new 1,2,2|young 0,0,0"}
	if got := snapshotStrings(snap); !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}
