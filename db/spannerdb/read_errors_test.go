package spannerdb_test

import (
	"context"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// Every read and write returns the store's error rather than a partial or empty result (#920).

// failingCall is one repository call that must fail on a canceled context.
type failingCall struct {
	name string
	call func(ctx context.Context) error
}

// assertAllFail runs each call on a canceled context and wants an error from every one.
func assertAllFail(t *testing.T, calls []failingCall) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(ctx); err == nil {
				t.Errorf("%s on a canceled context: want an error", c.name)
			}
		})
	}
}

// errOf drops a call's result and keeps its error.
func errOf[T any](_ T, err error) error { return err }

func TestBillRepo_ReadErrors(t *testing.T) {
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: testdb.New(t)})
	bill := testdb.FixtureHouseBill
	page := model.ListParams{Limit: 10}
	assertAllFail(t, []failingCall{
		{"List", func(ctx context.Context) error { return errOf(repo.List(ctx, page)) }},
		{"CountByStatus", func(ctx context.Context) error { return errOf(repo.CountByStatus(ctx, page)) }},
		{"Index", func(ctx context.Context) error { return errOf(repo.Index(ctx, testdb.FixtureCongress)) }},
		{"StatusIndex", func(ctx context.Context) error {
			return errOf(repo.StatusIndex(ctx, testdb.FixtureCongress))
		}},
		{"PolicyAreas", func(ctx context.Context) error { return errOf(repo.PolicyAreas(ctx)) }},
		{"GetByID", func(ctx context.Context) error { return errOf(repo.GetByID(ctx, bill)) }},
		{"GetActions", func(ctx context.Context) error { return errOf(repo.GetActions(ctx, bill)) }},
		{"GetSummaries", func(ctx context.Context) error {
			return errOf(repo.GetSummaries(ctx, []string{bill}))
		}},
		{"GetTextVersions", func(ctx context.Context) error { return errOf(repo.GetTextVersions(ctx, bill)) }},
		{"GetDiffs", func(ctx context.Context) error { return errOf(repo.GetDiffs(ctx, bill)) }},
		{"GetTextContent", func(ctx context.Context) error { return errOf(repo.GetTextContent(ctx, bill, "v1")) }},
		{"GetDiffByID", func(ctx context.Context) error { return errOf(repo.GetDiffByID(ctx, bill, "d1")) }},
		{"GetDiffSummary", func(ctx context.Context) error { return errOf(repo.GetDiffSummary(ctx, "d1")) }},
		{"GetStatusHistory", func(ctx context.Context) error { return errOf(repo.GetStatusHistory(ctx, bill)) }},
		{"GetAmendments", func(ctx context.Context) error { return errOf(repo.GetAmendments(ctx, bill)) }},
	})
}

func TestMemberRepo_ReadErrors(t *testing.T) {
	repo := spannerdb.NewMemberRepo(&spannerdb.Client{Spanner: testdb.New(t)})
	assertAllFail(t, []failingCall{
		{"List", func(ctx context.Context) error { return errOf(repo.List(ctx, model.ListParams{Limit: 10})) }},
		{"GetByID", func(ctx context.Context) error { return errOf(repo.GetByID(ctx, testdb.FixtureHouseDem)) }},
		{"GetByDistrict", func(ctx context.Context) error { return errOf(repo.GetByDistrict(ctx, "CA", 12)) }},
		{"GetRecentVotes", func(ctx context.Context) error {
			return errOf(repo.GetRecentVotes(ctx, testdb.FixtureHouseDem, 10))
		}},
	})
}

func TestUserRepo_StoreErrors(t *testing.T) {
	repo := spannerdb.NewUserRepo(&spannerdb.Client{Spanner: testdb.New(t)})
	const user = "user-920"
	bill := testdb.FixtureHouseBill
	page := model.ListParams{Limit: 10}
	assertAllFail(t, []failingCall{
		{"CreateForAuthUID", func(ctx context.Context) error {
			return errOf(repo.CreateForAuthUID(ctx, user, "uid-920", "password"))
		}},
		{"Update without changes", func(ctx context.Context) error {
			return errOf(repo.Update(ctx, user, model.UserUpdate{}))
		}},
		{"Delete", func(ctx context.Context) error { return repo.Delete(ctx, user) }},
		{"DeleteVote", func(ctx context.Context) error { return repo.DeleteVote(ctx, user, bill) }},
		{"RemoveFavorite", func(ctx context.Context) error { return repo.RemoveFavorite(ctx, user, bill) }},
		{"GetVotes", func(ctx context.Context) error { return errOf(repo.GetVotes(ctx, user, page)) }},
		{"GetFavorites", func(ctx context.Context) error { return errOf(repo.GetFavorites(ctx, user, page)) }},
		{"Export", func(ctx context.Context) error { return errOf(repo.Export(ctx, user)) }},
	})
}

