package testdb_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	databasepb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/testdb"
)

func TestNewAppliesSchema(t *testing.T) {
	client := testdb.New(t)

	var n int64
	stmt := spanner.Statement{SQL: `SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
		WHERE table_schema = '' AND table_name IN ('bills', 'members', 'member_votes', 'bill_gao_reports')`}
	if err := client.Single().Query(t.Context(), stmt).Do(func(r *spanner.Row) error {
		return r.Columns(&n)
	}); err != nil {
		t.Fatalf("query tables: %v", err)
	}
	if n != 4 {
		t.Errorf("found %d of 4 expected tables", n)
	}
}

// TestSeedFixtureIdentity guards the HR 1 / S 1 collision: the same number in
// another bill type or congress must be a distinct bill.
func TestSeedFixtureIdentity(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)

	cases := []struct {
		congress int64
		billType string
		want     string
	}{
		{testdb.FixtureCongress, "hr", testdb.FixtureHouseBill},
		{testdb.FixtureCongress, "s", testdb.FixtureSenateBill},
		{testdb.FixturePrevCongress, "hr", testdb.FixturePrevHouseBill},
	}
	for _, tc := range cases {
		var got string
		stmt := spanner.Statement{
			SQL: `SELECT bill_id FROM bills
				WHERE congress = @congress AND bill_type = @type AND number = 1`,
			Params: map[string]any{"congress": tc.congress, "type": tc.billType},
		}
		if err := client.Single().Query(ctx, stmt).Do(func(r *spanner.Row) error {
			return r.Columns(&got)
		}); err != nil {
			t.Fatalf("lookup %s %d: %v", tc.billType, tc.congress, err)
		}
		if got != tc.want {
			t.Errorf("%s 1 in congress %d = %q, want %q", tc.billType, tc.congress, got, tc.want)
		}
	}
}

func TestSeedFixtureLinks(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)

	// Each roll call is on its own chamber's bill, with votes from that chamber.
	stmt := spanner.Statement{SQL: `SELECT cv.bill_id, mt.chamber, COUNT(*) AS votes
		FROM congressional_votes cv
		JOIN member_votes mv ON mv.vote_id = cv.vote_id
		JOIN member_terms mt ON mt.member_id = mv.member_id AND mt.congress = cv.congress
		WHERE mt.chamber = cv.chamber
		GROUP BY cv.bill_id, mt.chamber
		ORDER BY cv.bill_id`}
	type row struct {
		BillID  string `spanner:"bill_id"`
		Chamber string `spanner:"chamber"`
		Votes   int64  `spanner:"votes"`
	}
	var got []row
	iter := client.Single().Query(ctx, stmt)
	defer iter.Stop()
	for {
		r, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			t.Fatalf("query votes: %v", err)
		}
		var v row
		if err = r.ToStruct(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	want := []row{{testdb.FixtureHouseBill, "House", 2}, {testdb.FixtureSenateBill, "Senate", 1}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("roll calls = %+v, want %+v", got, want)
	}

	// The companion relation is stored in the Congress.gov related_bills shape.
	var related spanner.NullJSON
	row1, err := client.Single().ReadRow(ctx, "bills", spanner.Key{testdb.FixtureHouseBill},
		[]string{"related_bills"})
	if err != nil {
		t.Fatalf("read related_bills: %v", err)
	}
	if err = row1.Columns(&related); err != nil {
		t.Fatalf("scan related_bills: %v", err)
	}
	var entries []struct {
		Congress int    `json:"congress"`
		Type     string `json:"type"`
		Number   int    `json:"number"`
	}
	if err = json.Unmarshal([]byte(related.String()), &entries); err != nil {
		t.Fatalf("decode related_bills %s: %v", related.String(), err)
	}
	if len(entries) != 1 || entries[0].Type != "S" || entries[0].Congress != testdb.FixtureCongress ||
		entries[0].Number != 1 {
		t.Errorf("related_bills = %+v, want S 1 of the %dth", entries, testdb.FixtureCongress)
	}
}

