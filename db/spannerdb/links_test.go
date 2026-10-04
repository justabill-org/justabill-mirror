package spannerdb_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// unsyncedMember and unsyncedBill are link endpoints that are not in the
// database; the NOT ENFORCED FKs must accept them.
const (
	unsyncedMember = "Z999999"
	unsyncedBill   = "hr-119-9999"
)

func newLinkStore(t *testing.T) (*spannerdb.PipelineStoreImpl, *spanner.Client) {
	t.Helper()
	client := testdb.New(t)
	testdb.SeedFixture(t.Context(), t, client)
	return spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: client}), client
}

// queryStrings runs a query whose rows are STRING columns and joins each row
// with "|", so results compare as plain []string.
func queryStrings(t *testing.T, client *spanner.Client, sql string, params map[string]any) []string {
	t.Helper()
	iter := client.Single().Query(t.Context(), spanner.Statement{SQL: sql, Params: params})
	defer iter.Stop()
	var out []string
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out
		}
		if err != nil {
			t.Fatalf("query %q: %v", sql, err)
		}
		cols := make([]string, row.Size())
		for i := range cols {
			var v spanner.NullString
			if colErr := row.Column(i, &v); colErr != nil {
				t.Fatalf("column %d: %v", i, colErr)
			}
			cols[i] = v.StringVal
		}
		out = append(out, strings.Join(cols, "|"))
	}
}

func assertRows(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
}

func TestReplaceBillSponsorships(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	bill := testdb.FixtureHouseBill
	date := time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC)
	const query = `SELECT member_id, role, CAST(sponsored_date AS STRING), CAST(is_original AS STRING)
		FROM bill_sponsorships WHERE bill_id = @bill ORDER BY member_id`
	params := map[string]any{"bill": bill}

	if err := store.ReplaceBillSponsorships(ctx, bill, repository.SponsorRoleSponsor,
		[]repository.BillSponsorshipRow{{MemberID: testdb.FixtureHouseDem, SponsoredDate: &date}},
	); err != nil {
		t.Fatalf("replace sponsors: %v", err)
	}
	if err := store.ReplaceBillSponsorships(ctx, bill, repository.SponsorRoleCosponsor,
		[]repository.BillSponsorshipRow{
			{MemberID: testdb.FixtureHouseRep, SponsoredDate: &date, IsOriginal: true},
			{MemberID: unsyncedMember},
		},
	); err != nil {
		t.Fatalf("replace cosponsors: %v", err)
	}
	assertRows(t, queryStrings(t, client, query, params), []string{
		testdb.FixtureHouseDem + "|sponsor|2025-01-03|",
		testdb.FixtureHouseRep + "|cosponsor|2025-01-03|true",
		unsyncedMember + "|cosponsor||false",
	})

	// Replacing one role leaves the other alone.
	if err := store.ReplaceBillSponsorships(ctx, bill, repository.SponsorRoleCosponsor, nil); err != nil {
		t.Fatalf("clear cosponsors: %v", err)
	}
	assertRows(t, queryStrings(t, client, query, params), []string{testdb.FixtureHouseDem + "|sponsor|2025-01-03|"})

	// Reverse traversal: bills by member, through idx_bill_sponsorships_member.
	assertRows(t, queryStrings(t, client,
		`SELECT bill_id FROM bill_sponsorships@{FORCE_INDEX=idx_bill_sponsorships_member} WHERE member_id = @m`,
		map[string]any{"m": testdb.FixtureHouseDem}), []string{bill})

	if err := store.ReplaceBillSponsorships(ctx, bill, "author", nil); err == nil {
		t.Error("invalid role: want error")
	}
	if err := store.ReplaceBillSponsorships(ctx, bill, repository.SponsorRoleSponsor,
		[]repository.BillSponsorshipRow{{}}); err == nil {
		t.Error("empty member id: want error")
	}
}

