package spannerdb_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const (
	secPhysicians = "/us/usc/t42/s1395w-4"
	secMedicare   = "/us/usc/t42/s1395"
	secFlag       = "/us/usc/t4/s1"
	secNotLoaded  = "/us/usc/t16/s460qqq-1"
	secSSA        = model.NonUSCSectionPrefix + "Section 5 of the Social Security Act"
	releasePoint  = "119-111"
	versionIH     = "00000000-0000-0000-0000-00000000000a"
	versionRH     = "00000000-0000-0000-0000-00000000000b"
	citesOnlyBill = "hr-119-2"
	newestBill    = "hr-119-3"
)

// lawFixture is the SeedFixture world plus loaded US Code sections and law references:
//   - HR 1 (119th), version IH: amends 42 U.S.C. 1395w-4(t), cites 42 U.S.C. 1395, amends a
//     section that isn't loaded and a law named without a US Code citation. Version RH: amends 4
//     U.S.C. 1.
//   - S 1 (119th) and HR 3 (119th, newest): amend 42 U.S.C. 1395w-4. HR 2 (119th) only cites it.
//   - HR 1 (118th) amends it too, in another congress.
type lawFixture struct {
	store *spannerdb.PipelineStoreImpl
	graph *spannerdb.GraphRepository
	law   *spannerdb.LawRepository
}

func newLawFixture(t *testing.T) lawFixture {
	t.Helper()
	store, client := newLinkStore(t)
	ctx := t.Context()
	testdb.SeedBill(ctx, t, client, citesOnlyBill, testdb.FixtureCongress, "hr", 2, "Cites Only Act")
	testdb.SeedBill(ctx, t, client, newestBill, testdb.FixtureCongress, "hr", 3, "Newest Act")

	sections := []repository.USCSectionRow{
		uscRow(secPhysicians, 42, "1395w-4", "Payment for physicians' services"),
		uscRow(secMedicare, 42, "1395", "Prohibition against any Federal interference"),
		uscRow(secFlag, 4, "1", "Flag; stripes and stars on"),
	}
	if err := store.UpsertUSCSections(ctx, sections); err != nil {
		t.Fatalf("UpsertUSCSections: %v", err)
	}

	refs := []struct {
		bill, version string
		rows          []repository.BillLawRefRow
	}{
		{testdb.FixtureHouseBill, versionIH, []repository.BillLawRefRow{
			{SectionID: secPhysicians, RefKind: model.LawRefAmends, CiteText: new("42 U.S.C. 1395w–4(t)"),
				SubsectionPath: new("(t)"), Instruction: new(`is amended by striking "2025"`),
				BillSectionRef: new("H1B2C3")},
			{SectionID: secMedicare, RefKind: model.LawRefCites},
			{SectionID: secNotLoaded, RefKind: model.LawRefAmends},
			{SectionID: secSSA, RefKind: model.LawRefAmends},
		}},
		{testdb.FixtureHouseBill, versionRH, []repository.BillLawRefRow{
			{SectionID: secFlag, RefKind: model.LawRefAmends},
		}},
		{testdb.FixtureSenateBill, versionIH, []repository.BillLawRefRow{
			{SectionID: secPhysicians, RefKind: model.LawRefAmends},
			{SectionID: secPhysicians, RefKind: model.LawRefCites},
		}},
		{newestBill, versionIH, []repository.BillLawRefRow{{SectionID: secPhysicians, RefKind: model.LawRefRepeals}}},
		{citesOnlyBill, versionIH, []repository.BillLawRefRow{{SectionID: secPhysicians, RefKind: model.LawRefCites}}},
		{testdb.FixturePrevHouseBill, versionIH, []repository.BillLawRefRow{
			{SectionID: secPhysicians, RefKind: model.LawRefAmends},
		}},
	}
	for _, r := range refs {
		if err := store.ReplaceBillLawRefs(ctx, r.bill, r.version, r.rows); err != nil {
			t.Fatalf("ReplaceBillLawRefs %s %s: %v", r.bill, r.version, err)
		}
	}
	c := &spannerdb.Client{Spanner: client}
	return lawFixture{store: store, graph: spannerdb.NewGraphRepo(c), law: spannerdb.NewLawRepo(c)}
}

