package fixture_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/fixture"
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
