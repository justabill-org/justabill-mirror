package spannerdb_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

func newUserRepo(t *testing.T) (*spannerdb.UserRepository, *spanner.Client) {
	t.Helper()
	client := testdb.New(t)
	return spannerdb.NewUserRepo(&spannerdb.Client{Spanner: client}), client
}

// seedBills inserts a bills row for each ID, since votes and favorites need one.
func seedBills(ctx context.Context, t *testing.T, client *spanner.Client, ids ...string) {
	t.Helper()
	testdb.SeedCongress(ctx, t, client, testdb.FixtureCongress)
	for i, id := range ids {
		testdb.SeedBill(ctx, t, client, id, testdb.FixtureCongress, "hr", i+1, "Bill "+id)
	}
}

func TestCreateForAuthUIDIsIdempotent(t *testing.T) {
	repo, _ := newUserRepo(t)
	ctx := t.Context()

	first, err := repo.CreateForAuthUID(ctx, "u1", "uid-a", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if first.ID != "u1" || first.AuthUID != "uid-a" || first.CreatedAt.IsZero() {
		t.Errorf("created = %+v, want ID u1, AuthUID uid-a and a created_at", first)
	}

	again, err := repo.CreateForAuthUID(ctx, "u2", "uid-a", "")
	if err != nil {
		t.Fatalf("create again: %v", err)
	}
	if again.ID != "u1" {
		t.Errorf("second create ID = %q, want the existing u1", again.ID)
	}

	byUID, err := repo.GetByAuthUID(ctx, "uid-a")
	if err != nil || byUID == nil || byUID.ID != "u1" {
		t.Errorf("GetByAuthUID = %+v, %v; want u1", byUID, err)
	}
	if u, getErr := repo.GetByID(ctx, "u2"); getErr != nil || u != nil {
		t.Errorf("GetByID(u2) = %+v, %v; want nil, nil (no second row)", u, getErr)
	}
	if u, getErr := repo.GetByAuthUID(ctx, "uid-missing"); getErr != nil || u != nil {
		t.Errorf("GetByAuthUID(missing) = %+v, %v; want nil, nil", u, getErr)
	}
	if _, err = repo.CreateForAuthUID(ctx, "u3", "", ""); err == nil {
		t.Error("create with empty auth uid: want error")
	}
}

func TestAuthUIDIsUnique(t *testing.T) {
	_, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")

	_, err := client.Apply(ctx, []*spanner.Mutation{spanner.Insert("users",
		[]string{"user_id", "auth_uid", "created_at"},
		[]any{"u2", testdb.TestAuthUID("u1"), time.Now()})})
	if err == nil {
		t.Error("second user with the same auth_uid: want unique index violation")
	}
}

func TestUpdateUser(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")

	state, district := "NY", 3
	got, err := repo.Update(ctx, "u1", model.UserUpdate{State: &state, District: &district})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.State == nil || *got.State != "NY" || got.District == nil || *got.District != 3 {
		t.Errorf("updated = %+v, want NY-3", got)
	}
	if got.AuthUID != testdb.TestAuthUID("u1") {
		t.Errorf("AuthUID = %q, want it unchanged", got.AuthUID)
	}
}

func TestUpdateUserRejectsInvalidSeats(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	testdb.SeedUser(ctx, t, client, "u2")

	// u1 has no state yet; u2 is in CA-12.
	if _, err := repo.Update(ctx, "u2", model.UserUpdate{State: new("CA"), District: new(12)}); err != nil {
		t.Fatalf("set CA-12: %v", err)
	}
	tests := []struct {
		name   string
		userID string
		update model.UserUpdate
	}{
		{"unknown state", "u1", model.UserUpdate{State: new("XX"), District: new(1)}},
		{"lower-case state", "u1", model.UserUpdate{State: new("ca"), District: new(12)}},
		{"unknown state alone", "u1", model.UserUpdate{State: new("ZZ")}},
		{"district past the state's seats", "u1", model.UserUpdate{State: new("CA"), District: new(53)}},
		{"district 0 in a multi-seat state", "u1", model.UserUpdate{State: new("NY"), District: new(0)}},
		{"numbered district at large", "u1", model.UserUpdate{State: new("AK"), District: new(1)}},
		{"negative district", "u1", model.UserUpdate{State: new("TX"), District: new(-3)}},
		{"district without a state", "u1", model.UserUpdate{District: new(3)}},
		{"district past the stored state's seats", "u2", model.UserUpdate{District: new(60)}},
		{"new state the stored district isn't in", "u2", model.UserUpdate{State: new("WY")}},
		{"district while clearing the state", "u2", model.UserUpdate{State: new(""), District: new(3)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.Update(ctx, tt.userID, tt.update)
			if !errors.Is(err, repository.ErrInvalidSeat) || got != nil {
				t.Errorf("Update = %+v, %v; want an error wrapping ErrInvalidSeat", got, err)
			}
		})
	}
	assertRows(t, queryStrings(t, client,
		"SELECT user_id, IFNULL(state, '-'), IFNULL(CAST(district AS STRING), '-') FROM users ORDER BY user_id", nil),
		[]string{"u1|-|-", "u2|CA|12"})

	// Valid seats, and clearing the state alone (which keeps the district), still go through.
	for _, update := range []model.UserUpdate{
		{State: new("AK"), District: new(0)},
		{State: new("PR"), District: new(0)},
		{State: new("CA"), District: new(52)},
		{District: new(1)},
		{State: new("")},
	} {
		if _, err := repo.Update(ctx, "u1", update); err != nil {
			t.Errorf("Update(%+v) = %v, want accepted", update, err)
		}
	}
	assertRows(t, queryStrings(t, client,
		"SELECT IFNULL(state, '-'), IFNULL(CAST(district AS STRING), '-') FROM users WHERE user_id = 'u1'", nil),
		[]string{"-|1"})
}

func TestDeleteUserCascades(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	testdb.SeedUser(ctx, t, client, "u2")
	seedBills(ctx, t, client, "hr-119-1")
	testdb.SeedUserVote(ctx, t, client, "u1", "hr-119-1", "yea")
	testdb.SeedUserVote(ctx, t, client, "u2", "hr-119-1", "nay")
	if err := repo.AddFavorite(ctx, "u1", "hr-119-1"); err != nil {
		t.Fatalf("add favorite: %v", err)
	}

	if err := repo.Delete(ctx, "u1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := repo.Delete(ctx, "u1"); err != nil {
		t.Errorf("delete missing user: %v, want nil", err)
	}

	assertRows(t, queryStrings(t, client, "SELECT user_id FROM users ORDER BY user_id", nil), []string{"u2"})
	assertRows(t, queryStrings(t, client, "SELECT user_id FROM user_votes", nil), []string{"u2"})
	assertRows(t, queryStrings(t, client, "SELECT user_id FROM user_favorites", nil), nil)
}

func TestVoteAndFavoriteNeedBill(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	seedBills(ctx, t, client, "hr-119-1")

	if err := repo.CastVote(ctx, "u1", "hr-119-1", "yea", model.VoteChecks{}); err != nil {
		t.Fatalf("vote on an existing bill: %v", err)
	}
	if err := repo.CastVote(ctx, "u1", "hr-119-1", "nay", model.VoteChecks{}); err != nil {
		t.Fatalf("change vote: %v", err)
	}
	if err := repo.AddFavorite(ctx, "u1", "hr-119-1"); err != nil {
		t.Fatalf("favorite an existing bill: %v", err)
	}

	err := repo.CastVote(ctx, "u1", "hr-119-99999", "yea", model.VoteChecks{})
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("vote on a missing bill = %v, want ErrNotFound", err)
	}
	if err = repo.AddFavorite(ctx, "u1", "hr-119-99999"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("favorite a missing bill = %v, want ErrNotFound", err)
	}
	err = repo.CastVote(ctx, "nobody", "hr-119-1", "yea", model.VoteChecks{})
	if err == nil || errors.Is(err, repository.ErrNotFound) {
		t.Errorf("vote by a missing user = %v, want an error other than ErrNotFound", err)
	}

	assertRows(t, queryStrings(t, client, "SELECT bill_id, vote FROM user_votes", nil), []string{"hr-119-1|nay"})
	assertRows(t, queryStrings(t, client, "SELECT bill_id FROM user_favorites", nil), []string{"hr-119-1"})
}

