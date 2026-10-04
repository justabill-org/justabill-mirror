package spannerdb_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// The read paths behind a bill's page (#225), each written the way the pipeline writes it.

const missingBill = "hr-119-9999"

// newBillDetailRepos seeds the fixture and returns the bill page's repository with the
// pipeline's store on the same database.
func newBillDetailRepos(t *testing.T) (*spannerdb.BillRepository, *spannerdb.PipelineStoreImpl) {
	t.Helper()
	store, client := newLinkStore(t)
	return spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client}), store
}

func utcDate(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestBillGetByID(t *testing.T) {
	repo, store := newBillDetailRepos(t)
	ctx := t.Context()

	b, err := repo.GetByID(ctx, testdb.FixtureHouseBill)
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatalf("GetByID(%s) = nil, want the bill", testdb.FixtureHouseBill)
	}
	if b.Congress != testdb.FixtureCongress || b.BillType != "hr" || b.Number != 1 || b.Title != "Companion Act" {
		t.Errorf("GetByID = %d %s %d %q, want 119 hr 1 %q", b.Congress, b.BillType, b.Number, b.Title, "Companion Act")
	}
	if deref(b.OriginChamber) != "House" || b.IntroducedDate == nil || len(b.Sponsors) == 0 {
		t.Errorf("GetByID chamber %q, introduced %v, sponsors %s; want House, a date and the sponsors",
			deref(b.OriginChamber), b.IntroducedDate, b.Sponsors)
	}
	if b.CurrentStatus != nil || b.UpdatedAt != nil {
		t.Errorf("GetByID status %v, updated_at %v; want both NULL in the fixture", b.CurrentStatus, b.UpdatedAt)
	}

	// The relationship columns the pipeline rewrites read back, and a bad column is refused.
	subjects := json.RawMessage(`["Taxation"]`)
	if err = store.UpdateBillJSON(ctx, testdb.FixtureHouseBill, "subjects", subjects); err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateBillJSON(ctx, testdb.FixtureHouseBill, "title", json.RawMessage(`"x"`)); err == nil {
		t.Error("UpdateBillJSON(title) = nil, want an error for a column it doesn't write")
	}
	if b, err = repo.GetByID(ctx, testdb.FixtureHouseBill); err != nil {
		t.Fatal(err)
	}
	if string(b.Subjects) != `["Taxation"]` {
		t.Errorf("subjects after UpdateBillJSON = %s, want [\"Taxation\"]", b.Subjects)
	}

	// The same number in another chamber and another congress are other bills; a missing one is nil.
	for _, id := range []string{testdb.FixtureSenateBill, testdb.FixturePrevHouseBill} {
		other, getErr := repo.GetByID(ctx, id)
		if getErr != nil || other == nil || other.ID != id {
			t.Errorf("GetByID(%s) = %v, %v; want that bill", id, other, getErr)
		}
	}
	if missing, getErr := repo.GetByID(ctx, missingBill); getErr != nil || missing != nil {
		t.Errorf("GetByID(%s) = %v, %v; want nil, nil", missingBill, missing, getErr)
	}
}

func TestBillGetActions(t *testing.T) {
	repo, store := newBillDetailRepos(t)
	ctx := t.Context()

	committee := "Committee"
	// Written out of order: the reader orders by sort_order.
	actions := []repository.BillActionRow{
		{ActionDate: utcDate(2025, time.February, 1), ActionText: "Passed House", SortOrder: 2},
		{ActionDate: utcDate(2025, time.January, 3), ActionText: "Introduced", ActionType: &committee, SortOrder: 1},
	}
	if err := store.ReplaceBillActions(ctx, testdb.FixtureHouseBill, actions); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetActions(ctx, testdb.FixtureHouseBill)
	if err != nil {
		t.Fatal(err)
	}
	if texts := actionTexts(got); !slices.Equal(texts, []string{"Introduced", "Passed House"}) {
		t.Fatalf("GetActions = %v, want [Introduced Passed House]", texts)
	}
	first := got[0]
	if first.ID == "" || first.BillID != testdb.FixtureHouseBill || !first.ActionDate.Equal(actions[1].ActionDate) ||
		deref(first.ActionType) != committee || first.ActionCode != nil {
		t.Errorf("GetActions[0] = %+v, want the introduced action with a generated ID and no code", first)
	}

	// A sync replaces the whole list.
	if err = store.ReplaceBillActions(ctx, testdb.FixtureHouseBill, actions[:1]); err != nil {
		t.Fatal(err)
	}
	if got, err = repo.GetActions(ctx, testdb.FixtureHouseBill); err != nil {
		t.Fatal(err)
	}
	if texts := actionTexts(got); !slices.Equal(texts, []string{"Passed House"}) {
		t.Errorf("GetActions after replace = %v, want [Passed House]", texts)
	}

	for _, id := range []string{testdb.FixtureSenateBill, missingBill} {
		none, getErr := repo.GetActions(ctx, id)
		if getErr != nil || none == nil || len(none) != 0 {
			t.Errorf("GetActions(%s) = %v, %v; want an empty, non-nil list", id, none, getErr)
		}
	}
}