func uscRow(id string, title int, number, heading string) repository.USCSectionRow {
	return repository.USCSectionRow{
		SectionID: id, TitleNumber: title, SectionNumber: number, Heading: &heading,
		Text: "(a) In general.—" + heading, Status: "current", PositiveLaw: title == 4,
		ReleasePoint: releasePoint, ContentHash: "hash-" + id,
	}
}

func sectionBillIDs(bills []model.SectionBill) []string {
	ids := make([]string, len(bills))
	for i, b := range bills {
		ids[i] = b.BillID
	}
	return ids
}

func TestBillsChangingSection(t *testing.T) {
	f := newLawFixture(t)
	ctx := t.Context()

	got, err := f.graph.BillsChangingSection(ctx, secPhysicians, testdb.FixtureHouseBill, testdb.FixtureCongress, 0)
	if err != nil {
		t.Fatalf("BillsChangingSection: %v", err)
	}
	// Newest first; HR 2 only cites the section, and the 118th's HR 1 is another congress.
	if ids := sectionBillIDs(got); !reflect.DeepEqual(ids, []string{newestBill, testdb.FixtureSenateBill}) {
		t.Fatalf("bills = %v, want [%s %s]", ids, newestBill, testdb.FixtureSenateBill)
	}
	senate := got[1]
	if !reflect.DeepEqual(senate.RefKinds, []string{model.LawRefAmends}) || senate.Congress != testdb.FixtureCongress ||
		senate.BillType != "s" || senate.Number != 1 || senate.Title == "" {
		t.Errorf("S 1 = %+v, want amends only, with the bill's fields", senate)
	}

	all, err := f.graph.BillsChangingSection(ctx, secPhysicians, "", testdb.FixtureCongress, 0)
	if err != nil {
		t.Fatalf("BillsChangingSection without exclusion: %v", err)
	}
	want := []string{newestBill, testdb.FixtureHouseBill, testdb.FixtureSenateBill}
	if ids := sectionBillIDs(all); !reflect.DeepEqual(ids, want) {
		t.Errorf("bills = %v, want %v", ids, want)
	}

	one, err := f.graph.BillsChangingSection(ctx, secPhysicians, "", testdb.FixtureCongress, 1)
	if err != nil || len(one) != 1 {
		t.Errorf("limit 1: got %d bills, err %v; want 1", len(one), err)
	}

	none, err := f.graph.BillsChangingSection(ctx, secNotLoaded, "", testdb.FixtureCongress, 0)
	if err != nil || len(none) != 0 {
		t.Errorf("section that isn't loaded: got %v, err %v; want an empty list", none, err)
	}
}

func TestLawChangedByBill(t *testing.T) {
	f := newLawFixture(t)

	got, err := f.graph.LawChangedByBill(t.Context(), testdb.FixtureHouseBill, 0)
	if err != nil {
		t.Fatalf("LawChangedByBill: %v", err)
	}
	// The section that isn't loaded and the "nonusc:" reference have no LawSection, so no edge.
	type edge struct{ version, section, kind string }
	var edges []edge
	for _, r := range got {
		edges = append(edges, edge{r.VersionID, r.SectionID, r.RefKind})
	}
	want := []edge{
		{versionIH, secMedicare, model.LawRefCites},
		{versionIH, secPhysicians, model.LawRefAmends},
		{versionRH, secFlag, model.LawRefAmends},
	}
	if !reflect.DeepEqual(edges, want) {
		t.Fatalf("edges = %v, want %v", edges, want)
	}
	amend := got[1]
	if amend.SubsectionPath == nil || *amend.SubsectionPath != "(t)" || amend.TitleNumber != 42 ||
		amend.SectionNumber != "1395w-4" || amend.Heading == nil || *amend.Heading != "Payment for physicians' services" {
		t.Errorf("amendment edge = %+v, want (t) of 42 U.S.C. 1395w-4 with its heading", amend)
	}

	one, err := f.graph.LawChangedByBill(t.Context(), testdb.FixtureHouseBill, 1)
	if err != nil || len(one) != 1 {
		t.Errorf("limit 1: got %d edges, err %v; want 1", len(one), err)
	}
}

