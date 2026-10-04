package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"testing"
)

func TestTally_CountsVotesPerChamberAndSession(t *testing.T) {
	store := newVoteStore()
	s, _ := voteService(t, store, the119th())
	tally := NewTally()
	s.SetTally(tally)

	if err := s.SyncVotes(t.Context(), 119, []int{1, 2}); err != nil {
		t.Fatal(err)
	}

	for _, chamber := range []string{chamberHouse, chamberSenate} {
		for _, session := range []int{1, 2} {
			if got := tally.Votes(chamber, session); got != 3 {
				t.Errorf("%s session %d votes = %d, want 3", chamber, session, got)
			}
		}
	}
	if got := tally.UnmatchedSenators(); len(got) != 0 {
		t.Errorf("unmatched senators = %v, want none: the store knows all three", got)
	}
}

// unmatchedSenatorStore knows only one of the fixtures' three senators, by LIS ID or by name.
type unmatchedSenatorStore struct{ *voteStore }

func (unmatchedSenatorStore) LISLookup(context.Context) (map[string]string, error) {
	return map[string]string{"S428": "A000382"}, nil
}

func (unmatchedSenatorStore) ResolveSenatorByName(context.Context, string, string, string) (string, error) {
	return "", errors.New("no match")
}

func (unmatchedSenatorStore) ResolveSenatorByLastName(context.Context, string, string) (string, error) {
	return "", errors.New("no match")
}

func TestTally_CountsEachUnmatchedSenatorOnce(t *testing.T) {
	store := unmatchedSenatorStore{newVoteStore()}
	feeds := the119th()
	feeds.houseRolls = nil
	s, _ := voteService(t, store, feeds)
	tally := NewTally()
	s.SetTally(tally)

	// Three votes in each of two sessions, each with the same two unknown senators.
	if err := s.SyncVotes(t.Context(), 119, []int{1, 2}); err != nil {
		t.Fatal(err)
	}

	if got, want := tally.UnmatchedSenators(), []string{"S354", "S429"}; !slices.Equal(got, want) {
		t.Errorf("unmatched senators = %v, want %v", got, want)
	}
	if got := tally.Votes(chamberSenate, 2); got != 3 {
		t.Errorf("senate session 2 votes = %d, want 3 (unmatched senators don't fail a vote)", got)
	}
}

func TestTally_CountsBillsByID(t *testing.T) {
	store := &billSyncStore{}
	api := &billAPI{
		missingPaths: map[string]bool{"/bill/119/hr/404": true},
		failPaths:    map[string]bool{"/bill/119/hr/5/subjects": true},
	}
	s := newBillSyncService(t, store, api)
	tally := NewTally()
	s.SetTally(tally)

	s.SyncBillsByID(t.Context(), []string{"hr-119-404", "hr-119-5", "hr-119-6", "hr-119-7", "pn-119-5"})

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).InfoContext(t.Context(), "summary", tally.Attrs()...)
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"bills_fetched": 2.0, "bills_failed": 1.0, "unresolved_bill_refs": 2.0}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("%s = %v, want %v", k, line[k], v)
		}
	}
}

func TestTally_Attrs(t *testing.T) {
	tally := NewTally()
	tally.addVotes(2, 517, 339)
	tally.addVotes(1, 724, 352)
	tally.addBills(480, 3, 12)
	tally.addUnmatchedSenator("S999")
	tally.addUnmatchedSenator("S999")

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).InfoContext(t.Context(), "summary", tally.Attrs()...)
	var line struct {
		Votes      map[string]int `json:"votes"`
		Fetched    int            `json:"bills_fetched"`
		Failed     int            `json:"bills_failed"`
		Unresolved int            `json:"unresolved_bill_refs"`
		Unmatched  int            `json:"unmatched_senators"`
		LISIDs     []string       `json:"unmatched_senator_lis_ids"`
	}
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	wantVotes := map[string]int{"house_s1": 724, "house_s2": 517, "senate_s1": 352, "senate_s2": 339}
	if !maps.Equal(line.Votes, wantVotes) {
		t.Errorf("votes = %v, want %v", line.Votes, wantVotes)
	}
	if line.Fetched != 480 || line.Failed != 3 || line.Unresolved != 12 || line.Unmatched != 1 ||
		!slices.Equal(line.LISIDs, []string{"S999"}) {
		t.Errorf("summary = %+v, want 480 fetched, 3 failed, 12 unresolved, unmatched [S999]", line)
	}
	if want := `"votes":{"house_s1":724,"house_s2":517,"senate_s1":352,"senate_s2":339}`; !bytes.Contains(
		buf.Bytes(), []byte(want)) {
		t.Errorf("votes aren't House first, then by session:\n%s", buf.String())
	}
}

func TestTally_NilCountsNothing(t *testing.T) {
	// A service without SetTally has a nil tally; counting into it must not panic.
	s, _ := voteService(t, newVoteStore(), the119th())
	if err := s.SyncVotes(t.Context(), 119, []int{1}); err != nil {
		t.Fatal(err)
	}
	var tally *Tally
	tally.addBills(1, 1, 1)
	tally.addUnmatchedSenator("S1")
}
