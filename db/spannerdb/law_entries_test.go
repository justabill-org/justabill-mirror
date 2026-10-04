package spannerdb_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// TestBillLawChangeEntries covers the API's law-changes read: HR 1's latest text (RH) amends
// 42 U.S.C. 1395w-4 twice (one of them a repeal), adds a section that isn't loaded, amends a law
// with no US Code citation and cites 4 U.S.C. 1; its older text (IH) amends 4 U.S.C. 1. Other
// bills change 1395w-4 too, one of them in another congress, and one only cites it.
func TestBillLawChangeEntries(t *testing.T) {
	store, client := newLawChangeStore(t)
	ctx := t.Context()
	law := spannerdb.NewLawRepo(&spannerdb.Client{Spanner: client})

	ids := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 1, []string{"rh", "ih"}, "rh", "ih")
	lawRefs(t, store, billID(1), ids["ih"], ref(secFlag, model.LawRefAmends))
	lawRefs(t, store, billID(1), ids["rh"],
		repository.BillLawRefRow{
			SectionID: secPhysicians, RefKind: model.LawRefAmends, CiteText: new("42 U.S.C. 1395w–4(t)"),
			SubsectionPath: new("(t)"), Instruction: new(`is amended by striking "2025"`), BillSectionRef: new("s2"),
		},
		repository.BillLawRefRow{
			SectionID: secPhysicians, RefKind: model.LawRefRepeals, CiteText: new("42 U.S.C. 1395w–4(c)"),
			SubsectionPath: new("(c), (t)"), Instruction: new("is repealed"), BillSectionRef: new("s2"),
		},
		ref(secNotLoaded, model.LawRefAdds),
		ref(secSSA, model.LawRefAmends),
		ref(secFlag, model.LawRefCites),
	)
	for n, kind := range map[int]string{2: model.LawRefAmends, 3: model.LawRefCites, 5: model.LawRefAdds} {
		other := seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 1, []string{"ih"}, "ih")
		lawRefs(t, store, billID(n), other["ih"], ref(secPhysicians, kind))
	}
	prev := seedSummaryBill(t, store, client, testdb.FixturePrevCongress, 4, 1, []string{"ih"}, "ih")
	lawRefs(t, store, "hr-118-4", prev["ih"], ref(secPhysicians, model.LawRefAmends))

	t.Run("unknown bill", func(t *testing.T) {
		got, err := law.BillLawChangeEntries(ctx, "hr-119-999", 5)
		if err != nil || got != nil {
			t.Errorf("got %+v, err %v; want nil", got, err)
		}
	})

	t.Run("no text", func(t *testing.T) {
		seedSummaryBill(t, store, client, testdb.FixtureCongress, 6, 1, []string{"ih"})
		got, err := law.BillLawChangeEntries(ctx, billID(6), 5)
		if err != nil || got == nil || got.VersionID != nil || len(got.Changes) != 0 || got.Explained != nil {
			t.Errorf("got %+v, err %v; want no version and no changes", got, err)
		}
	})

	t.Run("not explained", func(t *testing.T) {
		got, err := law.BillLawChangeEntries(ctx, billID(1), 5)
		if err != nil {
			t.Fatalf("BillLawChangeEntries: %v", err)
		}
		if got.VersionID == nil || *got.VersionID != ids["rh"] || got.VersionCode == nil || *got.VersionCode != "rh" {
			t.Errorf("version = %v %v, want the latest (rh)", got.VersionID, got.VersionCode)
		}
		checkUnexplainedEntries(t, got)
	})

	t.Run("also limit", func(t *testing.T) {
		got, err := law.BillLawChangeEntries(ctx, billID(1), 1)
		if err != nil || len(got.Changes) != 3 || len(got.Changes[2].AlsoChangedBy) != 1 {
			t.Fatalf("got %+v, err %v; want one other bill", got, err)
		}
		if got, err = law.BillLawChangeEntries(
			ctx,
			billID(1),
			0,
		); err != nil ||
			len(got.Changes[2].AlsoChangedBy) != 0 {
			t.Errorf("limit 0: got %+v, err %v; want no other bills", got, err)
		}
	})

	t.Run("explained", func(t *testing.T) { checkExplainedEntries(t, store, law) })
}