func TestListStoredBillActions(t *testing.T) {
	_, store := newBillDetailRepos(t)
	ctx := t.Context()

	code, source := "8000", "Library of Congress"
	actions := []repository.BillActionRow{
		{ActionDate: utcDate(2025, time.February, 1), ActionText: "Passed/agreed to in House", ActionCode: &code,
			SourceSystem: &source, SortOrder: 2},
		{ActionDate: utcDate(2025, time.January, 3), ActionText: "Introduced in House", SortOrder: 1},
	}
	if err := store.ReplaceBillActions(ctx, testdb.FixtureHouseBill, actions); err != nil {
		t.Fatal(err)
	}

	// In bill_id order, the fixture's H.J.Res. 63 comes first, with no actions.
	first, err := store.ListStoredBillActions(ctx, testdb.FixtureCongress, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].BillID != testdb.FixtureCRAUnmatched || len(first[0].Actions) != 0 ||
		first[1].BillID != testdb.FixtureHouseBill || len(first[1].Actions) != 2 {
		t.Fatalf("first page = %+v, want %s with no actions, then %s with its 2", first,
			testdb.FixtureCRAUnmatched, testdb.FixtureHouseBill)
	}
	// In sort_order, with every column the status derivation reads.
	got := first[1].Actions
	if got[0].ActionText != "Introduced in House" || got[0].ActionCode != nil || got[0].SortOrder != 1 {
		t.Errorf("action 1 = %+v, want the introduced action, no code", got[0])
	}
	if a := got[1]; deref(a.ActionCode) != code || deref(a.SourceSystem) != source || a.SortOrder != 2 ||
		!a.ActionDate.Equal(utcDate(2025, time.February, 1)) {
		t.Errorf("action 2 = %+v, want the coded House passage on 2025-02-01", a)
	}

	// The rest of the 119th: bills without stored actions come back with none, and the 118th's
	// HR 1 isn't listed.
	rest, err := store.ListStoredBillActions(ctx, testdb.FixtureCongress, testdb.FixtureHouseBill, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 3 || rest[0].BillID != testdb.FixtureLawBill || rest[1].BillID != testdb.FixtureSenateBill ||
		rest[2].BillID != testdb.FixtureCRABill {
		t.Fatalf("second page = %+v, want %s, %s and %s", rest, testdb.FixtureLawBill, testdb.FixtureSenateBill,
			testdb.FixtureCRABill)
	}
	done, err := store.ListStoredBillActions(ctx, testdb.FixtureCongress, rest[2].BillID, 10)
	if err != nil || len(done) != 0 {
		t.Fatalf("last page = %+v, %v; want empty", done, err)
	}
}

func actionTexts(actions []model.BillAction) []string {
	texts := make([]string, 0, len(actions))
	for _, a := range actions {
		texts = append(texts, a.ActionText)
	}
	return texts
}

func TestBillGetStatusHistory(t *testing.T) {
	repo, store := newBillDetailRepos(t)
	ctx := t.Context()

	// The fixture's law bill: introduced, then became law.
	got, err := repo.GetStatusHistory(ctx, testdb.FixtureLawBill)
	if err != nil {
		t.Fatal(err)
	}
	if s := statuses(got); !slices.Equal(s, []string{"introduced", "became_law"}) {
		t.Errorf("GetStatusHistory(law bill) = %v, want [introduced became_law]", s)
	}

	rows := []repository.BillStatusRow{
		{
			BillID:     testdb.FixtureHouseBill,
			Status:     "signed",
			StatusDate: utcDate(2025, time.March, 4),
			StatusRank: 8,
		},
		{
			BillID:     testdb.FixtureHouseBill,
			Status:     "introduced",
			StatusDate: utcDate(2025, time.January, 3),
			StatusRank: 1,
		},
	}
	signed := utcDate(2025, time.March, 4)
	if err = store.ReplaceBillStatus(ctx, testdb.FixtureHouseBill, "signed", &signed, rows); err != nil {
		t.Fatal(err)
	}
	// Deriving it again replaces the history: a stage the bill no longer has is dropped and a new one
	// added, and the current status follows.
	presented := utcDate(2025, time.March, 5)
	rows = []repository.BillStatusRow{
		rows[1],
		{BillID: testdb.FixtureHouseBill, Status: "to_president", StatusDate: presented, StatusRank: 7},
	}
	if err = store.ReplaceBillStatus(ctx, testdb.FixtureHouseBill, "to_president", &presented, rows); err != nil {
		t.Fatal(err)
	}
	if got, err = repo.GetStatusHistory(ctx, testdb.FixtureHouseBill); err != nil {
		t.Fatal(err)
	}
	if s := statuses(got); !slices.Equal(s, []string{"introduced", "to_president"}) {
		t.Fatalf("GetStatusHistory = %v, want [introduced to_president]", s)
	}
	if got[1].StatusRank != 7 || !got[1].StatusDate.Equal(presented) {
		t.Errorf("to_president = rank %d on %v, want rank 7 on 2025-03-05", got[1].StatusRank, got[1].StatusDate)
	}
	b, err := repo.GetByID(ctx, testdb.FixtureHouseBill)
	if err != nil {
		t.Fatal(err)
	}
	if deref(b.CurrentStatus) != "to_president" || b.StatusDate == nil || !b.StatusDate.Equal(presented) {
		t.Errorf("current status = %q on %v, want to_president on 2025-03-05", deref(b.CurrentStatus), b.StatusDate)
	}

	none, err := repo.GetStatusHistory(ctx, missingBill)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("GetStatusHistory(%s) = %v, %v; want an empty, non-nil list", missingBill, none, err)
	}
}

