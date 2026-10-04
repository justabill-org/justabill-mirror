// Package testdb gives tests a fresh database on the Spanner emulator, with the schema
// applied, and helpers to seed it. Tests skip when SPANNER_EMULATOR_HOST is unset.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	databasepb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"

	"github.com/justabill-org/justabill/db/schema"
)

const (
	randomBytes      = 4
	congressDuration = 2
	monthsPerYear    = 12
	colBillID        = "bill_id"
	colUserID        = "user_id"
	colAuthUID       = "auth_uid"
	colCongress      = "congress"
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// New creates a unique test database on the Spanner emulator, applies
// db/schema.sql, and returns a *spanner.Client. The database is dropped
// automatically when the test finishes. The test is skipped when
// SPANNER_EMULATOR_HOST is unset, so it never touches a real instance.
func New(t *testing.T) *spanner.Client {
	t.Helper()
	skipWithoutEmulator(t)

	ddl, readErr := schema.Read()
	if readErr != nil {
		t.Fatalf("testdb: %v", readErr)
	}
	client, _ := create(t, ddl)
	return client
}

// Database names a test database on the emulator, for tools such as wrench
// that take the project, instance and database separately.
type Database struct {
	Project  string
	Instance string
	ID       string
}

// NewEmpty creates a unique test database with no tables, for tests that
// build the schema themselves (for example by running migrations). Like New,
// it drops the database when the test finishes and skips without the emulator.
func NewEmpty(t *testing.T) (*spanner.Client, Database) {
	t.Helper()
	skipWithoutEmulator(t)
	return create(t, nil)
}

func skipWithoutEmulator(t *testing.T) {
	t.Helper()
	if os.Getenv("SPANNER_EMULATOR_HOST") == "" {
		t.Skip("testdb: SPANNER_EMULATOR_HOST not set; skipping emulator test")
	}
}

// create makes a uniquely named database with the given DDL statements and
// registers its cleanup.
func create(t *testing.T, ddl []string) (*spanner.Client, Database) {
	t.Helper()

	b := make([]byte, randomBytes)
	_, _ = rand.Read(b)
	db := Database{
		Project:  envOrDefault("SPANNER_PROJECT", "test-project"),
		Instance: envOrDefault("SPANNER_INSTANCE", "test-instance"),
		ID:       "test_" + hex.EncodeToString(b),
	}

	ctx := context.Background()

	adminClient, err := database.NewDatabaseAdminClient(ctx)
	if err != nil {
		t.Fatalf("testdb: admin client: %v", err)
	}

	parent := fmt.Sprintf("projects/%s/instances/%s", db.Project, db.Instance)
	op, err := adminClient.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          parent,
		CreateStatement: fmt.Sprintf("CREATE DATABASE `%s`", db.ID),
		ExtraStatements: ddl,
	})
	if err != nil {
		t.Fatalf("testdb: create database: %v", err)
	}
	if _, err = op.Wait(ctx); err != nil {
		t.Fatalf("testdb: wait create database: %v", err)
	}

	dbPath := fmt.Sprintf("%s/databases/%s", parent, db.ID)
	client, err := spanner.NewClient(ctx, dbPath)
	if err != nil {
		t.Fatalf("testdb: spanner client: %v", err)
	}

	t.Cleanup(func() {
		client.Close()
		_ = adminClient.DropDatabase(context.Background(), &databasepb.DropDatabaseRequest{
			Database: dbPath,
		})
		_ = adminClient.Close()
	})

	return client, db
}

// SeedCongress inserts a congress row.
func SeedCongress(ctx context.Context, t *testing.T, client *spanner.Client, number int) {
	t.Helper()
	start := civil.DateOf(time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC))
	end := start.AddMonths(congressDuration * monthsPerYear)
	_, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.InsertOrUpdate("congresses",
			[]string{"number", "start_date", "end_date", "is_current"},
			[]any{int64(number), start, end, true}),
	})
	if err != nil {
		t.Fatalf("testdb: seed congress: %v", err)
	}
}

// SeedBill inserts a bill row.
func SeedBill(ctx context.Context, t *testing.T, client *spanner.Client,
	id string, congress int, billType string, number int, title string) {
	t.Helper()
	_, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("bills",
			[]string{colBillID, colCongress, "bill_type", "number", "title", "introduced_date"},
			[]any{id, int64(congress), billType, int64(number), title, civil.DateOf(time.Now())}),
	})
	if err != nil {
		t.Fatalf("testdb: seed bill %s: %v", id, err)
	}
}