// checkUnexplainedEntries checks HR 1's entries before any explanation: one per section, in the
// references' order, with the strongest kind and the references' details together.
func checkUnexplainedEntries(t *testing.T, got *model.BillLawChanges) {
	t.Helper()
	if got.Explained != nil {
		t.Errorf("explained = %+v, want nil", got.Explained)
	}
	var sections []string
	for _, c := range got.Changes {
		sections = append(sections, c.SectionID+"|"+c.ChangeKind)
		if c.Explanation != nil {
			t.Errorf("%s explanation = %q, want nil", c.SectionID, *c.Explanation)
		}
	}
	want := []string{secNotLoaded + "|adds", secSSA + "|amends", secPhysicians + "|repeals"}
	if !reflect.DeepEqual(sections, want) {
		t.Fatalf("sections = %v, want %v (one per section, strongest kind, cites left out)", sections, want)
	}

	checkPhysiciansEntry(t, got.Changes[2])

	notLoaded, ssa := got.Changes[0], got.Changes[1]
	if !notLoaded.InUSCode || notLoaded.Loaded || notLoaded.Heading != nil || notLoaded.TitleNumber == nil ||
		*notLoaded.TitleNumber != 16 || len(notLoaded.AlsoChangedBy) != 0 {
		t.Errorf("not-loaded entry = %+v, want a US Code section with no heading", notLoaded)
	}
	if ssa.InUSCode || ssa.Loaded || ssa.TitleNumber != nil || ssa.SectionNumber != nil ||
		ssa.AlsoChangedBy == nil {
		t.Errorf("nonusc entry = %+v, want not in the US Code, with an empty also-changed-by list", ssa)
	}
}

// checkExplainedEntries stores explanations of HR 1's latest text and then of an older one.
func checkExplainedEntries(t *testing.T, store *spannerdb.PipelineStoreImpl, law *spannerdb.LawRepository) {
	t.Helper()
	ctx := t.Context()
	before := time.Now().Add(-time.Minute)
	ssa := lawChange(secSSA, model.LawRefAmends, "hash-rh")
	ssa.Explanation = ""
	if err := store.ReplaceBillLawChanges(ctx, billID(1), []repository.BillLawChangeRow{
		lawChange(secPhysicians, model.LawRefRepeals, "hash-rh"), ssa,
	}); err != nil {
		t.Fatalf("ReplaceBillLawChanges: %v", err)
	}
	got, err := law.BillLawChangeEntries(ctx, billID(1), 5)
	if err != nil {
		t.Fatalf("BillLawChangeEntries: %v", err)
	}
	e := got.Explained
	if e == nil || e.ModelUsed != summaryModel || e.PromptVersion != lawPrompt || e.ReleasePoint != releasePoint ||
		e.GeneratedAt.Before(before) {
		t.Errorf("explained = %+v, want the explanation's provenance", e)
	}
	if x := got.Changes[2].Explanation; x == nil || *x != "It would change "+secPhysicians {
		t.Errorf("physicians explanation = %v, want the stored one", x)
	}
	if got.Changes[0].Explanation != nil || got.Changes[1].Explanation != nil {
		t.Errorf("unexplained entries = %+v, want nil explanations (none, or empty)", got.Changes[:2])
	}

	// Explanations of an older text don't describe the latest one.
	if err = store.ReplaceBillLawChanges(ctx, billID(1), []repository.BillLawChangeRow{
		lawChange(secPhysicians, model.LawRefRepeals, "hash-ih"),
	}); err != nil {
		t.Fatalf("ReplaceBillLawChanges: %v", err)
	}
	if got, err = law.BillLawChangeEntries(ctx, billID(1), 5); err != nil || got.Explained != nil ||
		got.Changes[2].Explanation != nil {
		t.Errorf("stale explanation: got %+v, err %v; want it left out", got, err)
	}
}