func statuses(entries []model.BillStatusEntry) []string {
	s := make([]string, 0, len(entries))
	for _, e := range entries {
		s = append(s, e.Status)
	}
	return s
}

func TestBillGetAmendments(t *testing.T) {
	repo, store := newBillDetailRepos(t)
	ctx := t.Context()

	purpose := "To strike section 2."
	a := repository.AmendmentRow{
		ID: "hamdt-119-5", BillID: testdb.FixtureHouseBill, Congress: testdb.FixtureCongress,
		AmendmentType: "hamdt", AmendmentNumber: 5, Purpose: &purpose,
		LatestAction: json.RawMessage(`{"text":"Agreed to"}`), Chamber: "House",
	}
	if err := store.UpsertAmendment(ctx, a); err != nil {
		t.Fatal(err)
	}
	// A second sync of the same amendment updates it.
	a.Purpose = nil
	description := "Amendment in the nature of a substitute."
	a.Description = &description
	if err := store.UpsertAmendment(ctx, a); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetAmendments(ctx, testdb.FixtureHouseBill)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("GetAmendments = %d amendments, want 1", len(got))
	}
	g := got[0]
	if g.ID != a.ID || g.BillID != a.BillID || g.Congress != a.Congress || g.AmendmentType != "hamdt" ||
		g.AmendmentNumber != 5 || g.Chamber != "House" || deref(g.Description) != description || g.Purpose != nil {
		t.Errorf("GetAmendments[0] = %+v, want the updated amendment", g)
	}
	if string(g.LatestAction) != `{"text":"Agreed to"}` {
		t.Errorf("latest action = %s, want the stored JSON", g.LatestAction)
	}

	for _, id := range []string{testdb.FixtureSenateBill, missingBill} {
		none, getErr := repo.GetAmendments(ctx, id)
		if getErr != nil || none == nil || len(none) != 0 {
			t.Errorf("GetAmendments(%s) = %v, %v; want an empty, non-nil list", id, none, getErr)
		}
	}
}