func TestReplaceBillLawRefs(t *testing.T) {
	f := newLawFixture(t)
	ctx := t.Context()

	rows := []repository.BillLawRefRow{{SectionID: secMedicare, RefKind: model.LawRefAmends}}
	if err := f.store.ReplaceBillLawRefs(ctx, testdb.FixtureHouseBill, versionIH, rows); err != nil {
		t.Fatalf("ReplaceBillLawRefs: %v", err)
	}
	got, err := f.graph.LawChangedByBill(ctx, testdb.FixtureHouseBill, 0)
	if err != nil {
		t.Fatalf("LawChangedByBill: %v", err)
	}
	// Only IH's references were replaced; RH keeps its edge.
	var sections []string
	for _, r := range got {
		sections = append(sections, r.VersionID+" "+r.SectionID+" "+r.RefKind)
	}
	want := []string{versionIH + " " + secMedicare + " amends", versionRH + " " + secFlag + " amends"}
	if !reflect.DeepEqual(sections, want) {
		t.Errorf("edges after replace = %v, want %v", sections, want)
	}

	cite := func(section string) repository.BillLawRefRow {
		return repository.BillLawRefRow{SectionID: section, RefKind: model.LawRefCites}
	}
	bad := []struct {
		name, bill, version string
		row                 repository.BillLawRefRow
	}{
		{
			"unknown kind",
			testdb.FixtureHouseBill,
			versionIH,
			repository.BillLawRefRow{SectionID: secFlag, RefKind: "x"},
		},
		{"empty section", testdb.FixtureHouseBill, versionIH, cite("")},
		{"empty version", testdb.FixtureHouseBill, "", cite(secFlag)},
		{"empty bill", "", versionIH, cite(secFlag)},
	}
	for _, tc := range bad {
		if replaceErr := f.store.ReplaceBillLawRefs(
			ctx, tc.bill, tc.version, []repository.BillLawRefRow{tc.row},
		); replaceErr == nil {
			t.Errorf("%s: ReplaceBillLawRefs succeeded, want an error", tc.name)
		}
	}
}

func TestBillLawChanges(t *testing.T) {
	f := newLawFixture(t)
	ctx := t.Context()

	empty, err := f.law.BillLawChanges(ctx, testdb.FixtureHouseBill)
	if err != nil || len(empty) != 0 {
		t.Fatalf("before any explanation: got %v, err %v; want an empty list", empty, err)
	}

	change := func(section, kind string) repository.BillLawChangeRow {
		return repository.BillLawChangeRow{
			SectionID: section, ChangeKind: kind, Explanation: "Moves the deadline from 2025 to 2026.",
			SourceContentHash: "text-hash", ReleasePoint: releasePoint, ModelUsed: "gemini-3.8-flash",
			PromptVersion: "law-v1",
		}
	}
	before := time.Now().Add(-time.Minute)
	rows := []repository.BillLawChangeRow{
		change(secPhysicians, model.LawRefAmends),
		change(secSSA, model.LawRefAmends),
	}
	if err = f.store.ReplaceBillLawChanges(ctx, testdb.FixtureHouseBill, rows); err != nil {
		t.Fatalf("ReplaceBillLawChanges: %v", err)
	}
	got, err := f.law.BillLawChanges(ctx, testdb.FixtureHouseBill)
	if err != nil {
		t.Fatalf("BillLawChanges: %v", err)
	}
	if len(got) != 2 || got[0].SectionID != secPhysicians || got[1].SectionID != secSSA {
		t.Fatalf("changes = %+v, want %s then %s", got, secPhysicians, secSSA)
	}
	c := got[0]
	if c.Heading == nil || *c.Heading != "Payment for physicians' services" || c.ChangeKind != model.LawRefAmends ||
		c.Explanation == "" || c.ReleasePoint != releasePoint || c.ModelUsed != "gemini-3.8-flash" ||
		c.PromptVersion != "law-v1" || c.GeneratedAt.Before(before) {
		t.Errorf("change = %+v, want the row with its section heading and a commit timestamp", c)
	}
	if got[1].Heading != nil {
		t.Errorf("nonusc change heading = %q, want nil", *got[1].Heading)
	}

	if err = f.store.ReplaceBillLawChanges(ctx, testdb.FixtureHouseBill, rows[:1]); err != nil {
		t.Fatalf("ReplaceBillLawChanges again: %v", err)
	}
	if got, err = f.law.BillLawChanges(ctx, testdb.FixtureHouseBill); err != nil || len(got) != 1 {
		t.Errorf("after replacing with one row: got %d, err %v; want 1", len(got), err)
	}

	for _, bad := range []repository.BillLawChangeRow{change(secFlag, model.LawRefCites), change("", model.LawRefAdds)} {
		if err = f.store.ReplaceBillLawChanges(
			ctx,
			testdb.FixtureHouseBill,
			[]repository.BillLawChangeRow{bad},
		); err == nil {
			t.Errorf("ReplaceBillLawChanges(%q, %q) succeeded, want an error", bad.SectionID, bad.ChangeKind)
		}
	}
	if err = f.store.ReplaceBillLawChanges(ctx, "", rows); err == nil {
		t.Error("ReplaceBillLawChanges with no bill succeeded, want an error")
	}
}

