package spannerdb_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const (
	identicalBill = "Identical bill"
	relatedBill   = "Related bill"
)

// newGraphRepo seeds the fixture plus link rows through the pipeline's own writers:
//   - HR 1 (119th): sponsor A (D), cosponsors B (R), C (R) and an unsynced member.
//   - S 1 (119th): sponsor C, cosponsor A. HR 1 and S 1 are identical bills listed on both
//     sides and share two subjects.
//   - HR 1 (118th): sponsor B, cosponsor A, one subject shared with the 119th bills, and a
//     "Related bill" edge from the 119th HR 1. HR 1 also relates to a bill that isn't synced.
func newGraphRepo(t *testing.T) *spannerdb.GraphRepository {
	t.Helper()
	repo, _, _ := seedGraph(t)
	return repo
}

// seedGraph seeds newGraphRepo's rows and also returns the store and client, for tests that
// add rows of their own.
func seedGraph(t *testing.T) (*spannerdb.GraphRepository, *spannerdb.PipelineStoreImpl, *spanner.Client) {
	t.Helper()
	store, client := newLinkStore(t)
	ctx := t.Context()
	crs := "CRS"
	subjects := []string{"Taxation", "Income tax credits"}

	sponsorships := []struct {
		bill, role string
		members    []string
	}{
		{testdb.FixtureHouseBill, repository.SponsorRoleSponsor, []string{testdb.FixtureHouseDem}},
		{testdb.FixtureHouseBill, repository.SponsorRoleCosponsor,
			[]string{testdb.FixtureHouseRep, testdb.FixtureSenatorRep, unsyncedMember}},
		{testdb.FixtureSenateBill, repository.SponsorRoleSponsor, []string{testdb.FixtureSenatorRep}},
		{testdb.FixtureSenateBill, repository.SponsorRoleCosponsor, []string{testdb.FixtureHouseDem}},
		{testdb.FixturePrevHouseBill, repository.SponsorRoleSponsor, []string{testdb.FixtureHouseRep}},
		{testdb.FixturePrevHouseBill, repository.SponsorRoleCosponsor, []string{testdb.FixtureHouseDem}},
	}
	for _, s := range sponsorships {
		rows := make([]repository.BillSponsorshipRow, 0, len(s.members))
		for _, m := range s.members {
			rows = append(rows, repository.BillSponsorshipRow{MemberID: m})
		}
		if err := store.ReplaceBillSponsorships(ctx, s.bill, s.role, rows); err != nil {
			t.Fatalf("seed sponsorships %s: %v", s.bill, err)
		}
	}

	subjectsByBill := map[string][]string{
		testdb.FixtureHouseBill:     subjects,
		testdb.FixtureSenateBill:    subjects,
		testdb.FixturePrevHouseBill: subjects[:1],
	}
	for bill, names := range subjectsByBill {
		if err := store.ReplaceBillSubjects(ctx, bill, names); err != nil {
			t.Fatalf("seed subjects %s: %v", bill, err)
		}
	}

	relations := map[string][]repository.BillRelationRow{
		testdb.FixtureHouseBill: {
			{RelatedBillID: testdb.FixtureSenateBill, RelationType: identicalBill, IdentifiedBy: &crs},
			{RelatedBillID: testdb.FixturePrevHouseBill, RelationType: relatedBill, IdentifiedBy: &crs},
			{RelatedBillID: unsyncedBill, RelationType: relatedBill, IdentifiedBy: &crs},
		},
		testdb.FixtureSenateBill: {
			{RelatedBillID: testdb.FixtureHouseBill, RelationType: identicalBill, IdentifiedBy: &crs},
		},
	}
	for bill, rows := range relations {
		if err := store.ReplaceBillRelations(ctx, bill, rows); err != nil {
			t.Fatalf("seed relations %s: %v", bill, err)
		}
	}

	return spannerdb.NewGraphRepo(&spannerdb.Client{Spanner: client}), store, client
}

