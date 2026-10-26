package sync

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

// ListRollCallRefs lists the stored roll calls of the congress that have a roll number, by ID,
// as the Spanner query does.
func (f *voteStore) ListRollCallRefs(_ context.Context, congress int) ([]repository.RollCallRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refsCalls++
	var refs []repository.RollCallRef
	for _, v := range f.votes {
		if v.Congress != congress || v.RollNumber == nil {
			continue
		}
		ref := repository.RollCallRef{VoteID: v.ID, Chamber: v.Chamber}
		if v.Question != nil {
			ref.Question = *v.Question
		}
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b repository.RollCallRef) int { return strings.Compare(a.VoteID, b.VoteID) })
	return refs, nil
}

// CountMemberVotes counts the stored member votes of the congress's roll calls by member,
// chamber and vote, leaving out excludedVoteIDs and roll calls without a roll number.
func (f *voteStore) CountMemberVotes(
	_ context.Context, congress int, excludedVoteIDs []string,
) ([]repository.MemberVoteCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.excluded = append(f.excluded, excludedVoteIDs)
	n := map[repository.MemberVoteCount]int64{}
	for _, v := range f.votes {
		if v.Congress != congress || v.RollNumber == nil || slices.Contains(excludedVoteIDs, v.ID) {
			continue
		}
		for _, m := range f.memberRows[v.ID] {
			n[repository.MemberVoteCount{MemberID: m.MemberID, Chamber: v.Chamber, Vote: m.Vote}]++
		}
	}
	counts := make([]repository.MemberVoteCount, 0, len(n))
	for c, k := range n {
		c.Count = k
		counts = append(counts, c)
	}
	return counts, nil
}

// ReplaceMemberAttendance keeps the congress's rows, or fails with replaceErr.
func (f *voteStore) ReplaceMemberAttendance(
	_ context.Context, congress int, rows []repository.MemberAttendanceRow,
) error {
	if f.replaceErr != nil {
		return f.replaceErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attendance[congress] = rows
	return nil
}

// Two House members of the House fixtures.
const adams, aderholt = "A000370", "A000055"

// storedSenators are the three senators in the trimmed Senate fixtures.
func storedSenators() []string { return []string{"A000382", "B001230", "B001299"} }

// onlyLogLine returns the one line of logs with message msg, failing the test unless there's
// exactly one.
func onlyLogLine(t *testing.T, logs *bytes.Buffer, msg string) string {
	t.Helper()
	var found []string
	for line := range strings.Lines(logs.String()) {
		if strings.Contains(line, `msg="`+msg+`"`) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one %q line, got %d:\n%s", msg, len(found), logs.String())
	}
	return found[0]
}

// wantFields fails the test unless line has every key=value in fields.
func wantFields(t *testing.T, line string, fields ...string) {
	t.Helper()
	for _, f := range fields {
		if !strings.Contains(" "+line, " "+f+" ") && !strings.HasSuffix(strings.TrimSpace(line), " "+f) {
			t.Errorf("log line lacks %s:\n%s", f, line)
		}
	}
}

// storedRoll writes a session-1 roll call of the 119th with its members' votes, as an earlier
// run did.
func storedRoll(t *testing.T, store *voteStore, id, chamber, question string, roll int, votes ...string) {
	t.Helper()
	members := make([]repository.MemberVoteRow, 0, len(votes)/2)
	for i := 0; i+1 < len(votes); i += 2 {
		members = append(members, repository.MemberVoteRow{MemberID: votes[i], Vote: votes[i+1]})
	}
	if err := store.StoreRollCall(t.Context(), repository.CongressionalVoteRow{
		ID: id, Congress: 119, Chamber: chamber, Session: new(1), RollNumber: new(roll),
		VoteDate: time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC), Question: new(question),
	}, members); err != nil {
		t.Fatal(err)
	}
}

// Every run ends with the attendance step. The House fixtures are quorum calls, so they're
// excluded and count for nobody: only the senators get rows, each over all six of their votes.
func TestSyncVotes_ComputesAttendance(t *testing.T) {
	store := newVoteStore()
	store.roster = append(storedSenators(), adams, aderholt)
	s, logs := lisSyncService(t, store, the119th())

	if err := s.SyncVotes(t.Context(), 119, []int{1, 2}); err != nil {
		t.Fatal(err)
	}

	wantExcluded := []string{
		"house-119-s1-roll001", "house-119-s1-roll002", "house-119-s1-roll003",
		"house-119-s2-roll001", "house-119-s2-roll002", "house-119-s2-roll003",
	}
	if len(store.excluded) != 1 || !slices.Equal(store.excluded[0], wantExcluded) {
		t.Errorf("excluded vote IDs %v, want one call with %v", store.excluded, wantExcluded)
	}
	var want []repository.MemberAttendanceRow
	for _, id := range storedSenators() {
		want = append(want, repository.MemberAttendanceRow{
			MemberID: id, Congress: 119, Chamber: chamberSenate, Eligible: 6, ChamberMembers: 3,
		})
	}
	if got := store.attendance[119]; !slices.Equal(got, want) {
		t.Errorf("attendance rows\n%+v\nwant\n%+v", got, want)
	}
	wantFields(t, onlyLogLine(t, logs, "attendance computed"), "congress=119", "rule=attendance-v1", "members=3",
		"house_median_pct=0", "senate_median_pct=0", "unknown_members=0")
	wantFields(t, onlyLogLine(t, logs, "vote sync finished"), "attendance_members=3")
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("unexpected warnings:\n%s", logs.String())
	}
}

