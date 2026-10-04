package testdb

import (
	"context"
	"iter"
	"reflect"
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/fixture"
)

// IDs seeded by SeedFixture, from the db/fixture package. HR 1 and S 1 share a
// number on purpose, and HR 1 exists in two congresses, so tests catch bill
// identities that drop the type or the congress.
const (
	FixturePrevCongress = fixture.PrevCongress
	FixtureCongress     = fixture.Congress

	FixtureHouseBill     = fixture.HouseBill
	FixtureSenateBill    = fixture.SenateBill
	FixturePrevHouseBill = fixture.PrevHouseBill
	FixtureLawBill       = fixture.LawBill
	FixtureLawVersion    = fixture.LawTextVersion
	FixtureCRABill       = fixture.CRABill
	FixtureCRAUnmatched  = fixture.CRAUnmatchedBill
	FixtureCRADocument   = fixture.CRADocument

	FixtureHouseDem   = fixture.HouseDem
	FixtureHouseRep   = fixture.HouseRep
	FixtureSenatorRep = fixture.SenatorRep
	FixtureSenatorLIS = fixture.SenatorLIS

	FixtureHouseVote  = fixture.HouseVote
	FixtureSenateVote = fixture.SenateVote
)

// SeedFixture seeds a small, connected slice of Congress for integration
// tests: two congresses, HR 1 and S 1 of the 119th as companion bills (plus
// HR 1 of the 118th), HR 808 of the 119th, which became law and has a text
// version, its text and a summary, two CRA resolutions of the 119th (SJRes 41,
// its rule matched to a Federal Register document, and HJRes 63, unmatched),
// three members across two parties and both
// chambers, and one roll call per chamber with member votes.
func SeedFixture(ctx context.Context, t *testing.T, client *spanner.Client) {
	t.Helper()

	muts, err := fixture.Mutations()
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	if _, err = client.Apply(ctx, muts); err != nil {
		t.Fatalf("testdb: seed fixture: %v", err)
	}
}

// SeedFixtureForSchema inserts SeedFixture's rows into a database whose schema
// may be older than db/schema.sql, such as one migrated to main's version
// before a branch's new migrations run. Tables and columns the database
// doesn't have yet are left out, so the rows are what older code could have
// written. It fails the test if the rows still don't fit, for example when a
// column the fixture no longer sets is NOT NULL there.
func SeedFixtureForSchema(ctx context.Context, t *testing.T, client *spanner.Client) {
	t.Helper()

	have := schemaColumns(ctx, t, client)
	var muts []*spanner.Mutation
	for _, table := range fixture.Tables() {
		cols, ok := have[table.Name]
		if !ok {
			continue
		}
		for _, row := range table.Rows {
			var names []string
			var values []any
			for name, value := range taggedFields(row) {
				if cols[name] {
					names = append(names, name)
					values = append(values, value)
				}
			}
			muts = append(muts, spanner.Insert(table.Name, names, values))
		}
	}
	if _, err := client.Apply(ctx, muts); err != nil {
		t.Fatalf("testdb: seed fixture for the database's schema: %v", err)
	}
}

// schemaColumns returns the database's user tables and their columns.
func schemaColumns(ctx context.Context, t *testing.T, client *spanner.Client) map[string]map[string]bool {
	t.Helper()

	type columnRow struct {
		Table  string `spanner:"table_name"`
		Column string `spanner:"column_name"`
	}
	stmt := spanner.Statement{SQL: `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_catalog = '' AND table_schema = ''`}
	have := map[string]map[string]bool{}
	rows := client.Single().Query(ctx, stmt)
	err := rows.Do(func(r *spanner.Row) error {
		var c columnRow
		if err := r.ToStruct(&c); err != nil {
			return err
		}
		if have[c.Table] == nil {
			have[c.Table] = map[string]bool{}
		}
		have[c.Table][c.Column] = true
		return nil
	})
	if err != nil {
		t.Fatalf("testdb: read the database's columns: %v", err)
	}
	return have
}

// taggedFields yields a fixture row's columns (its fields' spanner tags) and
// values, in field order.
func taggedFields(row any) iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		v := reflect.ValueOf(row)
		for i := range v.NumField() {
			name := v.Type().Field(i).Tag.Get("spanner")
			if name != "" && !yield(name, v.Field(i).Interface()) {
				return
			}
		}
	}
}
