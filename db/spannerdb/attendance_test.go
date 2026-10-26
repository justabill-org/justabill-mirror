package spannerdb_test

import (
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// Members of the attendance tests: two representatives and a senator.
const (
	attRepA    = "A100001"
	attRepB    = "B100002"
	attSenator = "C100003"
)

// Roll calls of the attendance tests, sorted by ID as ListRollCallRefs returns them.
const (
	attHouse1   = "house-119-s1-roll001"
	attHouse2   = "house-119-s1-roll002"
	attQuorum   = "house-119-s1-roll003"
	attNoQuest  = "house-119-s1-roll004"
	attSenate1  = "senate-119-s1-vote00001"
	attVoice    = "house-119-s1-voice-hr-119-9"
	attPrevRoll = "house-118-s2-roll500"
)

// newAttendanceDB returns an empty database with congresses 118 and 119 and the three members.
func newAttendanceDB(t *testing.T) *spanner.Client {
	t.Helper()
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedCongress(ctx, t, client, testdb.FixturePrevCongress)
	testdb.SeedCongress(ctx, t, client, testdb.FixtureCongress)
	testdb.SeedMember(ctx, t, client, attRepA, "Ana", "Abel")
	testdb.SeedMember(ctx, t, client, attRepB, "Ben", "Baird")
	testdb.SeedMember(ctx, t, client, attSenator, "Cleo", "Cruz")
	return client
}

func attRollCall(id string, congress int, chamber string, roll int, question *string) repository.CongressionalVoteRow {
	return repository.CongressionalVoteRow{
		ID: id, Congress: congress, Chamber: chamber, Session: new(1), RollNumber: &roll,
		VoteDate: time.Date(2025, time.June, 2, 17, 0, 0, 0, time.UTC), Question: question,
	}
}

// seedAttendanceVotes stores roll calls in both congresses and both chambers, a quorum call, one
// with no question, and a voice vote with member rows, which has no roll number.
func seedAttendanceVotes(t *testing.T, client *spanner.Client) *spannerdb.PipelineStoreImpl {
	t.Helper()
	ctx := t.Context()
	store := spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: client})
	type stored struct {
		vote    repository.CongressionalVoteRow
		members []repository.MemberVoteRow
	}
	for _, s := range []stored{
		{attRollCall(attHouse1, testdb.FixtureCongress, "House", 1, new("On Passage")),
			[]repository.MemberVoteRow{{MemberID: attRepA, Vote: "Yea"}, {MemberID: attRepB, Vote: "Not Voting"}}},
		{attRollCall(attHouse2, testdb.FixtureCongress, "House", 2, new("On Motion to Recommit")),
			[]repository.MemberVoteRow{{MemberID: attRepA, Vote: "Not Voting"}, {MemberID: attRepB, Vote: "Not Voting"}}},
		{attRollCall(attQuorum, testdb.FixtureCongress, "House", 3, new("Call by States")),
			[]repository.MemberVoteRow{{MemberID: attRepA, Vote: "Present"}}},
		{attRollCall(attNoQuest, testdb.FixtureCongress, "House", 4, nil),
			[]repository.MemberVoteRow{{MemberID: attRepB, Vote: "Nay"}}},
		{attRollCall(attSenate1, testdb.FixtureCongress, "Senate", 1, new("On the Nomination")),
			[]repository.MemberVoteRow{{MemberID: attSenator, Vote: "Yea"}}},
		{attRollCall(attPrevRoll, testdb.FixturePrevCongress, "House", 500, new("On Passage")),
			[]repository.MemberVoteRow{{MemberID: attRepA, Vote: "Nay"}}},
	} {
		if err := store.StoreRollCall(ctx, s.vote, s.members); err != nil {
			t.Fatal(err)
		}
	}
	// SeedCongressionalVote writes no roll_number, as a voice vote has none.
	testdb.SeedCongressionalVote(ctx, t, client, attVoice, nil, testdb.FixtureCongress, "House",
		time.Date(2025, time.June, 3, 17, 0, 0, 0, time.UTC))
	testdb.SeedMemberVote(ctx, t, client, attVoice, attRepA, "Not Voting")
	return store
}