// checkPhysiciansEntry checks the entry for 42 U.S.C. 1395w-4, which HR 1 amends and repeals.
func checkPhysiciansEntry(t *testing.T, phys model.LawChangeEntry) {
	t.Helper()
	if !phys.InUSCode || !phys.Loaded || phys.TitleNumber == nil || *phys.TitleNumber != 42 ||
		phys.SectionNumber == nil || *phys.SectionNumber != "1395w-4" || phys.Heading == nil {
		t.Errorf("physicians entry = %+v, want a loaded US Code section 42/1395w-4 with its heading", phys)
	}
	if phys.CiteText == nil || *phys.CiteText != "42 U.S.C. 1395w–4(t)" {
		t.Errorf("cite text = %v, want the first reference's", phys.CiteText)
	}
	if phys.SubsectionPath == nil || *phys.SubsectionPath != "(t), (c)" {
		t.Errorf("subsection path = %v, want (t), (c)", phys.SubsectionPath)
	}
	if phys.Instruction == nil || *phys.Instruction != "is amended by striking \"2025\"\n\nis repealed" {
		t.Errorf("instruction = %v, want both instructions", phys.Instruction)
	}
	var also []string
	for _, b := range phys.AlsoChangedBy {
		also = append(also, b.BillID)
	}
	if wantAlso := []string{billID(2), billID(5)}; !reflect.DeepEqual(also, wantAlso) {
		t.Errorf("also changed by = %v, want %v (same congress, not cites-only)", also, wantAlso)
	}
}

// TestBillLawChangeEntries_Note covers a statutory note (#573): HR 1's latest text amends
// 4 U.S.C. 1 note and 4 U.S.C. 1 itself, and HR 2 amends the same note. The note is in the US
// Code under title 4, section 1, but it's not the loaded section, so it gets neither the section's
// heading nor its explanation, and the other bills are those changing the note.
func TestBillLawChangeEntries_Note(t *testing.T) {
	store, client := newLawChangeStore(t)
	ctx := t.Context()
	law := spannerdb.NewLawRepo(&spannerdb.Client{Spanner: client})
	note := secFlag + model.USCNoteSuffix

	ids := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 1, []string{"rh"}, "rh")
	lawRefs(t, store, billID(1), ids["rh"],
		repository.BillLawRefRow{SectionID: note, RefKind: model.LawRefAmends, CiteText: new("4 U.S.C. 1 note")},
		ref(secFlag, model.LawRefAmends),
	)
	other := seedSummaryBill(t, store, client, testdb.FixtureCongress, 2, 2, []string{"ih"}, "ih")
	lawRefs(t, store, billID(2), other["ih"], ref(note, model.LawRefAmends))
	if err := store.ReplaceBillLawChanges(ctx, billID(1), []repository.BillLawChangeRow{
		lawChange(note, model.LawRefAmends, "hash-rh"),
	}); err != nil {
		t.Fatalf("ReplaceBillLawChanges: %v", err)
	}

	got, err := law.BillLawChangeEntries(ctx, billID(1), 5)
	if err != nil || got == nil || len(got.Changes) != 2 {
		t.Fatalf("got %+v, err %v; want two entries", got, err)
	}
	byID := map[string]model.LawChangeEntry{}
	for _, c := range got.Changes {
		byID[c.SectionID] = c
	}
	n, sec := byID[note], byID[secFlag]
	if !n.InUSCode || !n.IsNote || n.TitleNumber == nil || *n.TitleNumber != 4 || n.SectionNumber == nil ||
		*n.SectionNumber != "1" || n.Loaded || n.Heading != nil {
		t.Errorf("note entry = %+v, want a statutory note under 4 U.S.C. 1, not loaded, with no heading", n)
	}
	if n.CiteText == nil || *n.CiteText != "4 U.S.C. 1 note" {
		t.Errorf("note cite text = %v, want 4 U.S.C. 1 note", n.CiteText)
	}
	if n.Explanation == nil || *n.Explanation != "It would change "+note {
		t.Errorf("note explanation = %v, want the stored one", n.Explanation)
	}
	if len(n.AlsoChangedBy) != 1 || n.AlsoChangedBy[0].BillID != billID(2) {
		t.Errorf("note also changed by = %+v, want %s", n.AlsoChangedBy, billID(2))
	}
	if !sec.InUSCode || sec.IsNote || !sec.Loaded || sec.Explanation != nil || len(sec.AlsoChangedBy) != 0 {
		t.Errorf("section entry = %+v, want the loaded section itself, unexplained, no other bills", sec)
	}
}