func TestGraphRelatedBills(t *testing.T) {
	repo := newGraphRepo(t)
	ctx := t.Context()

	got, err := repo.RelatedBills(ctx, testdb.FixtureHouseBill, 0)
	if err != nil {
		t.Fatalf("related bills: %v", err)
	}
	// S 1 is related and shares both subjects, so it comes first. HR 1 of the 118th shares only
	// one subject but is explicitly related. The unsynced bill is left out.
	want := []model.RelatedBill{
		{
			BillID: testdb.FixtureSenateBill, Congress: testdb.FixtureCongress, BillType: "s", Number: 1,
			Title: "Companion Act", RelationTypes: []string{identicalBill}, SharedSubjects: 2,
		},
		{
			BillID: testdb.FixturePrevHouseBill, Congress: testdb.FixturePrevCongress, BillType: "hr", Number: 1,
			Title: "Earlier Act", RelationTypes: []string{relatedBill}, SharedSubjects: 1,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("related bills = %+v, want %+v", got, want)
	}

	// Reverse direction: HR 1 (118th) stores no relations itself, but the edge from the
	// 119th HR 1 still links them. One shared subject alone wouldn't.
	got, err = repo.RelatedBills(ctx, testdb.FixturePrevHouseBill, 0)
	if err != nil {
		t.Fatalf("related bills (reverse): %v", err)
	}
	if len(got) != 1 || got[0].BillID != testdb.FixtureHouseBill {
		t.Errorf("reverse related bills = %+v, want only %s", got, testdb.FixtureHouseBill)
	}

	got, err = repo.RelatedBills(ctx, testdb.FixtureHouseBill, 1)
	if err != nil {
		t.Fatalf("related bills (limit 1): %v", err)
	}
	if len(got) != 1 || got[0].BillID != testdb.FixtureSenateBill {
		t.Errorf("limited related bills = %+v, want only %s", got, testdb.FixtureSenateBill)
	}
}

func TestGraphCollaborators(t *testing.T) {
	repo := newGraphRepo(t)
	ctx := t.Context()
	party := func(s string) *string { return &s }

	got, err := repo.Collaborators(ctx, testdb.FixtureHouseDem, testdb.FixtureCongress, 0)
	if err != nil {
		t.Fatalf("collaborators: %v", err)
	}
	// C shares HR 1 and S 1; B shares only HR 1 (the 118th bill is another congress). The
	// unsynced cosponsor isn't a Member node, so it never matches.
	want := []model.Collaborator{
		{
			BioguideID: testdb.FixtureSenatorRep, FirstName: "Cora", LastName: "Chen",
			Party: party("R"), State: party("OH"), Chamber: party("Senate"), SharedBills: 2,
		},
		{
			BioguideID: testdb.FixtureHouseRep, FirstName: "Ben", LastName: "Brooks",
			Party: party("R"), State: party("TX"), Chamber: party("House"), SharedBills: 1,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collaborators = %+v, want %+v", got, want)
	}

	// The fixture has no 118th terms, so party, state and chamber are unknown.
	got, err = repo.Collaborators(ctx, testdb.FixtureHouseDem, testdb.FixturePrevCongress, 0)
	if err != nil {
		t.Fatalf("collaborators (118th): %v", err)
	}
	want = []model.Collaborator{{
		BioguideID: testdb.FixtureHouseRep, FirstName: "Ben", LastName: "Brooks", SharedBills: 1,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("118th collaborators = %+v, want %+v", got, want)
	}

	got, err = repo.Collaborators(ctx, unsyncedMember, testdb.FixtureCongress, 0)
	if err != nil {
		t.Fatalf("collaborators (unsynced member): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unsynced member collaborators = %+v, want none", got)
	}
}

func TestGraphCompanionVotes(t *testing.T) {
	repo, store, client := seedGraph(t)
	ctx := t.Context()

	// A later voice vote on the Senate companion. The pipeline writes a synthetic Yea for every
	// member on a voice vote; those rows have no roll_number and must not show up.
	senateBill := testdb.FixtureSenateBill
	voiceVote := "senate-119-voice-s-119-1-20250501"
	testdb.SeedCongressionalVote(ctx, t, client, voiceVote, &senateBill, testdb.FixtureCongress, "Senate",
		time.Date(2025, time.May, 1, 17, 0, 0, 0, time.UTC))
	testdb.SeedMemberVote(ctx, t, client, voiceVote, testdb.FixtureSenatorRep, "Yea")

	got, err := repo.CompanionVotes(ctx, testdb.FixtureHouseBill, 0)
	if err != nil {
		t.Fatalf("companion votes: %v", err)
	}
	// The relation is stored on both bills; each vote still appears once, and the voice vote
	// is left out.
	if len(got) != 1 {
		t.Fatalf("companion votes = %+v, want one Senate roll call vote", got)
	}
	v := got[0]
	if v.CompanionBillID != testdb.FixtureSenateBill || v.VoteID != testdb.FixtureSenateVote ||
		v.MemberID != testdb.FixtureSenatorRep || v.Vote != "Yea" || v.Chamber != "Senate" ||
		v.Party == nil || *v.Party != "R" || v.LastName != "Chen" || v.VoteDate.IsZero() {
		t.Errorf("companion vote = %+v", v)
	}

	got, err = repo.CompanionVotes(ctx, testdb.FixtureSenateBill, 0)
	if err != nil {
		t.Fatalf("companion votes (Senate bill): %v", err)
	}
	var members []string
	for _, v := range got {
		members = append(members, v.MemberID+"|"+v.Vote+"|"+*v.Party)
	}
	wantMembers := []string{testdb.FixtureHouseDem + "|Yea|D", testdb.FixtureHouseRep + "|Nay|R"}
	if !reflect.DeepEqual(members, wantMembers) {
		t.Errorf("House companion votes = %q, want %q", members, wantMembers)
	}

	// HR 1 of the 118th is only a "Related bill" of the 119th HR 1 (same chamber) and of S 1
	// (other chamber, with a roll call). Only "Identical bill" makes a companion.
	crs := "CRS"
	err = store.ReplaceBillRelations(ctx, testdb.FixturePrevHouseBill, []repository.BillRelationRow{
		{RelatedBillID: testdb.FixtureSenateBill, RelationType: relatedBill, IdentifiedBy: &crs},
	})
	if err != nil {
		t.Fatalf("seed relations %s: %v", testdb.FixturePrevHouseBill, err)
	}
	got, err = repo.CompanionVotes(ctx, testdb.FixturePrevHouseBill, 0)
	if err != nil {
		t.Fatalf("companion votes (no companion): %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("companion votes without a companion = %#v, want empty slice", got)
	}
}

// TestGraphCompanionVotesBusyCompanion is #784: a companion with many full House roll calls
// must answer within the API's 2-second graph query budget (graphQueryTimeout in
// api/internal/handler), newest roll calls first. Joining and sorting every member vote on every
// roll call before the limit ran out of memory on the emulator and timed out in production.
func TestGraphCompanionVotesBusyCompanion(t *testing.T) {
	repo, _, client := seedGraph(t)
	const rollCalls, members, limit = 40, 435, 2000
	seedBusyHouseCompanion(t, client, rollCalls, members)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	got, err := repo.CompanionVotes(ctx, testdb.FixtureSenateBill, limit)
	if err != nil {
		t.Fatalf("companion votes: %v", err)
	}
	// The newest roll calls are busy-39 to busy-36 (435 votes each) and the first 260 member
	// votes of busy-35, by member ID.
	perVote := map[string]int{}
	var order []string
	for _, v := range got {
		if perVote[v.VoteID] == 0 {
			order = append(order, v.VoteID)
		}
		perVote[v.VoteID]++
	}
	wantOrder := []string{
		"house-119-busy-39", "house-119-busy-38", "house-119-busy-37", "house-119-busy-36", "house-119-busy-35",
	}
	if len(got) != limit || !reflect.DeepEqual(order, wantOrder) ||
		perVote["house-119-busy-39"] != members || perVote["house-119-busy-35"] != 260 {
		t.Fatalf("got %d votes over roll calls %v (%v), want %d over %v", len(got), order, perVote, limit, wantOrder)
	}
	first, last := got[0], got[len(got)-1]
	if first.MemberID != "Z000000" || first.Chamber != "House" || first.Party == nil || *first.Party != "I" ||
		first.CompanionBillID != testdb.FixtureHouseBill || first.Question == nil || last.MemberID != "Z000259" {
		t.Errorf("first vote = %+v, last = %+v", first, last)
	}
}

// seedBusyHouseCompanion adds members (with 119th House terms) and rollCalls recorded House roll
// calls on the fixture's HR 1, busy-0 the oldest, each with every member's vote and a tally.
func seedBusyHouseCompanion(t *testing.T, client *spanner.Client, rollCalls, members int) {
	t.Helper()
	var ms []*spanner.Mutation
	for m := range members {
		id := fmt.Sprintf("Z%06d", m)
		ms = append(ms,
			spanner.Insert("members", []string{"bioguide_id", "first_name", "last_name"}, []any{id, "First", "Last"}),
			spanner.Insert("member_terms", []string{"member_id", "congress", "chamber", "state", "party"},
				[]any{id, int64(testdb.FixtureCongress), "House", "VT", "I"}))
	}
	start := time.Date(2026, time.March, 2, 15, 0, 0, 0, time.UTC)
	for r := range rollCalls {
		voteID := fmt.Sprintf("house-119-busy-%d", r)
		ms = append(ms, spanner.Insert("congressional_votes",
			[]string{"vote_id", "bill_id", "congress", "chamber", "roll_number", "vote_date", "question", "yeas"},
			[]any{voteID, testdb.FixtureHouseBill, int64(testdb.FixtureCongress), "House", int64(100 + r),
				start.Add(time.Duration(r) * time.Hour), "On Agreeing to the Amendment", int64(members)}))
		for m := range members {
			ms = append(ms, spanner.Insert("member_votes", []string{"vote_id", "member_id", "vote"},
				[]any{voteID, fmt.Sprintf("Z%06d", m), "Yea"}))
		}
	}
	const batch = 2000
	for len(ms) > 0 {
		n := min(len(ms), batch)
		if _, err := client.Apply(t.Context(), ms[:n]); err != nil {
			t.Fatalf("seed busy companion: %v", err)
		}
		ms = ms[n:]
	}
}