// A run that stores no new roll call still recomputes from the stored ones, replacing the
// rows an earlier run wrote.
func TestSyncVotes_RecomputesWithoutNewRollCalls(t *testing.T) {
	store := newVoteStore(session1IDs()...)
	store.roster = []string{adams, aderholt}
	storedRoll(t, store, "house-119-s1-roll001", chamberHouse, "Call by States", 1,
		adams, "Present", aderholt, "Present")
	storedRoll(t, store, "house-119-s1-roll003", chamberHouse, "On Passage", 3,
		adams, "Yea", aderholt, "Not Voting")
	store.attendance[119] = []repository.MemberAttendanceRow{
		{MemberID: adams, Congress: 119, Chamber: chamberHouse, Eligible: 9, Missed: 9, ChamberMembers: 1},
	}
	feeds := the119th()
	s, logs := lisSyncService(t, store, feeds)

	if err := s.SyncVotes(t.Context(), 119, []int{1}); err != nil {
		t.Fatal(err)
	}

	if got := append(feeds.paths("/evs/2025/roll"), feeds.paths("/votes/")...); len(got) != 0 {
		t.Errorf("fetched %v, want no roll call", got)
	}
	want := []repository.MemberAttendanceRow{
		{MemberID: aderholt, Congress: 119, Chamber: chamberHouse, Eligible: 1, Missed: 1,
			ChamberMembers: 2, ChamberMedianPct: 50},
		{MemberID: adams, Congress: 119, Chamber: chamberHouse, Eligible: 1,
			ChamberMembers: 2, ChamberMedianPct: 50},
	}
	if got := store.attendance[119]; !slices.Equal(got, want) {
		t.Errorf("attendance rows\n%+v\nwant\n%+v", got, want)
	}
	wantFields(t, onlyLogLine(t, logs, "attendance computed"), "members=2", "house_median_pct=50",
		"senate_median_pct=0")
}

// A failed write fails the step with the cause, after the run's roll calls are stored.
func TestSyncVotes_AttendanceWriteFails(t *testing.T) {
	store := newVoteStore()
	store.roster = storedSenators()
	store.replaceErr = errors.New("spanner unavailable")
	s, logs := lisSyncService(t, store, the119th())

	err := s.SyncVotes(t.Context(), 119, []int{1, 2})
	if !errors.Is(err, store.replaceErr) || !strings.Contains(err.Error(), "compute attendance") {
		t.Fatalf("SyncVotes error %v, want the write's error in compute attendance", err)
	}
	if got := len(store.voteIDs()); got != 12 {
		t.Errorf("stored %d roll calls, want all 12", got)
	}
	if store.success != nil || len(store.failures) != 1 {
		t.Errorf("sync success %+v, failures %+v, want one failure", store.success, store.failures)
	}
	wantFields(t, onlyLogLine(t, logs, "vote sync finished"), "stored=12", "attendance_members=0")
	wantFields(t, onlyLogLine(t, logs, "no attendance rows for a congress with roll calls"), "congress=119",
		"roll_calls=12")
	if strings.Contains(logs.String(), `msg="attendance computed"`) {
		t.Errorf("logged attendance computed after a failed write:\n%s", logs.String())
	}
}

// A member with votes but no members row (sworn in since the last member sync) gets no row: the
// table's foreign key would fail the whole write. With nobody else, the run warns.
func TestSyncVotes_AttendanceSkipsUnstoredMembers(t *testing.T) {
	store := newVoteStore()
	s, logs := lisSyncService(t, store, the119th())

	if err := s.SyncVotes(t.Context(), 119, []int{1}); err != nil {
		t.Fatal(err)
	}

	if got, ok := store.attendance[119]; !ok || len(got) != 0 {
		t.Errorf("attendance rows %+v (written: %v), want an empty replace", got, ok)
	}
	wantFields(t, onlyLogLine(t, logs, "attendance computed"), "members=0", "unknown_members=3")
	wantFields(t, onlyLogLine(t, logs, "no attendance rows for a congress with roll calls"), "roll_calls=6")
}

// A cancelled run fails anyway: it doesn't start the attendance reads.
func TestSyncVotes_CancelledRunSkipsAttendance(t *testing.T) {
	store := newVoteStore()
	s, _ := lisSyncService(t, store, the119th())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := s.SyncVotes(ctx, 119, []int{1}); err == nil {
		t.Fatal("SyncVotes succeeded on a cancelled context")
	}
	if store.refsCalls != 0 {
		t.Errorf("listed roll calls %d times on a cancelled run, want none", store.refsCalls)
	}
}