// TestBillLawChangeEntries_AlsoChangedByOrder covers the other bills listed under a section
// (#784 reads them per section and bill, then the bills once each): a bill changing the section
// in two of its versions is listed once with both kinds, newest introduced first and undated
// last, cut at the limit, without the 118th's bill.
func TestBillLawChangeEntries_AlsoChangedByOrder(t *testing.T) {
	store, client := newLawChangeStore(t)
	ctx := t.Context()
	law := spannerdb.NewLawRepo(&spannerdb.Client{Spanner: client})

	own := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 1, []string{"ih"}, "ih")
	lawRefs(t, store, billID(1), own["ih"], ref(secPhysicians, model.LawRefAmends))
	introduced := map[string]spanner.NullDate{
		billID(2):  {Date: civil.Date{Year: 2025, Month: 3, Day: 1}, Valid: true},
		billID(3):  {Date: civil.Date{Year: 2025, Month: 6, Day: 1}, Valid: true},
		billID(4):  {},
		"hr-118-5": {Date: civil.Date{Year: 2024, Month: 1, Day: 1}, Valid: true},
	}
	two := seedSummaryBill(t, store, client, testdb.FixtureCongress, 2, 1, []string{"rh", "ih"}, "rh", "ih")
	lawRefs(t, store, billID(2), two["ih"], ref(secPhysicians, model.LawRefAmends))
	lawRefs(t, store, billID(2), two["rh"], ref(secPhysicians, model.LawRefRepeals))
	for _, n := range []int{3, 4} {
		v := seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 1, []string{"ih"}, "ih")
		lawRefs(t, store, billID(n), v["ih"], ref(secPhysicians, model.LawRefAdds))
	}
	prev := seedSummaryBill(t, store, client, testdb.FixturePrevCongress, 5, 1, []string{"ih"}, "ih")
	lawRefs(t, store, "hr-118-5", prev["ih"], ref(secPhysicians, model.LawRefAmends))
	for id, date := range introduced {
		if _, err := client.Apply(ctx, []*spanner.Mutation{
			spanner.Update("bills", []string{"bill_id", "introduced_date"}, []any{id, date}),
		}); err != nil {
			t.Fatalf("set introduced date of %s: %v", id, err)
		}
	}

	for _, tt := range []struct {
		limit int
		want  []string
	}{
		{5, []string{billID(3) + "|adds", billID(2) + "|amends,repeals", billID(4) + "|adds"}},
		{2, []string{billID(3) + "|adds", billID(2) + "|amends,repeals"}},
	} {
		got, err := law.BillLawChangeEntries(ctx, billID(1), tt.limit)
		if err != nil || got == nil || len(got.Changes) != 1 {
			t.Fatalf("limit %d: got %+v, err %v; want one entry", tt.limit, got, err)
		}
		var also []string
		for _, b := range got.Changes[0].AlsoChangedBy {
			also = append(also, b.BillID+"|"+strings.Join(b.RefKinds, ","))
		}
		if !reflect.DeepEqual(also, tt.want) {
			t.Errorf("limit %d: also changed by = %v, want %v", tt.limit, also, tt.want)
		}
	}
}