func TestUSCSections(t *testing.T) {
	f := newLawFixture(t)
	ctx := t.Context()

	got, err := f.law.Section(ctx, secPhysicians)
	if err != nil || got == nil {
		t.Fatalf("Section: %+v, %v", got, err)
	}
	if got.TitleNumber != 42 || got.SectionNumber != "1395w-4" || got.Heading == nil || got.Status != "current" ||
		got.PositiveLaw || got.ReleasePoint != releasePoint || got.Text == "" || got.UpdatedAt.IsZero() {
		t.Errorf("section = %+v, want the loaded row", got)
	}
	if missing, missErr := f.law.Section(ctx, secNotLoaded); missErr != nil || missing != nil {
		t.Errorf("Section(not loaded) = %+v, %v; want nil, nil", missing, missErr)
	}

	// Title 4's key range must not reach title 42's sections, or the reverse.
	hashes42, err := f.store.USCSectionHashes(ctx, 42)
	if err != nil {
		t.Fatalf("USCSectionHashes(42): %v", err)
	}
	want42 := map[string]string{secPhysicians: "hash-" + secPhysicians, secMedicare: "hash-" + secMedicare}
	if !reflect.DeepEqual(hashes42, want42) {
		t.Errorf("title 42 hashes = %v, want %v", hashes42, want42)
	}
	hashes4, err := f.store.USCSectionHashes(ctx, 4)
	if err != nil || !reflect.DeepEqual(hashes4, map[string]string{secFlag: "hash-" + secFlag}) {
		t.Errorf("title 4 hashes = %v, %v; want only %s", hashes4, err, secFlag)
	}

	if err = f.store.UpsertUSCSections(ctx, []repository.USCSectionRow{{TitleNumber: 1}}); err == nil {
		t.Error("UpsertUSCSections with no section ID succeeded, want an error")
	}
}

func TestUpsertUSCSectionsBatches(t *testing.T) {
	f := newLawFixture(t)
	ctx := t.Context()

	// More rows than one commit takes, so the writer has to split them.
	const n = 1201
	rows := make([]repository.USCSectionRow, n)
	for i := range rows {
		num := strconv.Itoa(i + 1)
		rows[i] = uscRow("/us/usc/t50/s"+num, 50, num, "Section "+num)
	}
	if err := f.store.UpsertUSCSections(ctx, rows); err != nil {
		t.Fatalf("UpsertUSCSections: %v", err)
	}
	hashes, err := f.store.USCSectionHashes(ctx, 50)
	if err != nil || len(hashes) != n {
		t.Errorf("title 50 has %d sections, err %v; want %d", len(hashes), err, n)
	}
}