func TestReplaceBillCommittees(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	bill := testdb.FixtureHouseBill
	house, standing := "House", "Standing"
	referred := time.Date(2025, time.January, 3, 15, 0, 0, 0, time.UTC)
	const links = `SELECT committee_id, activity FROM bill_committees WHERE bill_id = @bill
		ORDER BY committee_id, activity`
	params := map[string]any{"bill": bill}

	if err := store.ReplaceBillCommittees(ctx, bill, []repository.BillCommitteeRow{
		{
			CommitteeID: "HSWM00", CommitteeName: "Ways and Means Committee", Chamber: &house,
			CommitteeType: &standing, Activity: "Referred To", ActivityDate: &referred,
		},
		{CommitteeID: "hswm00", CommitteeName: "Ways and Means Committee", Activity: "Markup By"},
	}); err != nil {
		t.Fatalf("replace committees: %v", err)
	}
	assertRows(t, queryStrings(t, client, links, params), []string{"hswm00|Markup By", "hswm00|Referred To"})
	assertRows(t, queryStrings(t, client,
		`SELECT committee_id, name, chamber FROM committees`, nil),
		[]string{"hswm00|Ways and Means Committee|House"})

	// A re-sync replaces the links; the committee node stays for other bills.
	if err := store.ReplaceBillCommittees(ctx, bill, []repository.BillCommitteeRow{
		{CommitteeID: "hsju00", CommitteeName: "Judiciary Committee", Activity: "Referred To"},
	}); err != nil {
		t.Fatalf("re-replace committees: %v", err)
	}
	assertRows(t, queryStrings(t, client, links, params), []string{"hsju00|Referred To"})
	assertRows(t, queryStrings(t, client,
		`SELECT committee_id FROM committees ORDER BY committee_id`, nil), []string{"hsju00", "hswm00"})
	assertRows(t, queryStrings(t, client,
		`SELECT bill_id FROM bill_committees WHERE committee_id = @c`,
		map[string]any{"c": "hsju00"}), []string{bill})

	if err := store.ReplaceBillCommittees(ctx, bill, []repository.BillCommitteeRow{{CommitteeName: "x"}}); err == nil {
		t.Error("empty committee id: want error")
	}
}

func TestReplaceBillSubjects(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	const bySubject = `SELECT bill_id FROM bill_subjects@{FORCE_INDEX=idx_bill_subjects_subject}
		WHERE subject_id = @s ORDER BY bill_id`

	// Duplicates by slug and blank names collapse instead of failing the write.
	if err := store.ReplaceBillSubjects(ctx, testdb.FixtureHouseBill,
		[]string{"Taxation", "Income tax credits", "taxation", " -- "}); err != nil {
		t.Fatalf("replace house subjects: %v", err)
	}
	if err := store.ReplaceBillSubjects(ctx, testdb.FixtureSenateBill, []string{"Taxation"}); err != nil {
		t.Fatalf("replace senate subjects: %v", err)
	}
	assertRows(t, queryStrings(t, client,
		`SELECT subject_id FROM bill_subjects WHERE bill_id = @b ORDER BY subject_id`,
		map[string]any{"b": testdb.FixtureHouseBill}), []string{"income-tax-credits", "taxation"})
	assertRows(t, queryStrings(t, client, bySubject, map[string]any{"s": "taxation"}),
		[]string{testdb.FixtureHouseBill, testdb.FixtureSenateBill})

	// Re-syncing one bill doesn't touch the other bill's links.
	if err := store.ReplaceBillSubjects(ctx, testdb.FixtureHouseBill, nil); err != nil {
		t.Fatalf("clear house subjects: %v", err)
	}
	assertRows(t, queryStrings(t, client, bySubject, map[string]any{"s": "taxation"}),
		[]string{testdb.FixtureSenateBill})
}

func TestReplaceBillRelations(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	crs := "CRS"
	const byRelated = `SELECT bill_id, relation_type FROM bill_relations@{FORCE_INDEX=idx_bill_relations_related}
		WHERE related_bill_id = @r ORDER BY bill_id`

	if err := store.ReplaceBillRelations(ctx, testdb.FixtureHouseBill, []repository.BillRelationRow{
		{RelatedBillID: testdb.FixtureSenateBill, RelationType: "Identical bill", IdentifiedBy: &crs},
		{RelatedBillID: testdb.FixturePrevHouseBill, RelationType: "Related bill"},
		{RelatedBillID: unsyncedBill, RelationType: "Related bill"},
	}); err != nil {
		t.Fatalf("replace relations: %v", err)
	}
	assertRows(t, queryStrings(t, client,
		`SELECT related_bill_id, relation_type, identified_by FROM bill_relations WHERE bill_id = @b
		ORDER BY related_bill_id`, map[string]any{"b": testdb.FixtureHouseBill}),
		[]string{
			testdb.FixturePrevHouseBill + "|Related bill|",
			unsyncedBill + "|Related bill|",
			testdb.FixtureSenateBill + "|Identical bill|CRS",
		})
	assertRows(t, queryStrings(t, client, byRelated, map[string]any{"r": testdb.FixtureSenateBill}),
		[]string{testdb.FixtureHouseBill + "|Identical bill"})

	if err := store.ReplaceBillRelations(ctx, testdb.FixtureHouseBill, []repository.BillRelationRow{
		{RelatedBillID: testdb.FixtureSenateBill, RelationType: "Identical bill"},
	}); err != nil {
		t.Fatalf("re-replace relations: %v", err)
	}
	assertRows(t, queryStrings(t, client, byRelated, map[string]any{"r": unsyncedBill}), nil)

	if err := store.ReplaceBillRelations(ctx, testdb.FixtureHouseBill,
		[]repository.BillRelationRow{{RelationType: "Related bill"}}); err == nil {
		t.Error("empty related bill id: want error")
	}
}