func TestImportVotes(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	seedBills(ctx, t, client, "hr-119-1", "s-119-2", "hr-119-3", "hr-119-4")
	testdb.SeedUserVote(ctx, t, client, "u1", "hr-119-1", "yea")
	past := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	start := time.Now()

	res, err := repo.ImportVotes(ctx, "u1", []model.UserVote{
		{BillID: "hr-119-1", Vote: "nay", VotedAt: past},                 // server wins
		{BillID: "s-119-2", Vote: "nay", VotedAt: past},                  // inserted as is
		{BillID: "s-119-2", Vote: "yea", VotedAt: past},                  // first in batch wins
		{BillID: "hr-119-3", Vote: "skip"},                               // zero time becomes now
		{BillID: "hr-119-4", Vote: "yea", VotedAt: start.Add(time.Hour)}, // future becomes now
		{BillID: "hr-119-99999", Vote: "yea"},                            // no such bill: skipped
	}, model.VoteChecks{DailyCap: 500})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 3 || res.Capped != nil {
		t.Errorf("import = %+v, want 3 imported, none capped", res)
	}
	assertRows(t, queryStrings(t, client,
		"SELECT bill_id, vote FROM user_votes WHERE user_id = 'u1' ORDER BY bill_id", nil),
		[]string{"hr-119-1|yea", "hr-119-3|skip", "hr-119-4|yea", "s-119-2|nay"})

	export, err := repo.Export(ctx, "u1")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, v := range export.Votes {
		switch v.BillID {
		case "s-119-2":
			if !v.VotedAt.Equal(past) {
				t.Errorf("s-119-2 voted_at = %v, want %v", v.VotedAt, past)
			}
			// The server's time is recorded whatever voted_at the browser sent.
			if v.RecordedAt == nil || v.RecordedAt.Before(start) {
				t.Errorf("s-119-2 recorded_at = %v, want now", v.RecordedAt)
			}
		case "hr-119-3", "hr-119-4":
			if v.VotedAt.Before(start) || v.VotedAt.After(time.Now()) {
				t.Errorf("%s voted_at = %v, want now", v.BillID, v.VotedAt)
			}
		}
	}
}