func TestUSCReleasePoints(t *testing.T) {
	f := newLawFixture(t)
	ctx := t.Context()

	if rp, err := f.law.CurrentReleasePoint(ctx); err != nil || rp != nil {
		t.Fatalf("before any load: %+v, %v; want nil, nil", rp, err)
	}
	published := time.Date(2026, time.September, 24, 0, 0, 0, 0, time.UTC)
	loaded := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	points := []repository.USCReleasePointRow{
		{ReleasePoint: "119-60", SourceURL: "https://uscode.house.gov/a.zip", LoadedAt: loaded.AddDate(0, -1, 0),
			SectionCount: 60000},
		{ReleasePoint: releasePoint, PublishedDate: &published, SourceURL: "https://uscode.house.gov/b.zip",
			LoadedAt: loaded, SectionCount: 60123},
		// Recorded but never loaded: not current.
		{ReleasePoint: "119-112", SourceURL: "https://uscode.house.gov/c.zip"},
	}
	for _, p := range points {
		if err := f.store.RecordUSCReleasePoint(ctx, p); err != nil {
			t.Fatalf("RecordUSCReleasePoint %s: %v", p.ReleasePoint, err)
		}
	}
	for name, get := range map[string]func() (*model.USCReleasePoint, error){
		"LawRepo":       func() (*model.USCReleasePoint, error) { return f.law.CurrentReleasePoint(ctx) },
		"PipelineStore": func() (*model.USCReleasePoint, error) { return f.store.CurrentUSCReleasePoint(ctx) },
	} {
		rp, err := get()
		if err != nil || rp == nil {
			t.Fatalf("%s: %+v, %v", name, rp, err)
		}
		if rp.ReleasePoint != releasePoint || rp.PublishedDate == nil || !rp.PublishedDate.Equal(published) ||
			rp.LoadedAt == nil || !rp.LoadedAt.Equal(loaded) || rp.SectionCount == nil || *rp.SectionCount != 60123 ||
			rp.SourceURL != "https://uscode.house.gov/b.zip" {
			t.Errorf("%s current release point = %+v, want %s", name, rp, releasePoint)
		}
	}
}

func TestListLawRefSources(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	// Two fetched versions of HR 1, one of S 1, one unfetched version of S 1, and a fetched
	// version of the 118th congress's HR 1, which another congress's page leaves out.
	upsertVersions(t, store, testdb.FixtureHouseBill,
		textVersion(testdb.FixtureHouseBill, typeIntroduced, "ih", 1),
		textVersion(testdb.FixtureHouseBill, typeReported, "rh", 2))
	upsertVersions(t, store, testdb.FixtureSenateBill,
		textVersion(testdb.FixtureSenateBill, "Introduced in Senate", "is", 1),
		textVersion(testdb.FixtureSenateBill, "Reported in Senate", "rs", 2))
	upsertVersions(t, store, testdb.FixturePrevHouseBill,
		textVersion(testdb.FixturePrevHouseBill, typeIntroduced, "ih", 1))
	house := versionIDs(t, client, testdb.FixtureHouseBill)
	senate := versionIDs(t, client, testdb.FixtureSenateBill)
	prev := versionIDs(t, client, testdb.FixturePrevHouseBill)
	for _, id := range []string{house["ih"], house["rh"], senate["is"], prev["ih"]} {
		if err := store.InsertBillText(ctx, repository.BillTextRow{
			TextVersionID: id, Format: "Formatted XML", Content: "<bill>" + id + "</bill>", ContentHash: "h-" + id,
			FetchedAt: time.Now(),
		}); err != nil {
			t.Fatalf("insert text: %v", err)
		}
	}

	// Page through the congress two at a time, as the backfill does.
	var got []string
	afterBill, afterVersion := "", ""
	for range 5 {
		page, err := store.ListLawRefSources(ctx, testdb.FixtureCongress, afterBill, afterVersion, 2)
		if err != nil {
			t.Fatalf("ListLawRefSources: %v", err)
		}
		for _, src := range page {
			got = append(got, src.BillID+" "+src.VersionCode)
			if src.BillID == testdb.FixtureLawBill {
				continue // the fixture's own text
			}
			if src.Content != "<bill>"+src.VersionID+"</bill>" || src.Format != "Formatted XML" ||
				!strings.Contains(string(src.Formats), src.VersionCode+".xml") {
				t.Errorf("source %s %s = %+v", src.BillID, src.VersionCode, src)
			}
		}
		if len(page) < 2 {
			break
		}
		afterBill, afterVersion = page[len(page)-1].BillID, page[len(page)-1].VersionID
	}
	// By bill ID (hr-119-1, hr-119-808, s-119-1), then version ID.
	want := []string{
		testdb.FixtureHouseBill + " ih", testdb.FixtureHouseBill + " rh",
		testdb.FixtureLawBill + " enr", testdb.FixtureSenateBill + " is",
	}
	if house["ih"] > house["rh"] {
		want[0], want[1] = want[1], want[0]
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
}
