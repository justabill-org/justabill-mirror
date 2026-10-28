package spannerdb_test

import (
	"maps"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// Bills built to hit each passage rank (#1090).
const (
	rankContested = "hr-119-910" // a 216–214 House passage
	rankLopsided  = "hr-119-911" // a 420–3 House passage
	rankVoice     = "hr-119-912" // passed the House by voice vote
	rankStale     = "hr-119-913" // no vote, but a rank left from before
	rankLatest    = "hr-119-914" // a 216–214 passage, then a 420–3 concurrence in the same chamber
	rankProcedure = "hr-119-915" // only a procedural roll call
	rankBothKinds = "hr-119-916" // a Senate voice vote and a contested House roll call
	rankPrevious  = "hr-118-910" // the previous congress, contested
)

func TestRefreshPassageRanks(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	seedPassageRanks(t, store, client)
	before := billUpdatedAt(t, client, rankContested)

	n, err := store.RefreshPassageRanks(ctx, testdb.FixtureCongress)
	if err != nil {
		t.Fatalf("RefreshPassageRanks: %v", err)
	}
	want := map[string]*int64{
		rankContested: new(int64(model.PassageRankContested)),
		rankLopsided:  new(int64(model.PassageRankRoll)),
		rankVoice:     new(int64(model.PassageRankVoice)),
		rankStale:     nil,
		rankLatest:    new(int64(model.PassageRankRoll)),
		rankProcedure: nil,
		rankBothKinds: new(int64(model.PassageRankContested)),
		rankPrevious:  nil, // another congress: not read, not written
	}
	checkPassageRanks(t, client, want)
	// Written: the five bills with a rank, and the stale one cleared.
	if n != 6 {
		t.Errorf("RefreshPassageRanks wrote %d bills; want 6", n)
	}
	if after := billUpdatedAt(t, client, rankContested); !before.Valid || after != before {
		t.Errorf("updated_at went from %v to %v; want it set and untouched", before, after)
	}

	t.Run("a second run writes nothing", func(t *testing.T) {
		again, againErr := store.RefreshPassageRanks(ctx, testdb.FixtureCongress)
		if againErr != nil || again != 0 {
			t.Errorf("RefreshPassageRanks = %d, %v; want 0, nil", again, againErr)
		}
		checkPassageRanks(t, client, want)
	})

	t.Run("a new vote moves a bill up", func(t *testing.T) {
		cardVote(t, store, rankVoice, "senate-119-1-090", "Senate", 90, cardDay(2025, time.June, 2),
			"On Passage of the Bill", [4]int{51, 49, 0, 0})
		moved, movedErr := store.RefreshPassageRanks(ctx, testdb.FixtureCongress)
		if movedErr != nil || moved != 1 {
			t.Errorf("RefreshPassageRanks = %d, %v; want 1, nil", moved, movedErr)
		}
		next := maps.Clone(want)
		next[rankVoice] = new(int64(model.PassageRankContested))
		checkPassageRanks(t, client, next)
	})

	t.Run("a congress with no votes", func(t *testing.T) {
		none, noneErr := store.RefreshPassageRanks(ctx, 1)
		if noneErr != nil || none != 0 {
			t.Errorf("RefreshPassageRanks(1) = %d, %v; want 0, nil", none, noneErr)
		}
	})
}

func seedPassageRanks(t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client) {
	t.Helper()
	for _, n := range []int{910, 911, 912, 913, 914, 915, 916} {
		cardBill(t, store, "hr", n)
	}
	if err := store.MarkBillSynced(t.Context(), rankContested); err != nil {
		t.Fatalf("mark synced: %v", err)
	}
	cardVote(t, store, rankContested, "house-119-roll910", "House", 910, cardDay(2025, time.March, 1),
		"On Passage", [4]int{216, 214, 0, 3})
	cardVote(t, store, rankLopsided, "house-119-roll911", "House", 911, cardDay(2025, time.March, 1),
		"On Motion to Suspend the Rules and Pass", [4]int{420, 3, 0, 10})
	cardUnrecorded(t, store, rankVoice, "house-119-voice-hr-119-912-20250301", "House",
		cardDay(2025, time.March, 1), "On Passage")
	cardVote(t, store, rankLatest, "house-119-roll914", "House", 914, cardDay(2025, time.March, 1),
		"On Passage", [4]int{216, 214, 0, 3})
	cardVote(t, store, rankLatest, "house-119-roll915", "House", 915, cardDay(2025, time.May, 1),
		"On Motion to Concur in the Senate Amendment", [4]int{420, 3, 0, 10})
	cardVote(t, store, rankProcedure, "house-119-roll916", "House", 916, cardDay(2025, time.March, 1),
		"On Motion to Recommit", [4]int{200, 210, 0, 22})
	cardUnrecorded(t, store, rankBothKinds, "senate-119-uc-hr-119-916-20250401", "Senate",
		cardDay(2025, time.April, 1), "Passed Senate without amendment by Unanimous Consent")
	cardVote(t, store, rankBothKinds, "house-119-roll917", "House", 917, cardDay(2025, time.May, 1),
		"On Passage", [4]int{230, 190, 0, 12})
	// A vote naming a bill the pipeline hasn't stored yet is skipped.
	cardVote(t, store, "hr-119-919", "house-119-roll919", "House", 919, cardDay(2025, time.March, 1),
		"On Passage", [4]int{216, 214, 0, 3})

	prev := rankPrevious
	if err := store.UpsertBill(t.Context(), repository.BillRow{
		ID: prev, Congress: testdb.FixturePrevCongress, BillType: "hr", Number: 910, Title: "Previous bill",
	}); err != nil {
		t.Fatalf("upsert bill: %v", err)
	}
	roll := 910
	if err := store.UpsertCongressionalVote(t.Context(), repository.CongressionalVoteRow{
		ID: "house-118-roll910", BillID: &prev, Congress: testdb.FixturePrevCongress, Chamber: "House",
		RollNumber: &roll, VoteDate: cardDay(2024, time.March, 1), Question: new("On Passage"),
		Yeas: new(216), Nays: new(214),
	}); err != nil {
		t.Fatalf("upsert vote: %v", err)
	}

	if _, err := client.Apply(t.Context(), []*spanner.Mutation{
		spanner.Update("bills", []string{"bill_id", "passage_rank"}, []any{rankStale, int64(model.PassageRankRoll)}),
	}); err != nil {
		t.Fatalf("seed stale rank: %v", err)
	}
}

// checkPassageRanks compares each bill's stored passage_rank with want (nil: NULL).
func checkPassageRanks(t *testing.T, client *spanner.Client, want map[string]*int64) {
	t.Helper()
	for id, w := range want {
		row, err := client.Single().ReadRow(t.Context(), "bills", spanner.Key{id}, []string{"passage_rank"})
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		var got spanner.NullInt64
		if err = row.Columns(&got); err != nil {
			t.Fatalf("decode %s: %v", id, err)
		}
		switch {
		case w == nil && got.Valid:
			t.Errorf("%s passage_rank = %d; want NULL", id, got.Int64)
		case w != nil && (!got.Valid || got.Int64 != *w):
			t.Errorf("%s passage_rank = %v; want %d", id, got, *w)
		}
	}
}