// TestSeedHelpers checks that every single-row seed helper writes values the
// schema accepts.
func TestSeedHelpers(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()

	testdb.SeedCongress(ctx, t, client, testdb.FixtureCongress)
	testdb.SeedBill(ctx, t, client, "hr-119-2", testdb.FixtureCongress, "hr", 2, "Seed Act")
	testdb.SeedMember(ctx, t, client, "D000004", "Dana", "Diaz")
	testdb.SeedMemberTerm(ctx, t, client, "D000004", testdb.FixtureCongress, "House", "NY",
		new(3), "D")
	testdb.SeedUser(ctx, t, client, "u1")
	testdb.SeedUserWithDistrict(ctx, t, client, "u2", "NY", new(3))
	testdb.SeedCongressionalVote(ctx, t, client, "house-119-s1-roll002", new("hr-119-2"),
		testdb.FixtureCongress, "House", time.Now())
	testdb.SeedMemberVote(ctx, t, client, "house-119-s1-roll002", "D000004", "Yea")
	testdb.SeedUserVote(ctx, t, client, "u2", "hr-119-2", "yea")
}

func TestNewEmptyHasNoTables(t *testing.T) {
	client, db := testdb.NewEmpty(t)

	if db.Project == "" || db.Instance == "" || !strings.HasPrefix(db.ID, "test_") {
		t.Errorf("NewEmpty database = %+v, want a project, an instance and a test_ ID", db)
	}
	if n := countRows(t, client, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ''`); n != 0 {
		t.Errorf("empty database has %d tables, want 0", n)
	}
}

// TestSeedFixtureForSchemaOlderSchema seeds a database that predates some of
// the fixture's tables and columns, as the migration upgrade test does.
func TestSeedFixtureForSchemaOlderSchema(t *testing.T) {
	client, db := testdb.NewEmpty(t)
	updateDDL(t, db,
		`CREATE TABLE congresses (
			number INT64 NOT NULL, start_date DATE, end_date DATE, is_current BOOL,
		) PRIMARY KEY (number)`,
		`CREATE TABLE members (
			bioguide_id STRING(10) NOT NULL, first_name STRING(MAX), last_name STRING(MAX),
		) PRIMARY KEY (bioguide_id)`,
	)

	testdb.SeedFixtureForSchema(t.Context(), t, client)

	if n := countRows(t, client, `SELECT COUNT(*) FROM congresses`); n != 2 {
		t.Errorf("congresses = %d rows, want 2", n)
	}
	// members has no lis_id yet, and the fixture's other tables don't exist.
	if n := countRows(t, client, `SELECT COUNT(*) FROM members WHERE last_name = 'Chen'`); n != 1 {
		t.Errorf("members named Chen = %d, want 1", n)
	}
}

func TestSeedFixtureForSchemaCurrentSchema(t *testing.T) {
	client := testdb.New(t)

	testdb.SeedFixtureForSchema(t.Context(), t, client)

	// On today's schema it writes every row and column SeedFixture does.
	if n := countRows(t, client, `SELECT COUNT(*) FROM bills`); n != 6 {
		t.Errorf("bills = %d rows, want 6", n)
	}
	if n := countRows(t, client, `SELECT COUNT(*) FROM bill_cra_rules`); n != 2 {
		t.Errorf("bill_cra_rules = %d rows, want 2", n)
	}
	if n := countRows(t, client, `SELECT COUNT(*) FROM bill_texts WHERE sections IS NOT NULL`); n != 1 {
		t.Errorf("bill_texts with sections = %d rows, want 1", n)
	}
	if n := countRows(t, client, `SELECT COUNT(*) FROM member_votes`); n != 3 {
		t.Errorf("member_votes = %d rows, want 3", n)
	}
	if n := countRows(t, client, `SELECT COUNT(*) FROM members WHERE lis_id = 'S901'`); n != 1 {
		t.Errorf("members with LIS ID S901 = %d, want 1", n)
	}
}

func countRows(t *testing.T, client *spanner.Client, sql string) int64 {
	t.Helper()
	var n int64
	if err := client.Single().Query(t.Context(), spanner.Statement{SQL: sql}).Do(func(r *spanner.Row) error {
		return r.Columns(&n)
	}); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func updateDDL(t *testing.T, db testdb.Database, stmts ...string) {
	t.Helper()
	ctx := t.Context()
	admin, err := database.NewDatabaseAdminClient(ctx)
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	defer admin.Close()
	op, err := admin.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
		Database:   "projects/" + db.Project + "/instances/" + db.Instance + "/databases/" + db.ID,
		Statements: stmts,
	})
	if err != nil {
		t.Fatalf("update DDL: %v", err)
	}
	if err = op.Wait(ctx); err != nil {
		t.Fatalf("wait for DDL: %v", err)
	}
}
