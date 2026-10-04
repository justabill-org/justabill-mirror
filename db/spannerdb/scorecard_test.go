package spannerdb_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/scoring"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// Scorecard fixture on top of testdb.SeedFixture: a California voter (Ada's district, and two
// California senators), a Texas voter (Ben's district) and a voter with no address. Sam Shaw
// was a House member (CA-30) in the 118th and is a senator in the 119th, like Adam Schiff; Rhea
// Reyes held CA-12 in the 118th and has retired.
const (
	userCA       = "user-ca"
	userTX       = "user-tx"
	userNoAddr   = "user-none"
	senatorCA    = "D000004"
	switcherCA   = "S000150"
	retiredCA    = "R000118"
	billPrev     = "hr-118-20" // Sam's House vote in the 118th
	billFloor    = "hr-119-10" // amendment, recommit and passage roll calls
	billVoice    = "hr-119-11" // voice vote only
	billSenate   = "s-119-5"   // the California senator didn't vote on passage
	senateAbsent = "senate-119-s1-vote00005"
)

// seedScorecard adds the scorecard fixture's members, bills, roll calls and user votes.
func seedScorecard(t *testing.T, client *spanner.Client) {
	t.Helper()
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)
	testdb.SeedMember(ctx, t, client, senatorCA, "Dana", "Diaz")
	testdb.SeedMemberTerm(ctx, t, client, senatorCA, testdb.FixtureCongress, "Senate", "CA", nil, "D")
	testdb.SeedMember(ctx, t, client, switcherCA, "Sam", "Shaw")
	testdb.SeedMemberTerm(ctx, t, client, switcherCA, testdb.FixturePrevCongress, "House", "CA", new(30), "D")
	testdb.SeedMemberTerm(ctx, t, client, switcherCA, testdb.FixtureCongress, "Senate", "CA", nil, "D")
	testdb.SeedMember(ctx, t, client, retiredCA, "Rhea", "Reyes")
	testdb.SeedMemberTerm(ctx, t, client, retiredCA, testdb.FixturePrevCongress, "House", "CA", new(12), "R")
	testdb.SeedBill(ctx, t, client, billPrev, testdb.FixturePrevCongress, "hr", 20, "Earlier Act")
	testdb.SeedBill(ctx, t, client, billFloor, testdb.FixtureCongress, "hr", 10, "Floor Act")
	testdb.SeedBill(ctx, t, client, billVoice, testdb.FixtureCongress, "hr", 11, "Voice Act")
	testdb.SeedBill(ctx, t, client, billSenate, testdb.FixtureCongress, "s", 5, "Senate Act")

	house, senate := "House", "Senate"
	floorDay := day(time.May, 6)
	rolls := []rollCallRow{
		{"house-119-s1-roll010", nullStr(billFloor), 119, house, roll(10), floorDay,
			nullStr("On Agreeing to the Amendment")},
		{"house-119-s1-roll011", nullStr(billFloor), 119, house, roll(11), floorDay,
			nullStr("On Motion to Recommit")},
		{"house-119-s1-roll012", nullStr(billFloor), 119, house, roll(12), floorDay, nullStr("On Passage")},
		// A voice vote with the synthetic Yeas the pipeline wrote before #188.
		{"house-119-voice-20250507", nullStr(billVoice), 119, house, spanner.NullInt64{}, day(time.May, 7),
			nullStr("On Passage")},
		{senateAbsent, nullStr(billSenate), 119, senate, roll(5), day(time.May, 8),
			nullStr("On Passage of the Bill S. 5")},
		// Ada voted in the 118th, when Rhea held CA-12 (Ada has no term then).
		{"house-118-s1-roll001", nullStr(testdb.FixturePrevHouseBill), 118, house, roll(1),
			time.Date(2023, time.March, 1, 17, 0, 0, 0, time.UTC), nullStr("On Passage")},
		{"house-118-s1-roll020", nullStr(billPrev), 118, house, roll(20),
			time.Date(2023, time.April, 4, 17, 0, 0, 0, time.UTC), nullStr("On Passage")},
	}
	votes := []memberVoteRow{
		{"house-119-s1-roll010", testdb.FixtureHouseDem, "Aye"},
		{"house-119-s1-roll011", testdb.FixtureHouseDem, "No"},
		{"house-119-s1-roll012", testdb.FixtureHouseDem, "Aye"},
		{"house-119-s1-roll012", testdb.FixtureHouseRep, "Aye"},
		{"house-119-voice-20250507", testdb.FixtureHouseDem, "Yea"},
		{senateAbsent, senatorCA, "Not Voting"},
		{"house-118-s1-roll001", testdb.FixtureHouseDem, "Yea"},
		{"house-118-s1-roll001", retiredCA, "Nay"},
		{"house-118-s1-roll020", switcherCA, "Yea"},
		{"house-118-s1-roll020", retiredCA, "Nay"},
	}
	var muts []*spanner.Mutation
	for _, row := range rolls {
		muts = append(muts, mustInsertStruct(t, "congressional_votes", row))
	}
	for _, row := range votes {
		muts = append(muts, mustInsertStruct(t, "member_votes", row))
	}
	if _, err := client.Apply(ctx, muts); err != nil {
		t.Fatalf("seed roll calls: %v", err)
	}

	testdb.SeedUserWithDistrict(ctx, t, client, userCA, "CA", new(12))
	testdb.SeedUserWithDistrict(ctx, t, client, userTX, "TX", new(7))
	testdb.SeedUser(ctx, t, client, userNoAddr)
	for _, v := range []struct{ user, bill, vote string }{
		{userCA, billFloor, "yea"},
		{userCA, billVoice, "yea"},
		{userCA, billSenate, "nay"},
		{userCA, testdb.FixtureHouseBill, "nay"}, // Ada voted Yea
		{userCA, testdb.FixturePrevHouseBill, "yea"},
		{userCA, testdb.FixtureSenateBill, "skip"},
		{userCA, billPrev, "yea"},
		{userTX, testdb.FixtureHouseBill, "yea"}, // Ben voted Nay
		{userNoAddr, testdb.FixtureHouseBill, "yea"},
	} {
		testdb.SeedUserVote(ctx, t, client, v.user, v.bill, v.vote)
	}
}

