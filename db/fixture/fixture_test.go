package fixture_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/fixture"
	"github.com/justabill-org/justabill/db/schema"
)

func TestTablesParentsFirst(t *testing.T) {
	var names []string
	rows := 0
	for _, table := range fixture.Tables() {
		names = append(names, table.Name)
		rows += len(table.Rows)
	}
	want := []string{
		"congresses", "bills", "bill_status_history", "bill_text_versions", "bill_texts", "bill_summaries",
		"federal_register_documents", "bill_cra_rules", "members", "member_terms", "congressional_votes",
		"member_votes",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("tables = %q, want %q", names, want)
	}

	muts, err := fixture.Mutations()
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	if len(muts) != rows {
		t.Errorf("got %d mutations, want one per row (%d)", len(muts), rows)
	}
}

// TestBillIdentities guards the fixture's point: HR 1 and S 1 of the 119th,
// and HR 1 of the 118th, are three different bills. HR 808 and the two CRA
// resolutions are the others.
func TestBillIdentities(t *testing.T) {
	var ids []string
	for _, table := range fixture.Tables() {
		if table.Name != "bills" {
			continue
		}
		for _, row := range table.Rows {
			ids = append(ids, reflect.ValueOf(row).FieldByName("BillID").String())
		}
	}
	for _, id := range []string{
		fixture.HouseBill, fixture.SenateBill, fixture.PrevHouseBill, fixture.LawBill,
		fixture.CRABill, fixture.CRAUnmatchedBill,
	} {
		if !slices.Contains(ids, id) {
			t.Errorf("bills %q lack %s", ids, id)
		}
	}
}

// TestLawBill checks that HR 808 has what /vote and the text reader need: the
// became_law status in its history, a summary of its one text version, and a
// text whose hash is its content's (else the API reads content_gz) and whose
// sections parse.
func TestLawBill(t *testing.T) {
	rows := fixtureRows()
	field := func(v reflect.Value, name string) string { return v.FieldByName(name).String() }

	becameLaw := slices.ContainsFunc(rows["bill_status_history"], func(h reflect.Value) bool {
		return field(h, "BillID") == fixture.LawBill && field(h, "Status") == "became_law"
	})
	if !becameLaw {
		t.Errorf("bill_status_history has no became_law row for %s", fixture.LawBill)
	}

	summary, text := only(t, rows, "bill_summaries"), only(t, rows, "bill_texts")
	if field(summary, "BillID") != fixture.LawBill || field(summary, "SourceVersionID") != fixture.LawTextVersion {
		t.Errorf("summary is of %s version %s, want %s version %s", field(summary, "BillID"),
			field(summary, "SourceVersionID"), fixture.LawBill, fixture.LawTextVersion)
	}
	if got := field(text, "VersionID"); got != fixture.LawTextVersion {
		t.Errorf("text of version %s, want %s", got, fixture.LawTextVersion)
	}
	sum := sha256.Sum256([]byte(field(text, "Content")))
	if got, want := field(text, "ContentHash"), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("content_hash = %s, want the content's SHA-256 %s", got, want)
	}
	if hash := field(summary, "SourceContentHash"); hash != field(text, "ContentHash") {
		t.Errorf("summary's source_content_hash = %s, want the text's", hash)
	}
	checkSections(t, text.FieldByName("Sections"))
}