func TestListRollCallRefs(t *testing.T) {
	client := newAttendanceDB(t)
	store := seedAttendanceVotes(t, client)

	got, err := store.ListRollCallRefs(t.Context(), testdb.FixtureCongress)
	if err != nil {
		t.Fatal(err)
	}
	// 119's roll calls only, by vote ID: not the voice vote or 118's roll call.
	want := []repository.RollCallRef{
		{VoteID: attHouse1, Chamber: "House", Question: "On Passage"},
		{VoteID: attHouse2, Chamber: "House", Question: "On Motion to Recommit"},
		{VoteID: attQuorum, Chamber: "House", Question: "Call by States"},
		{VoteID: attNoQuest, Chamber: "House", Question: ""},
		{VoteID: attSenate1, Chamber: "Senate", Question: "On the Nomination"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ListRollCallRefs(119) =\n%v\nwant\n%v", got, want)
	}

	if none, noneErr := store.ListRollCallRefs(t.Context(), 117); noneErr != nil || len(none) != 0 {
		t.Errorf("ListRollCallRefs(117) = %v, %v; want none", none, noneErr)
	}
}

func TestCountMemberVotes(t *testing.T) {
	client := newAttendanceDB(t)
	store := seedAttendanceVotes(t, client)

	tests := []struct {
		name     string
		excluded []string
		want     []repository.MemberVoteCount
	}{
		{
			// The quorum call and the roll call with no question are left out by ID; the voice
			// vote (no roll number) and 118's roll call never count.
			name:     "excluded",
			excluded: []string{attQuorum, attNoQuest},
			want: []repository.MemberVoteCount{
				{MemberID: attRepA, Chamber: "House", Vote: "Not Voting", Count: 1},
				{MemberID: attRepA, Chamber: "House", Vote: "Yea", Count: 1},
				{MemberID: attRepB, Chamber: "House", Vote: "Not Voting", Count: 2},
				{MemberID: attSenator, Chamber: "Senate", Vote: "Yea", Count: 1},
			},
		},
		{
			// A nil list excludes nothing.
			name: "nil",
			want: []repository.MemberVoteCount{
				{MemberID: attRepA, Chamber: "House", Vote: "Not Voting", Count: 1},
				{MemberID: attRepA, Chamber: "House", Vote: "Present", Count: 1},
				{MemberID: attRepA, Chamber: "House", Vote: "Yea", Count: 1},
				{MemberID: attRepB, Chamber: "House", Vote: "Nay", Count: 1},
				{MemberID: attRepB, Chamber: "House", Vote: "Not Voting", Count: 2},
				{MemberID: attSenator, Chamber: "Senate", Vote: "Yea", Count: 1},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.CountMemberVotes(t.Context(), testdb.FixtureCongress, tt.excluded)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("CountMemberVotes(119) =\n%v\nwant\n%v", got, tt.want)
			}
		})
	}

	prev, err := store.CountMemberVotes(t.Context(), testdb.FixturePrevCongress, []string{})
	want := []repository.MemberVoteCount{{MemberID: attRepA, Chamber: "House", Vote: "Nay", Count: 1}}
	if err != nil || !slices.Equal(prev, want) {
		t.Errorf("CountMemberVotes(118) = %v, %v; want %v", prev, err, want)
	}
}

// attendance drops ComputedAt, which a test checks on its own, so rows compare as values.
func attendance(rows []model.MemberAttendance) []model.MemberAttendance {
	out := make([]model.MemberAttendance, len(rows))
	for i, r := range rows {
		r.ComputedAt = time.Time{}
		out[i] = r
	}
	return out
}

