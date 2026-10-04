package spannerdb_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/testdb"
)

// These tests cover the write-side controls of docs/design/89-aggregate-analytics.md (#121).

func TestCreateForAuthUIDRecordsSignInProvider(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()

	created, err := repo.CreateForAuthUID(ctx, "u1", "uid-a", "google.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.SignInProvider == nil || *created.SignInProvider != "google.com" {
		t.Errorf("created provider = %v, want google.com", created.SignInProvider)
	}
	again, err := repo.CreateForAuthUID(ctx, "u2", "uid-a", "password")
	if err != nil || again.SignInProvider == nil || *again.SignInProvider != "google.com" {
		t.Errorf("second sign-in = %+v, %v; want the first provider kept", again, err)
	}

	// An account from before providers were recorded gets one on its next POST /me.
	testdb.SeedUser(ctx, t, client, "u3")
	filled, err := repo.CreateForAuthUID(ctx, "u4", testdb.TestAuthUID("u3"), "password")
	if err != nil || filled.ID != "u3" || filled.SignInProvider == nil || *filled.SignInProvider != "password" {
		t.Errorf("fill in = %+v, %v; want u3 with provider password", filled, err)
	}
	if _, err = repo.CreateForAuthUID(ctx, "u5", "uid-b", "oidc."+strings.Repeat("x", 40)); err != nil {
		t.Fatalf("create with a long provider: %v", err)
	}
	assertRows(t, queryStrings(t, client,
		"SELECT user_id, IFNULL(sign_in_provider, '-') FROM users ORDER BY user_id", nil),
		[]string{"u1|google.com", "u3|password", "u5|oidc." + strings.Repeat("x", model.MaxSignInProviderLen-5)})

	byUID, err := repo.GetByAuthUID(ctx, "uid-a")
	if err != nil || byUID.SignInProvider == nil || *byUID.SignInProvider != "google.com" {
		t.Errorf("GetByAuthUID = %+v, %v; want the provider read back", byUID, err)
	}
}

func TestUpdateLimitsDistrictChanges(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	const interval = 30 * 24 * time.Hour
	update := func(state string, district *int) (*model.User, error) {
		return repo.Update(ctx, "u1", model.UserUpdate{
			State: &state, District: district, DistrictChangeInterval: interval,
		})
	}

	before := time.Now()
	first, err := update("CA", new(12))
	if err != nil {
		t.Fatalf("first set: %v", err)
	}
	if first.DistrictChangedAt == nil || first.DistrictChangedAt.Before(before) {
		t.Fatalf("district_changed_at = %v, want stamped by the first set", first.DistrictChangedAt)
	}
	stamp := *first.DistrictChangedAt

	if _, err = update("CA", new(12)); err != nil {
		t.Errorf("same district again: %v, want no change and no error", err)
	}
	_, err = update("CA", new(13))
	changeErr, ok := errors.AsType[*repository.DistrictChangeError](err)
	if !ok {
		t.Fatalf("second change within 30 days = %v, want a *DistrictChangeError", err)
	}
	if !changeErr.NextAllowed.Equal(stamp.Add(interval)) {
		t.Errorf("NextAllowed = %v, want %v", changeErr.NextAllowed, stamp.Add(interval))
	}
	assertRows(t, queryStrings(t, client, "SELECT state, CAST(district AS STRING) FROM users", nil),
		[]string{"CA|12"})

	// Clearing the state is always allowed and keeps the stamp, so setting it again still waits.
	cleared, err := repo.Update(ctx, "u1", model.UserUpdate{
		State: new(""), District: nil, DistrictChangeInterval: interval,
	})
	if err != nil || cleared.State != nil || !cleared.DistrictChangedAt.Equal(stamp) {
		t.Fatalf("clear state = %+v, %v; want cleared with the stamp kept", cleared, err)
	}
	if _, err = update("NY", nil); !isDistrictChangeError(err) {
		t.Errorf("set again after clearing = %v, want a *DistrictChangeError", err)
	}

	// Once the interval has passed, the change goes through and is stamped again.
	backdate(t, client, "u1", time.Now().Add(-interval-time.Minute))
	moved, err := update("NY", new(3))
	if err != nil || moved.DistrictChangedAt == nil || moved.DistrictChangedAt.Before(before) {
		t.Errorf("change after 30 days = %+v, %v; want accepted and restamped", moved, err)
	}
	unlimited, err := repo.Update(ctx, "u1", model.UserUpdate{State: new("TX"), District: new(7)})
	if err != nil || unlimited.State == nil || *unlimited.State != "TX" {
		t.Errorf("change with no interval = %+v, %v; want accepted", unlimited, err)
	}

	missing, err := repo.Update(ctx, "nobody", model.UserUpdate{State: new("CA")})
	if err != nil || missing != nil {
		t.Errorf("update missing user = %+v, %v; want nil, nil", missing, err)
	}
}