func TestGetScorecard(t *testing.T) {
	client := testdb.New(t)
	seedScorecard(t, client)
	sc := spannerdb.NewScorecard(&spannerdb.Client{Spanner: client})

	ada := func(matching, compared int, pct float64) model.RepScore {
		return model.RepScore{MemberID: testdb.FixtureHouseDem, MemberName: "Ada Alvarez", Chamber: "House",
			Party: "D", MatchingVotes: matching, TotalCompared: compared, AlignmentPct: new(pct),
			Rule: scoring.RuleName}
	}
	dana := model.RepScore{MemberID: senatorCA, MemberName: "Dana Diaz", Chamber: "Senate", Party: "D",
		MemberAbsent: 1, Rule: scoring.RuleName}
	// Sam is scored on his 118th House vote, under his current chamber.
	sam := model.RepScore{MemberID: switcherCA, MemberName: "Sam Shaw", Chamber: "Senate", Party: "D",
		MatchingVotes: 1, TotalCompared: 1, AlignmentPct: new(100.0), Rule: scoring.RuleName}

	tests := []struct {
		name       string
		user       string
		congresses []int
		want       []model.RepScore
	}{
		{
			// Ada: one comparison on the floor bill (not three), a mismatch on HR 1, a match in the
			// 118th (she holds the seat now), nothing on the voice vote. Dana didn't vote, so her
			// percentage is null. Rhea held CA-12 in the 118th but doesn't now, so she isn't here.
			name: "every congress: current seat-holders, final votes only",
			user: userCA,
			want: []model.RepScore{ada(2, 3, 66.67), dana, sam},
		},
		{
			name: "both congresses named is every congress", user: userCA, congresses: []int{118, 119},
			want: []model.RepScore{ada(2, 3, 66.67), dana, sam},
		},
		{
			name: "119th only: the switcher has no Senate votes yet", user: userCA, congresses: []int{119},
			want: []model.RepScore{ada(1, 2, 50), dana},
		},
		{
			name: "118th only: the switcher's House votes count", user: userCA, congresses: []int{118},
			want: []model.RepScore{ada(1, 1, 100), sam},
		},
		{name: "a congress with no votes", user: userCA, congresses: []int{117}, want: []model.RepScore{}},
		{
			// Ben also voted on the floor bill, but only the California voter voted on it.
			name: "another user's votes don't leak",
			user: userTX,
			want: []model.RepScore{
				{MemberID: testdb.FixtureHouseRep, MemberName: "Ben Brooks", Chamber: "House", Party: "R",
					TotalCompared: 1, AlignmentPct: new(0.0), Rule: scoring.RuleName},
			},
		},
		{name: "no address", user: userNoAddr, want: []model.RepScore{}},
		{name: "unknown user", user: "nobody", want: []model.RepScore{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sc.GetScorecard(t.Context(), tt.user, tt.congresses)
			if err != nil {
				t.Fatalf("GetScorecard: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("scores = %s\nwant %s", fmtScores(got), fmtScores(tt.want))
			}
		})
	}
}

// fmtScores prints scores with their percentages dereferenced.
func fmtScores(scores []model.RepScore) string {
	out := make([]string, 0, len(scores))
	for _, s := range scores {
		pct := "null"
		if s.AlignmentPct != nil {
			pct = fmt.Sprint(*s.AlignmentPct)
		}
		out = append(out, fmt.Sprintf("{%s %s %s/%s matching=%d compared=%d absent=%d pct=%s %s}",
			s.MemberID, s.MemberName, s.Chamber, s.Party, s.MatchingVotes, s.TotalCompared, s.MemberAbsent,
			pct, s.Rule))
	}
	return "[" + strings.Join(out, " ") + "]"
}

func TestCompareWithMember(t *testing.T) {
	client := testdb.New(t)
	seedScorecard(t, client)
	sc := spannerdb.NewScorecard(&spannerdb.Client{Spanner: client})

	type row struct {
		bill, voteID, user, member string
		counted, matches           bool
	}
	adaFloor := row{billFloor, "house-119-s1-roll012", scoring.Yea, scoring.Yea, true, true}
	adaHR1 := row{testdb.FixtureHouseBill, testdb.FixtureHouseVote, scoring.Nay, scoring.Yea, true, false}
	adaPrev := row{testdb.FixturePrevHouseBill, "house-118-s1-roll001", scoring.Yea, scoring.Yea, true, true}
	samPrev := row{billPrev, "house-118-s1-roll020", scoring.Yea, scoring.Yea, true, true}
	tests := []struct {
		name         string
		user, member string
		congresses   []int
		want         []row
	}{
		{
			name: "own representative: final votes in every congress",
			user: userCA, member: testdb.FixtureHouseDem,
			want: []row{adaFloor, adaHR1, adaPrev},
		},
		{
			name: "filtered to the 119th", user: userCA, member: testdb.FixtureHouseDem, congresses: []int{119},
			want: []row{adaFloor, adaHR1},
		},
		{
			name: "senator's House votes in the 118th", user: userCA, member: switcherCA, congresses: []int{118},
			want: []row{samPrev},
		},
		{
			name: "senator who didn't vote on passage", user: userCA, member: senatorCA,
			want: []row{{billSenate, senateAbsent, scoring.Nay, scoring.NotVoting, false, false}},
		},
		{
			name: "a member who never represented the user: every congress", user: userCA,
			member: testdb.FixtureHouseRep,
			want: []row{
				{billFloor, "house-119-s1-roll012", scoring.Yea, scoring.Yea, true, true},
				{testdb.FixtureHouseBill, testdb.FixtureHouseVote, scoring.Nay, scoring.Nay, true, true},
			},
		},
		{name: "other user's votes don't leak", user: userTX, member: testdb.FixtureHouseDem, want: []row{
			{testdb.FixtureHouseBill, testdb.FixtureHouseVote, scoring.Yea, scoring.Yea, true, true},
		}},
		{name: "unknown member", user: userCA, member: "Z999999", want: []row{}},
		{name: "user without votes", user: "nobody", member: testdb.FixtureHouseDem, want: []row{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sc.CompareWithMember(t.Context(), tt.user, tt.member, tt.congresses)
			if err != nil {
				t.Fatalf("CompareWithMember: %v", err)
			}
			rows := []row{}
			for _, c := range got {
				rows = append(rows, row{c.BillID, c.VoteID, c.UserVote, c.MemberVote, c.Counted, c.Matches})
				checkComparisonLabels(t, c)
			}
			if !reflect.DeepEqual(rows, tt.want) {
				t.Errorf("comparisons = %+v\nwant %+v", rows, tt.want)
			}
		})
	}
}

// checkComparisonLabels checks that a comparison carries its bill title, question, date and
// the roll call's chamber and congress: Sam's 118th vote says House although he's a senator now.
func checkComparisonLabels(t *testing.T, c model.VoteComparison) {
	t.Helper()
	if c.BillTitle == "" || c.Question == nil || c.VoteDate.IsZero() || c.Chamber == "" || c.Congress == 0 {
		t.Errorf("comparison %+v lacks its title, question, date, chamber or congress", c)
	}
	if c.VoteID == "house-118-s1-roll020" && (c.Chamber != "House" || c.Congress != testdb.FixturePrevCongress) {
		t.Errorf("senator's 118th vote labelled %s %d, want House 118", c.Chamber, c.Congress)
	}
}

// TestScorecardAgreesWithCompare is the D4 regression test: for every representative on a
// user's scorecard, the one-member comparison has the same counts.
func TestScorecardAgreesWithCompare(t *testing.T) {
	client := testdb.New(t)
	seedScorecard(t, client)
	sc := spannerdb.NewScorecard(&spannerdb.Client{Spanner: client})

	for _, filter := range [][]int{nil, {118}, {119}} {
		for _, user := range []string{userCA, userTX} {
			agreeWithCompare(t, sc, user, filter)
		}
	}
}

// agreeWithCompare checks that each of the user's scorecard rows has the counts of the
// one-member comparison under the same congress filter.
func agreeWithCompare(t *testing.T, sc *spannerdb.ScorecardService, user string, congresses []int) {
	t.Helper()
	scores, err := sc.GetScorecard(t.Context(), user, congresses)
	if err != nil {
		t.Fatalf("GetScorecard(%s, %v): %v", user, congresses, err)
	}
	for _, s := range scores {
		rows, cmpErr := sc.CompareWithMember(t.Context(), user, s.MemberID, congresses)
		if cmpErr != nil {
			t.Fatalf("CompareWithMember(%s, %s, %v): %v", user, s.MemberID, congresses, cmpErr)
		}
		var compared, matching, absent int
		for _, r := range rows {
			switch {
			case r.Matches:
				compared++
				matching++
			case r.Counted:
				compared++
			default:
				absent++
			}
		}
		if compared != s.TotalCompared || matching != s.MatchingVotes || absent != s.MemberAbsent {
			t.Errorf(
				"%s vs %s in %v: compare has %d/%d/%d (compared/matching/absent), scorecard %d/%d/%d",
				user,
				s.MemberID,
				congresses,
				compared,
				matching,
				absent,
				s.TotalCompared,
				s.MatchingVotes,
				s.MemberAbsent,
			)
		}
	}
}

// TestMemberPositions_Switcher: the public positions endpoint's query returns a current
// senator's 118th positions, cast in the House (Schiff's case).
func TestMemberPositions_Switcher(t *testing.T) {
	client := testdb.New(t)
	seedScorecard(t, client)
	repo := spannerdb.NewVoteRepo(&spannerdb.Client{Spanner: client})

	got, err := repo.MemberPositions(t.Context(), switcherCA, testdb.FixturePrevCongress)
	if err != nil {
		t.Fatalf("MemberPositions: %v", err)
	}
	if len(got) != 1 || got[0].BillID != billPrev || got[0].Chamber != "House" || got[0].Vote != scoring.Yea {
		t.Errorf("positions = %+v, want one House yea on %s", got, billPrev)
	}
	got, err = repo.MemberPositions(t.Context(), switcherCA, testdb.FixtureCongress)
	if err != nil || len(got) != 0 {
		t.Errorf("119th positions = %+v, %v; want none", got, err)
	}
}

// TestScorecardRepsAreFindMyReps: the scorecard's representatives are the members find-my-reps
// returns for the user's address (GetByDistrict and GetSenators), so a retired 118th
// seat-holder is on neither, and the congresses a filter may name are the congresses table's.
func TestScorecardRepsAreFindMyReps(t *testing.T) {
	client := testdb.New(t)
	seedScorecard(t, client)
	c := &spannerdb.Client{Spanner: client}
	members := spannerdb.NewMemberRepo(c)

	house, err := members.GetByDistrict(t.Context(), "CA", 12)
	if err != nil {
		t.Fatalf("GetByDistrict: %v", err)
	}
	senators, err := members.GetSenators(t.Context(), "CA")
	if err != nil {
		t.Fatalf("GetSenators: %v", err)
	}
	var found []string
	for _, m := range slices.Concat(house, senators) {
		found = append(found, m.BioguideID)
	}
	scores, err := spannerdb.NewScorecard(c).GetScorecard(t.Context(), userCA, nil)
	if err != nil {
		t.Fatalf("GetScorecard: %v", err)
	}
	var scored []string
	for _, s := range scores {
		scored = append(scored, s.MemberID)
	}
	if want := []string{testdb.FixtureHouseDem, senatorCA, switcherCA}; !slices.Equal(found, want) ||
		!slices.Equal(scored, want) {
		t.Errorf("find-my-reps = %v, scorecard = %v, want both %v", found, scored, want)
	}

	congresses, err := spannerdb.NewCongressRepo(c).List(t.Context())
	if err != nil {
		t.Fatalf("congresses: %v", err)
	}
	var numbers []int
	for _, cg := range congresses {
		numbers = append(numbers, cg.Number)
	}
	if want := []int{testdb.FixtureCongress, testdb.FixturePrevCongress}; !slices.Equal(numbers, want) {
		t.Errorf("congresses = %v, want %v", numbers, want)
	}
}

// TestScorecardSkipsEndedTerms: a member whose current-congress term has an end_date has left
// the seat, so the scorecard leaves them out as find-my-reps does (#527): here a CA senator who
// resigned and the CA-12 member who held the seat before Ada, both with votes the user shares.
// The members still holding the seats are scored exactly as before.
func TestScorecardSkipsEndedTerms(t *testing.T) {
	client := testdb.New(t)
	seedScorecard(t, client)
	ctx := t.Context()
	c := &spannerdb.Client{Spanner: client}
	sc := spannerdb.NewScorecard(c)

	before, err := sc.GetScorecard(ctx, userCA, nil)
	if err != nil {
		t.Fatalf("GetScorecard before: %v", err)
	}

	const resigned, predecessor = "L000007", "P000008"
	testdb.SeedMember(ctx, t, client, resigned, "Lena", "Left")
	testdb.SeedMember(ctx, t, client, predecessor, "Paul", "Prior")
	left := spanner.NullDate{Date: civil.Date{Year: 2025, Month: time.March, Day: 1}, Valid: true}
	cols := []string{"member_id", "congress", "chamber", "state", "district", "party", "end_date"}
	muts := []*spanner.Mutation{
		spanner.Insert("member_terms", cols,
			[]any{resigned, int64(testdb.FixtureCongress), "Senate", "CA", spanner.NullInt64{}, "R", left}),
		spanner.Insert("member_terms", cols,
			[]any{predecessor, int64(testdb.FixtureCongress), "House", "CA", int64(12), "R", left}),
		mustInsertStruct(t, "member_votes", memberVoteRow{senateAbsent, resigned, "Nay"}),
		mustInsertStruct(t, "member_votes", memberVoteRow{"house-119-s1-roll012", predecessor, "Aye"}),
	}
	if _, err = client.Apply(ctx, muts); err != nil {
		t.Fatalf("seed ended terms: %v", err)
	}

	after, err := sc.GetScorecard(ctx, userCA, nil)
	if err != nil {
		t.Fatalf("GetScorecard after: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("scorecard with ended terms = %+v, want %+v", after, before)
	}

	members := spannerdb.NewMemberRepo(c)
	house, err := members.GetByDistrict(ctx, "CA", 12)
	if err != nil {
		t.Fatalf("GetByDistrict: %v", err)
	}
	senators, err := members.GetSenators(ctx, "CA")
	if err != nil {
		t.Fatalf("GetSenators: %v", err)
	}
	found := memberIDs(slices.Concat(house, senators))
	var scored []string
	for _, s := range after {
		scored = append(scored, s.MemberID)
	}
	if !slices.Equal(scored, found) {
		t.Errorf("scorecard = %v, find-my-reps = %v; want the same members", scored, found)
	}
}
