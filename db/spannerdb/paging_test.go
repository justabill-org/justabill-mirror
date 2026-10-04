package spannerdb_test

import (
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// pageAll reads every page of size limit from list and returns each item's ID in order. It
// fails the test if a page's total disagrees with the first page's.
func pageAll[T any](
	t *testing.T,
	limit int,
	list func(model.ListParams) (*model.ListResult[T], error),
	id func(T) string,
) []string {
	t.Helper()
	var ids []string
	total := -1
	for offset := 0; total < 0 || offset < total; offset += limit {
		page, err := list(model.ListParams{Offset: offset, Limit: limit})
		if err != nil {
			t.Fatalf("page at offset %d: %v", offset, err)
		}
		if total >= 0 && page.Total != total {
			t.Fatalf("page at offset %d: total = %d, want %d", offset, page.Total, total)
		}
		total = page.Total
		if len(page.Items) == 0 {
			break
		}
		for _, item := range page.Items {
			ids = append(ids, id(item))
		}
	}
	if len(ids) != total {
		t.Errorf("paged %d items %v, want total %d", len(ids), ids, total)
	}
	return ids
}

// assertOnce fails the test if any ID appears more than once.
func assertOnce(t *testing.T, ids []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("%s appears on more than one page: %v", id, ids)
		}
		seen[id] = true
	}
}

func TestBillListSortNumberPagesTies(t *testing.T) {
	client := testdb.New(t)
	testdb.SeedFixture(t.Context(), t, client)
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	sortKey, congress := "number", testdb.FixtureCongress

	ids := pageAll(t, 1, func(p model.ListParams) (*model.ListResult[model.Bill], error) {
		p.Sort, p.Congress = &sortKey, &congress
		return repo.List(t.Context(), p)
	}, func(b model.Bill) string { return b.ID })

	assertOnce(t, ids)
	// hr-119-1 and s-119-1 tie on congress and number; bill_type breaks the tie.
	hr, s := slices.Index(ids, testdb.FixtureHouseBill), slices.Index(ids, testdb.FixtureSenateBill)
	if hr < 0 || s != hr+1 {
		t.Errorf("sort=number pages = %v, want %s right before %s", ids, testdb.FixtureHouseBill,
			testdb.FixtureSenateBill)
	}
}

func TestUserVotesAndFavoritesPageTies(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	bills := []string{"hr-119-1", "hr-119-2", "hr-119-3", "hr-119-4", "hr-119-5"}
	seedBills(ctx, t, client, bills...)
	// Every row gets the same timestamp, as ImportVotes gives votes without one.
	at := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	var muts []*spanner.Mutation
	for _, b := range slices.Backward(bills) {
		muts = append(muts,
			spanner.Insert("user_votes", []string{"user_id", "bill_id", "vote", "voted_at"},
				[]any{"u1", b, "yea", at}),
			spanner.Insert("user_favorites", []string{"user_id", "bill_id", "created_at"},
				[]any{"u1", b, at}))
	}
	if _, err := client.Apply(ctx, muts); err != nil {
		t.Fatalf("seed votes and favorites: %v", err)
	}

	votes := pageAll(t, 2, func(p model.ListParams) (*model.ListResult[model.UserVote], error) {
		return repo.GetVotes(ctx, "u1", p)
	}, func(v model.UserVote) string { return v.BillID })
	if !slices.Equal(votes, bills) {
		t.Errorf("vote pages = %v, want %v", votes, bills)
	}

	favorites := pageAll(t, 2, func(p model.ListParams) (*model.ListResult[model.UserFavorite], error) {
		return repo.GetFavorites(ctx, "u1", p)
	}, func(f model.UserFavorite) string { return f.BillID })
	if !slices.Equal(favorites, bills) {
		t.Errorf("favorite pages = %v, want %v", favorites, bills)
	}
}

func TestMemberListPagesSameNames(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	for _, id := range []string{"S000003", "S000001", "S000002"} {
		testdb.SeedMember(ctx, t, client, id, "John", "Smith")
	}
	testdb.SeedMember(ctx, t, client, "A000001", "Ann", "Adams")
	repo := spannerdb.NewMemberRepo(&spannerdb.Client{Spanner: client})

	ids := pageAll(t, 1, func(p model.ListParams) (*model.ListResult[model.Member], error) {
		return repo.List(ctx, p)
	}, func(m model.Member) string { return m.BioguideID })
	want := []string{"A000001", "S000001", "S000002", "S000003"}
	if !slices.Equal(ids, want) {
		t.Errorf("member pages = %v, want %v", ids, want)
	}
}