// SeedMember inserts a member row.
func SeedMember(ctx context.Context, t *testing.T, client *spanner.Client,
	bioguideID, firstName, lastName string) {
	t.Helper()
	_, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("members",
			[]string{"bioguide_id", "first_name", "last_name"},
			[]any{bioguideID, firstName, lastName}),
	})
	if err != nil {
		t.Fatalf("testdb: seed member %s: %v", bioguideID, err)
	}
}

// SeedMemberTerm inserts a member_terms row.
func SeedMemberTerm(ctx context.Context, t *testing.T, client *spanner.Client,
	memberID string, congress int, chamber, state string, district *int, party string) {
	t.Helper()
	var dist spanner.NullInt64
	if district != nil {
		dist = spanner.NullInt64{Int64: int64(*district), Valid: true}
	}
	_, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("member_terms",
			[]string{"member_id", colCongress, "chamber", "state", "district", "party"},
			[]any{memberID, int64(congress), chamber, state, dist, party}),
	})
	if err != nil {
		t.Fatalf("testdb: seed member term: %v", err)
	}
}

// SeedUser inserts a user row whose auth_uid is TestAuthUID(id).
func SeedUser(ctx context.Context, t *testing.T, client *spanner.Client, id string) {
	t.Helper()
	_, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("users",
			[]string{colUserID, colAuthUID, "created_at"},
			[]any{id, TestAuthUID(id), time.Now()}),
	})
	if err != nil {
		t.Fatalf("testdb: seed user: %v", err)
	}
}

// SeedUserWithDistrict inserts a user with state and district whose auth_uid
// is TestAuthUID(id).
func SeedUserWithDistrict(ctx context.Context, t *testing.T, client *spanner.Client,
	id, state string, district *int) {
	t.Helper()
	var dist spanner.NullInt64
	if district != nil {
		dist = spanner.NullInt64{Int64: int64(*district), Valid: true}
	}
	_, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("users",
			[]string{colUserID, colAuthUID, "state", "district", "created_at"},
			[]any{id, TestAuthUID(id), state, dist, time.Now()}),
	})
	if err != nil {
		t.Fatalf("testdb: seed user with district: %v", err)
	}
}

// SeedCongressionalVote inserts a congressional_votes row.
func SeedCongressionalVote(ctx context.Context, t *testing.T, client *spanner.Client,
	id string, billID *string, congress int, chamber string, voteDate time.Time) {
	t.Helper()
	var bid spanner.NullString
	if billID != nil {
		bid = spanner.NullString{StringVal: *billID, Valid: true}
	}
	_, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("congressional_votes",
			[]string{"vote_id", colBillID, colCongress, "chamber", "vote_date"},
			[]any{id, bid, int64(congress), chamber, voteDate}),
	})
	if err != nil {
		t.Fatalf("testdb: seed congressional vote: %v", err)
	}
}

// SeedMemberVote inserts a member_votes row.
func SeedMemberVote(ctx context.Context, t *testing.T, client *spanner.Client,
	congressionalVoteID, memberID, vote string) {
	t.Helper()
	_, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("member_votes",
			[]string{"vote_id", "member_id", "vote"},
			[]any{congressionalVoteID, memberID, vote}),
	})
	if err != nil {
		t.Fatalf("testdb: seed member vote: %v", err)
	}
}

// SeedUserVote inserts a user_votes row.
func SeedUserVote(ctx context.Context, t *testing.T, client *spanner.Client,
	userID, billID, vote string) {
	t.Helper()
	_, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("user_votes",
			[]string{colUserID, colBillID, "vote", "voted_at"},
			[]any{userID, billID, vote, time.Now()}),
	})
	if err != nil {
		t.Fatalf("testdb: seed user vote: %v", err)
	}
}

// TestAuthUID is the auth_uid SeedUser and SeedUserWithDistrict store for a
// user ID.
func TestAuthUID(userID string) string { return "test-uid-" + userID }

// IntPtr returns a pointer to an int.
//
//go:fix inline
func IntPtr(v int) *int { return new(v) }

// StrPtr returns a pointer to a string.
//
//go:fix inline
func StrPtr(v string) *string { return new(v) }