func TestMemberGetAttendance(t *testing.T) {
	client := newAttendanceDB(t)
	ctx := t.Context()
	for _, row := range []repository.MemberAttendanceRow{
		{MemberID: attRepA, Congress: testdb.FixturePrevCongress, Chamber: "House", Eligible: 600, Missed: 12,
			Present: 1, ChamberMembers: 437, ChamberMedianPct: 1.5},
		{MemberID: attRepA, Congress: testdb.FixtureCongress, Chamber: "Senate", Eligible: 40, Missed: 0,
			ChamberMembers: 100, ChamberMedianPct: 0.8},
		{MemberID: attRepA, Congress: testdb.FixtureCongress, Chamber: "House", Eligible: 300, Missed: 30,
			Present: 2, ChamberMembers: 435, ChamberMedianPct: 1.25},
		{MemberID: attSenator, Congress: testdb.FixtureCongress, Chamber: "Senate", Eligible: 41, Missed: 1,
			ChamberMembers: 100, ChamberMedianPct: 0.8},
	} {
		testdb.SeedMemberAttendance(ctx, t, client, row)
	}
	repo := spannerdb.NewMemberRepo(&spannerdb.Client{Spanner: client})

	got, err := repo.GetAttendance(ctx, attRepA)
	if err != nil {
		t.Fatal(err)
	}
	// Newest congress first, then by chamber; never another member's rows.
	want := []model.MemberAttendance{
		{MemberID: attRepA, Congress: 119, Chamber: "House", Eligible: 300, Missed: 30, Present: 2,
			ChamberMembers: 435, ChamberMedianPct: 1.25},
		{MemberID: attRepA, Congress: 119, Chamber: "Senate", Eligible: 40, ChamberMembers: 100, ChamberMedianPct: 0.8},
		{MemberID: attRepA, Congress: 118, Chamber: "House", Eligible: 600, Missed: 12, Present: 1,
			ChamberMembers: 437, ChamberMedianPct: 1.5},
	}
	if !slices.Equal(attendance(got), want) {
		t.Errorf("GetAttendance(%s) =\n%+v\nwant\n%+v", attRepA, got, want)
	}
	for _, r := range got {
		if r.ComputedAt.IsZero() {
			t.Errorf("row %d %s has no computed_at", r.Congress, r.Chamber)
		}
	}

	// A stored member with no rows, and an unknown one, get an empty slice: never nil, which the
	// API would send as null.
	for _, id := range []string{attRepB, "Z999999"} {
		rows, getErr := repo.GetAttendance(ctx, id)
		if getErr != nil || rows == nil || len(rows) != 0 {
			t.Errorf("GetAttendance(%s) = %#v, %v; want an empty slice", id, rows, getErr)
		}
	}
}

func TestReplaceMemberAttendance(t *testing.T) {
	client := newAttendanceDB(t)
	ctx := t.Context()
	prevA := repository.MemberAttendanceRow{
		MemberID: attRepA, Congress: testdb.FixturePrevCongress, Chamber: "House", Eligible: 600, Missed: 12,
		Present: 1, ChamberMembers: 437, ChamberMedianPct: 1.5,
	}
	testdb.SeedMemberAttendance(ctx, t, client, prevA)
	testdb.SeedMemberAttendance(ctx, t, client, repository.MemberAttendanceRow{
		MemberID: attRepA, Congress: testdb.FixtureCongress, Chamber: "House", Eligible: 10, Missed: 5,
		ChamberMembers: 2, ChamberMedianPct: 25,
	})
	testdb.SeedMemberAttendance(ctx, t, client, repository.MemberAttendanceRow{
		MemberID: attRepB, Congress: testdb.FixtureCongress, Chamber: "House", Eligible: 10,
		ChamberMembers: 2, ChamberMedianPct: 25,
	})
	repo := spannerdb.NewMemberRepo(&spannerdb.Client{Spanner: client})
	store := spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: client})
	before := readAttendance(t, repo, attRepA)

	// A's 119 row is rewritten with new counts, B's is gone, the senator's is new.
	err := store.ReplaceMemberAttendance(ctx, testdb.FixtureCongress, []repository.MemberAttendanceRow{
		{MemberID: attRepA, Congress: testdb.FixtureCongress, Chamber: "House", Eligible: 12, Missed: 6,
			Present: 1, ChamberMembers: 1, ChamberMedianPct: 50},
		{MemberID: attSenator, Congress: testdb.FixtureCongress, Chamber: "Senate", Eligible: 3, Missed: 0,
			ChamberMembers: 1, ChamberMedianPct: 0},
	})
	if err != nil {
		t.Fatal(err)
	}

	gotA := readAttendance(t, repo, attRepA)
	wantA := []model.MemberAttendance{
		{MemberID: attRepA, Congress: 119, Chamber: "House", Eligible: 12, Missed: 6, Present: 1,
			ChamberMembers: 1, ChamberMedianPct: 50},
		{MemberID: attRepA, Congress: 118, Chamber: "House", Eligible: 600, Missed: 12, Present: 1,
			ChamberMembers: 437, ChamberMedianPct: 1.5},
	}
	if !slices.Equal(attendance(gotA), wantA) {
		t.Errorf("A's rows after the replace =\n%+v\nwant\n%+v", gotA, wantA)
	}
	if b := readAttendance(t, repo, attRepB); len(b) != 0 {
		t.Errorf("B's rows after the replace = %+v, want none", b)
	}
	gotC := readAttendance(t, repo, attSenator)
	wantC := []model.MemberAttendance{
		{MemberID: attSenator, Congress: 119, Chamber: "Senate", Eligible: 3, ChamberMembers: 1},
	}
	if !slices.Equal(attendance(gotC), wantC) {
		t.Errorf("the senator's rows after the replace =\n%+v\nwant\n%+v", gotC, wantC)
	}

	// The new rows share one commit's timestamp, later than the seeded ones; 118's row keeps its own.
	if len(gotA) != 2 || len(gotC) != 1 || len(before) != 2 {
		t.Fatalf("row counts: A %d, senator %d, A before %d; want 2, 1, 2", len(gotA), len(gotC), len(before))
	}
	if !gotA[0].ComputedAt.Equal(gotC[0].ComputedAt) || !gotA[0].ComputedAt.After(before[0].ComputedAt) {
		t.Errorf("computed_at: A %v, senator %v, A before %v; want A = senator, after before",
			gotA[0].ComputedAt, gotC[0].ComputedAt, before[0].ComputedAt)
	}
	if !gotA[1].ComputedAt.Equal(before[1].ComputedAt) {
		t.Errorf("118's computed_at = %v, want it untouched at %v", gotA[1].ComputedAt, before[1].ComputedAt)
	}
}