func TestPipelineStore_StoreErrors(t *testing.T) {
	store := spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: testdb.New(t)})
	bill := testdb.FixtureHouseBill
	congress := testdb.FixtureCongress
	run := repository.SyncRun{Step: "bills", Congress: congress, StartedAt: time.Now()}
	assertAllFail(t, []failingCall{
		{"ListMemberIDs", func(ctx context.Context) error { return errOf(store.ListMemberIDs(ctx)) }},
		{"UpsertBill", func(ctx context.Context) error {
			return store.UpsertBill(ctx, repository.BillRow{ID: bill, Congress: congress})
		}},
		{"MarkBillSynced", func(ctx context.Context) error { return store.MarkBillSynced(ctx, bill) }},
		{"ListBillIDsSyncedSince", func(ctx context.Context) error {
			return errOf(store.ListBillIDsSyncedSince(ctx, congress, time.Time{}))
		}},
		{"ExistingVoteIDs", func(ctx context.Context) error {
			return errOf(store.ExistingVoteIDs(ctx, []string{testdb.FixtureHouseVote}))
		}},
		{"QueryUnfetchedTextVersions", func(ctx context.Context) error {
			return errOf(store.QueryUnfetchedTextVersions(ctx, 0))
		}},
		{"FindPreviousVersion", func(ctx context.Context) error {
			return errOf(store.FindPreviousVersion(ctx, bill, "v1"))
		}},
		{"LoadSections", func(ctx context.Context) error { return errOf(store.LoadSections(ctx, "v1")) }},
		{"FindUnfetchedVersion", func(ctx context.Context) error {
			return errOf(store.FindUnfetchedVersion(ctx, bill, "ih"))
		}},
		{"LISLookup", func(ctx context.Context) error { return errOf(store.LISLookup(ctx)) }},
		{"ListSenators", func(ctx context.Context) error { return errOf(store.ListSenators(ctx, congress)) }},
		{"IncompleteVoteIDs", func(ctx context.Context) error {
			return errOf(store.IncompleteVoteIDs(ctx, congress, "House"))
		}},
		{"RecordSyncSuccess", func(ctx context.Context) error { return store.RecordSyncSuccess(ctx, run) }},
		{"SaveSyncCheckpoint", func(ctx context.Context) error {
			return store.SaveSyncCheckpoint(ctx, "bills", congress, nil, 0)
		}},
	})
}

func TestMemberList_TermFilters(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)
	repo := spannerdb.NewMemberRepo(&spannerdb.Client{Spanner: client})

	tests := []struct {
		name   string
		params model.ListParams
		want   []string
	}{
		{"congress", model.ListParams{Congress: new(testdb.FixtureCongress)},
			[]string{testdb.FixtureHouseDem, testdb.FixtureHouseRep, testdb.FixtureSenatorRep}},
		// The web sends lowercase chambers.
		{"chamber", model.ListParams{Chamber: new("senate")}, []string{testdb.FixtureSenatorRep}},
		{"state and district", model.ListParams{State: new("CA"), District: new(12)},
			[]string{testdb.FixtureHouseDem}},
		{"no match", model.ListParams{State: new("TX"), District: new(12)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.params.Limit = 10
			got, err := repo.List(ctx, tt.params)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got.Items == nil {
				t.Fatal("List items = nil, want an empty slice")
			}
			var ids []string
			for _, m := range got.Items {
				ids = append(ids, m.BioguideID)
			}
			assertRows(t, ids, tt.want)
			if got.Total != len(tt.want) {
				t.Errorf("List total = %d, want %d", got.Total, len(tt.want))
			}
		})
	}
}

func TestBillRepo_MissingRows(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})

	if got, err := repo.GetSummary(ctx, "hr-119-424242"); err != nil || got != nil {
		t.Errorf("GetSummary(missing) = %+v, %v; want nil, nil", got, err)
	}
	diffs, err := repo.GetDiffs(ctx, "hr-119-424242")
	if err != nil || diffs == nil || len(diffs) != 0 {
		t.Errorf("GetDiffs(missing) = %#v, %v; want an empty slice", diffs, err)
	}
}

func TestQueryUnfetchedTextVersions_Limit(t *testing.T) {
	store, client := newSummaryStore(t)
	seedQueueTexts(t, store, client)

	refs, err := store.QueryUnfetchedTextVersions(t.Context(), 1)
	if err != nil {
		t.Fatalf("QueryUnfetchedTextVersions: %v", err)
	}
	if len(refs) != 1 || refs[0].BillID != billID(refetchedBill) || refs[0].VersionCode != "rh" {
		t.Errorf("QueryUnfetchedTextVersions(limit 1) = %+v, want only %s's rh", refs, billID(refetchedBill))
	}
}