// TestCRARules checks that the two CRA resolutions show the rule card both
// ways: SJRes 41's rule matched by its citation to a Federal Register document
// that's in the fixture, and HJRes 63's unmatched, named by a GAO opinion.
func TestCRARules(t *testing.T) {
	rows := fixtureRows()
	doc := only(t, rows, "federal_register_documents")
	if got := doc.FieldByName("DocumentNumber").String(); got != fixture.CRADocument {
		t.Errorf("federal_register_documents holds %s, want %s", got, fixture.CRADocument)
	}

	type rule struct {
		status, method, reason, document string
		gao                              bool
	}
	nullString := func(v reflect.Value, name string) string {
		s, _ := reflect.TypeAssert[spanner.NullString](v.FieldByName(name))
		return s.StringVal
	}
	got := map[string]rule{}
	for _, r := range rows["bill_cra_rules"] {
		got[r.FieldByName("BillID").String()] = rule{
			status: r.FieldByName("Status").String(), method: nullString(r, "Method"),
			reason: nullString(r, "Reason"), document: nullString(r, "DocumentNumber"),
			gao: r.FieldByName("GAOOpinion").Bool(),
		}
	}
	want := map[string]rule{
		fixture.CRABill:          {status: "matched", method: "citation", document: fixture.CRADocument},
		fixture.CRAUnmatchedBill: {status: "unmatched", reason: "no_candidates", gao: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bill_cra_rules = %+v, want %+v", got, want)
	}
}

// fixtureRows returns the fixture's rows by table.
func fixtureRows() map[string][]reflect.Value {
	rows := map[string][]reflect.Value{}
	for _, table := range fixture.Tables() {
		for _, row := range table.Rows {
			rows[table.Name] = append(rows[table.Name], reflect.ValueOf(row))
		}
	}
	return rows
}

// only returns a table's one row, or fails the test.
func only(t *testing.T, rows map[string][]reflect.Value, table string) reflect.Value {
	t.Helper()
	if n := len(rows[table]); n != 1 {
		t.Fatalf("got %d %s rows, want 1", n, table)
	}
	return rows[table][0]
}

// checkSections checks that a text's sections hold a unit with children.
func checkSections(t *testing.T, v reflect.Value) {
	t.Helper()
	sections, ok := reflect.TypeAssert[spanner.NullJSON](v)
	if !ok || !sections.Valid {
		t.Fatal("text has no sections")
	}
	raw, ok := sections.Value.(json.RawMessage)
	if !ok {
		t.Fatalf("sections hold %T, want json.RawMessage", sections.Value)
	}
	var parsed []struct {
		Children []json.RawMessage `json:"children"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed) == 0 || len(parsed[0].Children) == 0 {
		t.Errorf("sections = %s (%v), want a unit with children", raw, err)
	}
}

// TestUpgradeTablesCoverSchema checks that every table in db/schema.sql is
// either seeded for the migration upgrade test or listed, with a reason, as
// not seeded, so a new table can't be left out of that test silently.
func TestUpgradeTablesCoverSchema(t *testing.T) {
	ddl, err := schema.Read()
	if err != nil {
		t.Fatal(err)
	}
	createTable := regexp.MustCompile(`^CREATE TABLE (\w+)`)
	var tables []string
	for _, stmt := range ddl {
		if m := createTable.FindStringSubmatch(stmt); m != nil {
			tables = append(tables, m[1])
		}
	}
	if len(tables) < 40 {
		t.Fatalf("found %d tables in schema.sql, want every CREATE TABLE (40 or more)", len(tables))
	}

	seeded := map[string]bool{}
	for _, table := range fixture.UpgradeTables() {
		if len(table.Rows) > 0 {
			seeded[table.Name] = true
		}
	}
	unseeded := fixture.UnseededTables()
	for _, name := range tables {
		reason, skipped := unseeded[name]
		switch {
		case seeded[name] && skipped:
			t.Errorf("%s is seeded and also listed as not seeded (%q)", name, reason)
		case !seeded[name] && !skipped:
			t.Errorf("%s is in schema.sql but neither seeded by UpgradeTables nor listed in UnseededTables", name)
		case skipped && strings.TrimSpace(reason) == "":
			t.Errorf("%s is listed as not seeded with no reason", name)
		}
	}
	for name := range unseeded {
		if !slices.Contains(tables, name) {
			t.Errorf("UnseededTables lists %s, which isn't in schema.sql", name)
		}
	}
	for name := range seeded {
		if !slices.Contains(tables, name) {
			t.Errorf("UpgradeTables seeds %s, which isn't in schema.sql", name)
		}
	}
}

// TestUpgradeTablesLegacyUserVote checks that the upgrade seed holds a vote
// written before migration 21, with no recorded_at or App Check result, next
// to one written since, so a migration that requires either fails the test.
func TestUpgradeTablesLegacyUserVote(t *testing.T) {
	var legacy, current int
	for _, table := range fixture.UpgradeTables() {
		if table.Name != "user_votes" {
			continue
		}
		for _, row := range table.Rows {
			v := reflect.ValueOf(row)
			recorded, _ := reflect.TypeAssert[spanner.NullTime](v.FieldByName("RecordedAt"))
			appCheck, _ := reflect.TypeAssert[spanner.NullBool](v.FieldByName("AppCheckOK"))
			switch {
			case !recorded.Valid && !appCheck.Valid:
				legacy++
			case recorded.Valid && appCheck.Valid:
				current++
			}
		}
	}
	if legacy == 0 || current == 0 {
		t.Errorf("user_votes has %d legacy rows (NULL recorded_at and app_check_ok) and %d current ones, want both",
			legacy, current)
	}
}

// TestUpgradeTablesStartWithTables checks that the upgrade seed is the
// shared fixture plus more, so the upgrade test still covers every row
// SeedFixture writes.
func TestUpgradeTablesStartWithTables(t *testing.T) {
	base, upgrade := fixture.Tables(), fixture.UpgradeTables()
	if len(upgrade) <= len(base) || !reflect.DeepEqual(upgrade[:len(base)], base) {
		t.Errorf("UpgradeTables doesn't start with Tables' %d tables", len(base))
	}
}