func isDistrictChangeError(err error) bool {
	_, ok := errors.AsType[*repository.DistrictChangeError](err)
	return ok
}

func backdate(t *testing.T, client *spanner.Client, userID string, at time.Time) {
	t.Helper()
	if _, err := client.Apply(t.Context(), []*spanner.Mutation{
		spanner.Update("users", []string{"user_id", "district_changed_at"}, []any{userID, at}),
	}); err != nil {
		t.Fatalf("backdate district_changed_at: %v", err)
	}
}

func TestCastVoteDailyCap(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	seedBills(ctx, t, client, "hr-119-1", "hr-119-2", "hr-119-3", "hr-119-4", "hr-119-5")
	capped := model.VoteChecks{DailyCap: 3}

	for _, bill := range []string{"hr-119-1", "hr-119-2", "hr-119-3"} {
		if err := repo.CastVote(ctx, "u1", bill, "yea", capped); err != nil {
			t.Fatalf("vote on %s: %v", bill, err)
		}
	}
	if err := repo.CastVote(ctx, "u1", "hr-119-4", "yea", capped); !errors.Is(err, repository.ErrDailyVoteCap) {
		t.Fatalf("fourth bill = %v, want ErrDailyVoteCap", err)
	}
	if err := repo.CastVote(ctx, "u1", "hr-119-2", "nay", capped); err != nil {
		t.Errorf("changing a vote at the cap = %v, want allowed", err)
	}

	// Votes stored more than 24 hours ago don't count.
	backdateVote(ctx, t, client, "u1", "hr-119-1", time.Now().Add(-25*time.Hour))
	if err := repo.CastVote(ctx, "u1", "hr-119-4", "yea", capped); err != nil {
		t.Errorf("vote after one aged out = %v, want allowed", err)
	}
	if err := repo.CastVote(ctx, "u1", "hr-119-5", "yea", model.VoteChecks{}); err != nil {
		t.Errorf("vote with no cap = %v, want allowed", err)
	}
	assertRows(t, queryStrings(t, client,
		"SELECT bill_id, vote FROM user_votes WHERE user_id = 'u1' ORDER BY bill_id", nil),
		[]string{"hr-119-1|yea", "hr-119-2|nay", "hr-119-3|yea", "hr-119-4|yea", "hr-119-5|yea"})
}

func TestVotesRecordAppCheck(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	seedBills(ctx, t, client, "hr-119-1", "hr-119-2", "hr-119-3", "s-119-4")

	for bill, ok := range map[string]*bool{"hr-119-1": new(true), "hr-119-2": new(false), "hr-119-3": nil} {
		if err := repo.CastVote(ctx, "u1", bill, "yea", model.VoteChecks{AppCheckOK: ok}); err != nil {
			t.Fatalf("vote on %s: %v", bill, err)
		}
	}
	imported := []model.UserVote{{BillID: "s-119-4", Vote: "nay"}}
	if _, err := repo.ImportVotes(ctx, "u1", imported, model.VoteChecks{AppCheckOK: new(true)}); err != nil {
		t.Fatalf("import: %v", err)
	}
	// A changed vote takes the new request's result.
	if err := repo.CastVote(ctx, "u1", "hr-119-1", "nay", model.VoteChecks{}); err != nil {
		t.Fatalf("change vote: %v", err)
	}
	assertRows(t, queryStrings(t, client, `SELECT bill_id, IFNULL(CAST(app_check_ok AS STRING), 'null')
		FROM user_votes WHERE user_id = 'u1' ORDER BY bill_id`, nil),
		[]string{"hr-119-1|null", "hr-119-2|false", "hr-119-3|null", "s-119-4|true"})
}