func TestLinksCascadeWithBill(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	bill := testdb.FixturePrevHouseBill

	if err := store.ReplaceBillSubjects(ctx, bill, []string{"Taxation"}); err != nil {
		t.Fatalf("replace subjects: %v", err)
	}
	if err := store.ReplaceBillRelations(ctx, bill, []repository.BillRelationRow{
		{RelatedBillID: testdb.FixtureHouseBill, RelationType: "Related bill"},
	}); err != nil {
		t.Fatalf("replace relations: %v", err)
	}
	if _, err := client.Apply(ctx, []*spanner.Mutation{spanner.Delete("bills", spanner.Key{bill})}); err != nil {
		t.Fatalf("delete bill: %v", err)
	}
	for _, table := range []string{"bill_subjects", "bill_relations"} {
		assertRows(t, queryStrings(t, client,
			"SELECT bill_id FROM "+table+" WHERE bill_id = @b", map[string]any{"b": bill}), nil)
	}
}

func TestListBillLinkSources(t *testing.T) {
	store, _ := newLinkStore(t)
	ctx := t.Context()

	// In bill_id order, the fixture's H.J.Res. 63 comes first.
	first, err := store.ListBillLinkSources(ctx, testdb.FixtureCongress, "", 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 || first[0].BillID != testdb.FixtureCRAUnmatched || first[1].BillID != testdb.FixtureHouseBill {
		t.Fatalf("first page = %+v, want %s and %s", first, testdb.FixtureCRAUnmatched, testdb.FixtureHouseBill)
	}
	hr := first[1]
	if hr.IntroducedDate == nil || hr.IntroducedDate.Format(time.DateOnly) != "2025-01-03" {
		t.Errorf("introduced date = %v, want 2025-01-03", hr.IntroducedDate)
	}
	for name, col := range map[string][]byte{
		"sponsors": hr.Sponsors, "cosponsors": hr.Cosponsors, "committees": hr.Committees,
		"subjects": hr.Subjects, "related_bills": hr.RelatedBills,
	} {
		if len(col) == 0 {
			t.Errorf("%s is empty, want the fixture JSON", name)
		}
	}

	rest, err := store.ListBillLinkSources(ctx, testdb.FixtureCongress, hr.BillID, 10)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	wantRest := []string{testdb.FixtureLawBill, testdb.FixtureSenateBill, testdb.FixtureCRABill}
	if ids := linkSourceIDs(rest); !slices.Equal(ids, wantRest) {
		t.Fatalf("second page = %v, want only %v (not the 118th's HR 1)", ids, wantRest)
	}
	if s1 := rest[1]; s1.Cosponsors != nil || s1.Committees != nil {
		t.Errorf(
			"S 1 cosponsors/committees = %s / %s, want nil for NULL columns",
			s1.Cosponsors,
			s1.Committees,
		)
	}

	done, err := store.ListBillLinkSources(ctx, testdb.FixtureCongress, rest[2].BillID, 10)
	if err != nil || len(done) != 0 {
		t.Fatalf("last page = %+v, %v; want empty", done, err)
	}
}

// linkSourceIDs returns the bill IDs of sources, in order.
func linkSourceIDs(sources []repository.BillLinkSource) []string {
	ids := make([]string, 0, len(sources))
	for _, s := range sources {
		ids = append(ids, s.BillID)
	}
	return ids
}
