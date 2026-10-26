package sync

import (
	"bytes"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
)

// The vote sync on the Spanner store, from the House's first roll calls of the 119th: roll 1 is
// the "Call by States" quorum call, excluded, and roll 2 the Speaker election, whose candidate
// names count as votes cast. Roll 3 was stored by an earlier run. A member the Clerk lists
// with no members row gets no attendance row. A second run recomputes from what's stored then.
func TestEmulator_SyncVotesComputesAttendance(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedCongress(ctx, t, client, 119)
	store := spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: client})
	const aguilar = "A000371"
	for _, id := range []string{adams, aderholt, aguilar} {
		testdb.SeedMember(ctx, t, client, id, "First", id)
	}
	for lis, id := range map[string]string{"S428": "A000382", "S354": "B001230", "S429": "B001299"} {
		testdb.SeedMember(ctx, t, client, id, "First", id)
		if err := store.UpdateMemberLisID(ctx, id, lis); err != nil {
			t.Fatal(err)
		}
	}
	// Z999999 has no members row.
	storeRoll(t, store, 3, "On Motion to Suspend the Rules and Pass",
		adams, "Not Voting", aderholt, "Not Voting", aguilar, "Present", "Z999999", "Not Voting")

	feeds := &voteFeedServer{
		houseRolls:    map[int]int{2025: 3},
		houseDocs:     map[int]string{2025: "house-2025-roll001.xml"},
		houseRollDocs: map[[2]int]string{{2025, 2}: "house-2025-roll002.xml"},
		senateVotes:   map[int]int{1: 3},
		senateDocs:    map[int]string{1: "senate-119-1-vote00001.xml"},
	}
	s, _ := voteService(t, store, feeds)
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&logs, nil))

	if err := s.SyncVotes(ctx, 119, []int{1}); err != nil {
		t.Fatal(err)
	}
	// Rolls 2 and 3; the senators were listed on three cloture votes, all Yea.
	want := []repository.MemberAttendanceRow{
		{MemberID: aderholt, Congress: 119, Chamber: chamberHouse, Eligible: 2, Missed: 1,
			ChamberMembers: 3, ChamberMedianPct: 50},
		{MemberID: adams, Congress: 119, Chamber: chamberHouse, Eligible: 2, Missed: 1,
			ChamberMembers: 3, ChamberMedianPct: 50},
		{MemberID: aguilar, Congress: 119, Chamber: chamberHouse, Eligible: 2, Present: 1,
			ChamberMembers: 3, ChamberMedianPct: 50},
		{MemberID: "A000382", Congress: 119, Chamber: chamberSenate, Eligible: 3, ChamberMembers: 3},
		{MemberID: "B001230", Congress: 119, Chamber: chamberSenate, Eligible: 3, ChamberMembers: 3},
		{MemberID: "B001299", Congress: 119, Chamber: chamberSenate, Eligible: 3, ChamberMembers: 3},
	}
	if got := readAttendance(t, client, 119); !slices.Equal(got, want) {
		t.Errorf("member_attendance\n%+v\nwant\n%+v", got, want)
	}
	wantFields(t, onlyLogLine(t, &logs, "attendance computed"), "congress=119", "members=6",
		"house_median_pct=50", "senate_median_pct=0", "unknown_members=1")

	// Roll 4, stored since, isn't on the Clerk's list the feed serves; the next run counts it.
	storeRoll(t, store, 4, "On Passage", adams, "Yea", aderholt, "Not Voting", aguilar, "Nay")
	logs.Reset()
	if err := s.SyncVotes(ctx, 119, []int{1}); err != nil {
		t.Fatal(err)
	}
	// Missed shares 2/3, 1/3 and 0: the median is a third.
	third := 100.0 / 3
	wantHouse := []repository.MemberAttendanceRow{
		{MemberID: aderholt, Congress: 119, Chamber: chamberHouse, Eligible: 3, Missed: 2,
			ChamberMembers: 3, ChamberMedianPct: third},
		{MemberID: adams, Congress: 119, Chamber: chamberHouse, Eligible: 3, Missed: 1,
			ChamberMembers: 3, ChamberMedianPct: third},
		{MemberID: aguilar, Congress: 119, Chamber: chamberHouse, Eligible: 3, Present: 1,
			ChamberMembers: 3, ChamberMedianPct: third},
	}
	if got := readAttendance(t, client, 119)[:3]; !slices.Equal(got, wantHouse) {
		t.Errorf("member_attendance after roll 4\n%+v\nwant\n%+v", got, wantHouse)
	}
	onlyLogLine(t, &logs, "attendance computed")
}

// storeRoll writes House roll n of the 119th's first session with its members' votes (pairs
// of member ID and stored value), as an earlier vote sync did. No totals are stored, so the
// sync never counts it incomplete and fetches it again.
func storeRoll(t *testing.T, store *spannerdb.PipelineStoreImpl, n int, question string, votes ...string) {
	t.Helper()
	members := make([]repository.MemberVoteRow, 0, len(votes)/2)
	for i := 0; i+1 < len(votes); i += 2 {
		members = append(members, repository.MemberVoteRow{MemberID: votes[i], Vote: votes[i+1]})
	}
	if err := store.StoreRollCall(t.Context(), repository.CongressionalVoteRow{
		ID: rollcall.HouseID(119, 1, n), Congress: 119, Chamber: chamberHouse, Session: new(1), RollNumber: new(n),
		VoteDate: time.Date(2025, time.January, 7, 0, 0, 0, 0, time.UTC), Question: new(question),
	}, members); err != nil {
		t.Fatal(err)
	}
}

// readAttendance returns the congress's member_attendance rows by member and chamber.
func readAttendance(t *testing.T, client *spanner.Client, congress int) []repository.MemberAttendanceRow {
	t.Helper()
	iter := client.Single().Query(t.Context(), spanner.Statement{
		SQL: `SELECT member_id, chamber, eligible, missed, present, chamber_members, chamber_median_pct
		 FROM member_attendance WHERE congress = @congress ORDER BY member_id, chamber`,
		Params: map[string]any{"congress": int64(congress)},
	})
	defer iter.Stop()
	var rows []repository.MemberAttendanceRow
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return rows
		}
		if err != nil {
			t.Fatal(err)
		}
		r := repository.MemberAttendanceRow{Congress: congress}
		if err = row.Columns(&r.MemberID, &r.Chamber, &r.Eligible, &r.Missed, &r.Present, &r.ChamberMembers,
			&r.ChamberMedianPct); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
}