// An empty run clears the congress; a row of another congress fails the replace and writes nothing.
func TestReplaceMemberAttendance_EmptyAndWrongCongress(t *testing.T) {
	client := newAttendanceDB(t)
	ctx := t.Context()
	testdb.SeedMemberAttendance(ctx, t, client, repository.MemberAttendanceRow{
		MemberID: attRepA, Congress: testdb.FixturePrevCongress, Chamber: "House", Eligible: 1,
		ChamberMembers: 1,
	})
	testdb.SeedMemberAttendance(ctx, t, client, repository.MemberAttendanceRow{
		MemberID: attRepA, Congress: testdb.FixtureCongress, Chamber: "House", Eligible: 2,
		ChamberMembers: 1,
	})
	repo := spannerdb.NewMemberRepo(&spannerdb.Client{Spanner: client})
	store := spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: client})

	err := store.ReplaceMemberAttendance(ctx, testdb.FixtureCongress, []repository.MemberAttendanceRow{
		{MemberID: attRepB, Congress: testdb.FixtureCongress, Chamber: "House", Eligible: 3, ChamberMembers: 2},
		{MemberID: attRepA, Congress: testdb.FixturePrevCongress, Chamber: "House", Eligible: 9, ChamberMembers: 2},
	})
	if err == nil {
		t.Error("ReplaceMemberAttendance(119) with a 118 row succeeded, want an error")
	}
	if got := congresses(readAttendance(t, repo, attRepA)); !slices.Equal(got, []int{119, 118}) {
		t.Errorf("A's congresses after the failed replace = %v, want [119 118]", got)
	}
	if b := readAttendance(t, repo, attRepB); len(b) != 0 {
		t.Errorf("B's rows after the failed replace = %+v, want none", b)
	}

	if err = store.ReplaceMemberAttendance(ctx, testdb.FixtureCongress, nil); err != nil {
		t.Fatal(err)
	}
	if got := congresses(readAttendance(t, repo, attRepA)); !slices.Equal(got, []int{118}) {
		t.Errorf("A's congresses after an empty replace = %v, want [118]", got)
	}
}

func readAttendance(t *testing.T, repo *spannerdb.MemberRepository, id string) []model.MemberAttendance {
	t.Helper()
	rows, err := repo.GetAttendance(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func congresses(rows []model.MemberAttendance) []int {
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Congress)
	}
	return out
}
