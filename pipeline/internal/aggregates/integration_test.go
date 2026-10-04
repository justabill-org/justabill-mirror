package aggregates_test

import (
	"fmt"
	"log/slog"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
	"github.com/justabill-org/justabill/pipeline/internal/aggregates"
)

// TestRunOnEmulator runs the job end to end against the testdb fixture: 60 eligible CA-12
// voters on HR 1 publish the district and state cells, the national cell stays below its
// minimum, and the district's House member gets a rep_alignment row. It skips without
// SPANNER_EMULATOR_HOST.
func TestRunOnEmulator(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)

	now := time.Now().UTC().Truncate(time.Second)
	var ms []*spanner.Mutation
	for i := range 60 {
		id := fmt.Sprintf("voter-%02d", i)
		vote := "yea"
		if i%3 == 0 {
			vote = "nay"
		}
		ms = append(ms,
			spanner.Insert("users", []string{"user_id", "auth_uid", "state", "district", "created_at"},
				[]any{id, testdb.TestAuthUID(id), "CA", int64(12), now.Add(-30 * 24 * time.Hour)}),
			spanner.Insert("user_votes", []string{"user_id", "bill_id", "vote", "voted_at", "app_check_ok"},
				[]any{id, testdb.FixtureHouseBill, vote, now.Add(-3 * 24 * time.Hour), true}),
		)
	}
	if _, err := client.Apply(ctx, ms); err != nil {
		t.Fatalf("seed voters: %v", err)
	}

	store := spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: client})
	job, err := aggregates.New(store, aggregates.DefaultConfig(), slog.New(slog.DiscardHandler),
		aggregates.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err = job.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cells, err := store.ListVoteAggregates(ctx, []string{testdb.FixtureHouseBill})
	if err != nil {
		t.Fatalf("ListVoteAggregates: %v", err)
	}
	got := map[string]string{}
	for _, c := range cells {
		s := c.Status
		if c.YeaPct != nil {
			s += fmt.Sprintf(" %d%% of %d+", *c.YeaPct, *c.VotersFloor)
		}
		got[c.Scope+" "+c.ScopeKey] = s
	}
	want := map[string]string{
		"national ":      model.AggregateStatusSuppressed,
		"state CA":       model.AggregateStatusPublished + " 67% of 60+",
		"district CA-12": model.AggregateStatusPublished + " 67% of 60+",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("cells = %v, want %v", got, want)
	}

	rows, err := spannerdb.NewAggregateReader(&spannerdb.Client{Spanner: client}).
		MemberAlignment(ctx, testdb.FixtureHouseDem)
	if err != nil {
		t.Fatalf("MemberAlignment: %v", err)
	}
	// The fixture's CA-12 member voted Yea on HR 1, with the users' majority.
	if len(rows) != 1 || rows[0].ScopeKey != "CA-12" || rows[0].BillsCompared != 1 || rows[0].BillsAgreed != 1 {
		t.Errorf("alignment = %+v, want CA-12 agreeing on 1 of 1 bills", rows)
	}
}