func TestImportVotesRejects(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	seedBills(ctx, t, client, "hr-119-1")

	if res, err := repo.ImportVotes(ctx, "u1", nil, model.VoteChecks{}); err != nil || res.Imported != 0 {
		t.Errorf("empty import = %+v, %v; want 0, nil", res, err)
	}
	tooMany := make([]model.UserVote, model.MaxImportVotes+1)
	if _, err := repo.ImportVotes(ctx, "u1", tooMany, model.VoteChecks{}); err == nil {
		t.Error("import over the limit: want error")
	}
	if _, err := repo.ImportVotes(ctx, "u1", []model.UserVote{{BillID: "hr-119-1"}}, model.VoteChecks{}); err == nil {
		t.Error("import without a vote: want error")
	}
	nobody := []model.UserVote{{BillID: "hr-119-1", Vote: "yea"}}
	if _, err := repo.ImportVotes(ctx, "nobody", nobody, model.VoteChecks{}); err == nil {
		t.Error("import for a missing user: want error")
	}
	assertRows(t, queryStrings(t, client, "SELECT bill_id FROM user_votes", nil), nil)
}

func TestGetVotesHaveBillTitles(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	testdb.SeedUser(ctx, t, client, "u2")
	seedBills(ctx, t, client, "hr-119-1", "s-119-2")
	day := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	if _, err := repo.ImportVotes(ctx, "u1", []model.UserVote{
		{BillID: "hr-119-1", Vote: "yea", VotedAt: day},
		{BillID: "s-119-2", Vote: "nay", VotedAt: day.Add(time.Hour)},
	}, model.VoteChecks{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	testdb.SeedUserVote(ctx, t, client, "u2", "hr-119-1", "skip")
	// A vote whose bills row is gone still lists, without a title.
	testdb.SeedUserVote(ctx, t, client, "u1", "hr-119-99999", "skip")

	got, err := repo.GetVotes(ctx, "u1", model.ListParams{Limit: 2})
	if err != nil {
		t.Fatalf("get votes: %v", err)
	}
	if got.Total != 3 {
		t.Errorf("total = %d, want 3", got.Total)
	}
	var rows []string
	for _, v := range got.Items {
		if v.UserID != "u1" || v.AppCheckOK != nil || v.RecordedAt != nil {
			t.Errorf("vote %+v: want u1's, without app_check_ok or recorded_at", v)
		}
		rows = append(rows, v.BillID+"|"+v.Vote+"|"+v.Title)
	}
	assertRows(t, rows, []string{"hr-119-99999|skip|", "s-119-2|nay|Bill s-119-2"})

	next, err := repo.GetVotes(ctx, "u1", model.ListParams{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("get votes page 2: %v", err)
	}
	if len(next.Items) != 1 || next.Items[0].BillID != "hr-119-1" || next.Items[0].Title != "Bill hr-119-1" {
		t.Errorf("page 2 = %+v, want hr-119-1 titled Bill hr-119-1", next.Items)
	}
}

func TestExportUser(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUserWithDistrict(ctx, t, client, "u1", "CA", new(12))
	testdb.SeedUser(ctx, t, client, "u2")
	seedBills(ctx, t, client, "s-119-5")
	testdb.SeedUserVote(ctx, t, client, "u1", "hr-119-1", "yea")
	testdb.SeedUserVote(ctx, t, client, "u2", "hr-119-2", "nay")
	if err := repo.AddFavorite(ctx, "u1", "s-119-5"); err != nil {
		t.Fatalf("add favorite: %v", err)
	}
	if err := repo.CastVote(ctx, "u1", "s-119-5", "nay", model.VoteChecks{AppCheckOK: new(true)}); err != nil {
		t.Fatalf("cast vote: %v", err)
	}

	got, err := repo.Export(ctx, "u1")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if got.User.ID != "u1" || got.AuthUID != testdb.TestAuthUID("u1") ||
		got.User.State == nil || *got.User.State != "CA" {
		t.Errorf("export profile = %+v (auth_uid %q), want u1 in CA", got.User, got.AuthUID)
	}
	appCheck := map[string]*bool{}
	for _, v := range got.Votes {
		appCheck[v.BillID] = v.AppCheckOK
	}
	if len(got.Votes) != 2 || appCheck["s-119-5"] == nil || !*appCheck["s-119-5"] {
		t.Errorf("export votes = %+v, want u1's two votes, s-119-5 with app_check_ok true", got.Votes)
	}
	if ok, found := appCheck["hr-119-1"]; !found || ok != nil {
		t.Errorf("export hr-119-1 app_check_ok = %v (found %v), want NULL", ok, found)
	}
	if len(got.Favorites) != 1 || got.Favorites[0].BillID != "s-119-5" {
		t.Errorf("export favorites = %+v, want s-119-5", got.Favorites)
	}

	empty, err := repo.Export(ctx, "u2")
	if err != nil || empty.Favorites == nil {
		t.Errorf("export u2 favorites = %v, %v; want an empty, non-nil slice", empty, err)
	}
	if missing, missingErr := repo.Export(ctx, "nobody"); missingErr != nil || missing != nil {
		t.Errorf("export missing = %+v, %v; want nil, nil", missing, missingErr)
	}
}

func TestDeleteVote(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	testdb.SeedUser(ctx, t, client, "u2")
	testdb.SeedUserVote(ctx, t, client, "u1", "hr-119-1", "yea")
	testdb.SeedUserVote(ctx, t, client, "u1", "hr-119-2", "nay")
	testdb.SeedUserVote(ctx, t, client, "u2", "hr-119-1", "yea")

	if err := repo.DeleteVote(ctx, "u1", "hr-119-1"); err != nil {
		t.Fatalf("delete vote: %v", err)
	}
	if err := repo.DeleteVote(ctx, "u1", "hr-119-1"); err != nil {
		t.Errorf("delete a vote again = %v, want nil", err)
	}
	assertRows(t, queryStrings(t, client, "SELECT user_id, bill_id FROM user_votes ORDER BY user_id, bill_id", nil),
		[]string{"u1|hr-119-2", "u2|hr-119-1"})
}

func TestExportHasEveryStoredColumn(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	seedBills(ctx, t, client, "s-119-5")
	testdb.SeedUserVote(ctx, t, client, "u1", "hr-119-1", "yea")
	if err := repo.CastVote(ctx, "u1", "s-119-5", "nay", model.VoteChecks{}); err != nil {
		t.Fatalf("cast vote: %v", err)
	}

	got, err := repo.Export(ctx, "u1")
	if err != nil || got.AggExcludedAt != nil {
		t.Fatalf("export = %+v, %v; want agg_excluded_at nil before an exclusion", got, err)
	}
	for _, v := range got.Votes {
		// The seeded vote predates recorded_at; the cast one has it.
		if (v.BillID == "s-119-5") != (v.RecordedAt != nil) {
			t.Errorf("export %s recorded_at = %v", v.BillID, v.RecordedAt)
		}
	}

	excludedAt := time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
	if _, err = client.Apply(ctx, []*spanner.Mutation{spanner.Update("users",
		[]string{"user_id", "agg_excluded_at"}, []any{"u1", excludedAt})}); err != nil {
		t.Fatalf("exclude u1: %v", err)
	}
	got, err = repo.Export(ctx, "u1")
	if err != nil || got.AggExcludedAt == nil || !got.AggExcludedAt.Equal(excludedAt) {
		t.Errorf("export after exclusion = %+v, %v; want agg_excluded_at %v", got, err, excludedAt)
	}
}