func TestBillListGAOReports(t *testing.T) {
	repo, store := newBillDetailRepos(t)
	ctx := t.Context()

	older, newer := utcDate(2024, time.May, 1), utcDate(2025, time.June, 1)
	number := "GAO-25-1"
	reports := []repository.GAOReportRow{
		{ReportID: "gao-old", Title: "Older report", PublishedDate: &older},
		{ReportID: "gao-new", Title: "Newer report", PublishedDate: &newer, ReportNumber: &number},
		{ReportID: "gao-other", Title: "Another bill's report", PublishedDate: &newer},
	}
	for _, r := range reports {
		if err := store.UpsertGAOReport(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	links := [][2]string{
		{testdb.FixtureHouseBill, "gao-old"}, {testdb.FixtureHouseBill, "gao-new"},
		{testdb.FixtureSenateBill, "gao-other"},
		{testdb.FixtureHouseBill, "gao-new"}, // linking twice is a no-op
	}
	for _, l := range links {
		if err := store.LinkBillGAOReport(ctx, l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.ListGAOReports(ctx, testdb.FixtureHouseBill)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ReportID)
	}
	if !slices.Equal(ids, []string{"gao-new", "gao-old"}) {
		t.Fatalf("ListGAOReports = %v, want [gao-new gao-old] (newest first, this bill's only)", ids)
	}
	if deref(got[0].ReportNumber) != number || got[0].PublishedDate == nil || !got[0].PublishedDate.Equal(newer) ||
		got[1].ReportNumber != nil {
		t.Errorf("ListGAOReports = %+v, %+v; want the stored number and dates", got[0], got[1])
	}

	none, err := repo.ListGAOReports(ctx, testdb.FixtureLawBill)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("ListGAOReports(law bill) = %v, %v; want an empty, non-nil list", none, err)
	}
}

func TestBillTextVersionReads(t *testing.T) {
	client := seedScopedTexts(t)
	ctx := t.Context()
	c := &spannerdb.Client{Spanner: client}
	repo, store := spannerdb.NewBillRepo(c), spannerdb.NewPipelineStore(c)

	versions, err := repo.GetTextVersions(ctx, testdb.FixtureHouseBill)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, v := range versions {
		codes = append(codes, v.VersionCode)
	}
	if !slices.Equal(codes, []string{"ih", "eh"}) || versions[0].ID != scopedVersionID {
		t.Errorf("GetTextVersions = %v, want [ih eh] in sort order", codes)
	}
	none, err := repo.GetTextVersions(ctx, missingBill)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("GetTextVersions(%s) = %v, %v; want an empty, non-nil list", missingBill, none, err)
	}

	t.Run("FindPreviousVersion", func(t *testing.T) { checkFindPreviousVersion(t, store) })
	t.Run("FindUnfetchedVersion", func(t *testing.T) { checkFindUnfetchedVersion(t, store) })
	t.Run("GetDiffSummary", func(t *testing.T) { checkGetDiffSummary(t, repo, store) })
}

func checkFindPreviousVersion(t *testing.T, store *spannerdb.PipelineStoreImpl) {
	t.Helper()
	ctx := t.Context()
	prev, err := store.FindPreviousVersion(ctx, testdb.FixtureHouseBill, scopedToID)
	if err != nil || prev == nil || prev.VersionID != scopedVersionID {
		t.Errorf("FindPreviousVersion(eh) = %+v, %v; want the ih version", prev, err)
	}
	for _, v := range []string{scopedVersionID, "no-such-version"} {
		if prev, err = store.FindPreviousVersion(ctx, testdb.FixtureHouseBill, v); err != nil || prev != nil {
			t.Errorf("FindPreviousVersion(%s) = %+v, %v; want nil, nil", v, prev, err)
		}
	}
}

func checkFindUnfetchedVersion(t *testing.T, store *spannerdb.PipelineStoreImpl) {
	t.Helper()
	ctx := t.Context()
	id, err := store.FindUnfetchedVersion(ctx, testdb.FixtureHouseBill, "eh")
	if err != nil || id != scopedToID {
		t.Errorf("FindUnfetchedVersion(eh) = %q, %v; want %q", id, err, scopedToID)
	}
	// ih has its text already; enr belongs to another bill.
	for _, code := range []string{"ih", "enr"} {
		if id, err = store.FindUnfetchedVersion(ctx, testdb.FixtureHouseBill, code); err == nil {
			t.Errorf("FindUnfetchedVersion(%s) = %q, nil; want an error", code, id)
		}
	}
}

func checkGetDiffSummary(t *testing.T, repo *spannerdb.BillRepository, store *spannerdb.PipelineStoreImpl) {
	t.Helper()
	ctx := t.Context()
	got, err := repo.GetDiffSummary(ctx, scopedDiffID)
	if err != nil || got != nil {
		t.Fatalf("GetDiffSummary before a summary = %+v, %v; want nil, nil", got, err)
	}
	recordDiffSummary(t, store, scopedDiffID)
	if got, err = repo.GetDiffSummary(ctx, scopedDiffID); err != nil || got == nil {
		t.Fatalf("GetDiffSummary = %+v, %v; want the summary", got, err)
	}
	if got.DiffID != scopedDiffID || got.Summary != "summary" || deref(got.ModelUsed) != summaryModel ||
		got.GeneratedAt == nil {
		t.Errorf("GetDiffSummary = %+v, want the recorded summary", got)
	}
}