// backdateVote sets a vote's voted_at and recorded_at to at.
func backdateVote(ctx context.Context, t *testing.T, client *spanner.Client, userID, billID string, at time.Time) {
	t.Helper()
	if _, err := client.Apply(ctx, []*spanner.Mutation{spanner.Update("user_votes",
		[]string{"user_id", "bill_id", "voted_at", "recorded_at"}, []any{userID, billID, at, at})}); err != nil {
		t.Fatalf("backdate vote: %v", err)
	}
}

func TestImportVotesDailyCap(t *testing.T) {
	repo, client := newUserRepo(t)
	ctx := t.Context()
	testdb.SeedUser(ctx, t, client, "u1")
	seedBills(ctx, t, client, "hr-119-1", "hr-119-2", "hr-119-3", "hr-119-4", "hr-119-5", "hr-119-6")
	capped := model.VoteChecks{DailyCap: 3}
	if err := repo.CastVote(ctx, "u1", "hr-119-1", "yea", capped); err != nil {
		t.Fatalf("cast vote: %v", err)
	}

	// Votes from long ago still count today: the cap reads when the server stored them.
	past := time.Now().Add(-30 * 24 * time.Hour)
	res, err := repo.ImportVotes(ctx, "u1", []model.UserVote{
		{BillID: "hr-119-1", Vote: "nay", VotedAt: past}, // already voted: skipped, not capped
		{BillID: "hr-119-2", Vote: "yea", VotedAt: past},
		{BillID: "hr-119-99999", Vote: "yea"}, // no such bill: skipped
		{BillID: "hr-119-3", Vote: "nay", VotedAt: past},
		{BillID: "hr-119-4", Vote: "yea", VotedAt: past},
		{BillID: "hr-119-5", Vote: "yea", VotedAt: past},
	}, capped)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 2 || len(res.Capped) != 2 || res.Capped[0] != "hr-119-4" || res.Capped[1] != "hr-119-5" {
		t.Errorf("import = %+v, want 2 imported and hr-119-4, hr-119-5 capped", res)
	}
	if err = repo.CastVote(ctx, "u1", "hr-119-6", "yea", capped); !errors.Is(err, repository.ErrDailyVoteCap) {
		t.Errorf("vote after the import filled the cap = %v, want ErrDailyVoteCap", err)
	}
	res, err = repo.ImportVotes(ctx, "u1", []model.UserVote{{BillID: "hr-119-4", Vote: "yea"}}, capped)
	if err != nil || res.Imported != 0 || len(res.Capped) != 1 {
		t.Errorf("import at the cap = %+v, %v; want nothing imported, hr-119-4 capped", res, err)
	}

	// Once a vote ages out, there's room for one more.
	backdateVote(ctx, t, client, "u1", "hr-119-1", time.Now().Add(-25*time.Hour))
	if res, err = repo.ImportVotes(ctx, "u1", []model.UserVote{
		{BillID: "hr-119-4", Vote: "yea"}, {BillID: "hr-119-5", Vote: "nay"},
	}, capped); err != nil || res.Imported != 1 || len(res.Capped) != 1 || res.Capped[0] != "hr-119-5" {
		t.Errorf("import after one aged out = %+v, %v; want hr-119-4 imported, hr-119-5 capped", res, err)
	}
	assertRows(t, queryStrings(t, client,
		"SELECT bill_id, vote FROM user_votes WHERE user_id = 'u1' ORDER BY bill_id", nil),
		[]string{"hr-119-1|yea", "hr-119-2|yea", "hr-119-3|nay", "hr-119-4|yea"})
}
