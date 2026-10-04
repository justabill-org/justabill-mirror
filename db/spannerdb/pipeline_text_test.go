package spannerdb_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const (
	typeIntroduced = "Introduced in House"
	typeReported   = "Reported in House"
	typeEngrossed  = "Engrossed in House"
	typeEAS        = "Engrossed Amendment Senate"
)

func textVersion(bill, versionType, code string, sortOrder int) repository.TextVersionRow {
	return repository.TextVersionRow{
		BillID: bill, VersionType: versionType, VersionCode: code, SortOrder: sortOrder,
		Formats: json.RawMessage(`[{"type":"Formatted XML","url":"https://example.test/` + code + `.xml"}]`),
	}
}

func upsertVersions(
	t *testing.T, store *spannerdb.PipelineStoreImpl, bill string, rows ...repository.TextVersionRow,
) repository.TextVersionSyncResult {
	t.Helper()
	res, err := store.UpsertBillTextVersions(t.Context(), bill, rows)
	if err != nil {
		t.Fatalf("upsert text versions: %v", err)
	}
	return res
}

// versionIDs maps a bill's version codes to their version_id.
func versionIDs(t *testing.T, client *spanner.Client, bill string) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, row := range queryStrings(t, client,
		"SELECT version_code, version_id FROM bill_text_versions WHERE bill_id = @bill",
		map[string]any{"bill": bill}) {
		code, id, _ := strings.Cut(row, "|")
		ids[code] = id
	}
	return ids
}

// addTextAndDiff stores a text for each version and a summarized diff for each
// consecutive pair, the way sync-texts and sync-summaries would.
func addTextAndDiff(t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client, bill string,
	ids ...string,
) {
	t.Helper()
	ctx := t.Context()
	now := time.Now()
	for _, id := range ids {
		if err := store.InsertBillText(ctx, repository.BillTextRow{
			TextVersionID: id, Format: "xml", Content: "text " + id, ContentHash: "h-" + id, FetchedAt: now,
		}); err != nil {
			t.Fatalf("insert text: %v", err)
		}
	}
	for i := 1; i < len(ids); i++ {
		if err := store.InsertBillTextDiff(ctx, repository.BillTextDiffRow{
			BillID: bill, FromVersionID: ids[i-1], ToVersionID: ids[i],
			DiffContent: json.RawMessage(`{"changes":[]}`), GeneratedAt: now,
		}); err != nil {
			t.Fatalf("insert diff: %v", err)
		}
		diffID := queryStrings(t, client,
			"SELECT diff_id FROM bill_text_diffs WHERE from_version_id = @f AND to_version_id = @t",
			map[string]any{"f": ids[i-1], "t": ids[i]})
		if len(diffID) != 1 {
			t.Fatalf("diff ids = %q, want one", diffID)
		}
		recordDiffSummary(t, store, diffID[0])
	}
}

// textCounts returns the bill's text, diff and diff-summary counts as one row.
func textCounts(t *testing.T, client *spanner.Client, bill string) []string {
	t.Helper()
	return queryStrings(t, client, `SELECT
		CAST((SELECT COUNT(*) FROM bill_texts bt JOIN bill_text_versions v ON v.version_id = bt.version_id
			WHERE v.bill_id = @bill) AS STRING),
		CAST((SELECT COUNT(*) FROM bill_text_diffs WHERE bill_id = @bill) AS STRING),
		CAST((SELECT COUNT(*) FROM bill_text_diff_summaries s JOIN bill_text_diffs d ON d.diff_id = s.diff_id
			WHERE d.bill_id = @bill) AS STRING)`, map[string]any{"bill": bill})
}

func TestUpsertBillTextVersions_ResyncKeepsIDsTextsAndDiffs(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill

	res := upsertVersions(
		t,
		store,
		bill,
		textVersion(bill, typeEngrossed, "eh", 1),
		textVersion(bill, typeIntroduced, "ih", 2),
	)
	if res.Inserted != 2 || res.Updated != 0 || res.Pruned != 0 {
		t.Fatalf("first sync = %+v, want 2 inserted", res)
	}
	before := versionIDs(t, client, bill)
	addTextAndDiff(t, store, client, bill, before["ih"], before["eh"])
	assertRows(t, textCounts(t, client, bill), []string{"2|1|1"})

	// Same versions with corrected order, a date and new formats.
	date := time.Date(2025, time.February, 26, 0, 0, 0, 0, time.UTC)
	ih := textVersion(bill, typeIntroduced, "ih", 1)
	eh := textVersion(bill, typeEngrossed, "eh", 2)
	eh.Date = &date
	eh.Formats = json.RawMessage(`[{"type":"PDF","url":"https://example.test/eh.pdf"}]`)
	res = upsertVersions(t, store, bill, eh, ih)
	if res.Inserted != 0 || res.Updated != 2 || res.Pruned != 0 {
		t.Fatalf("re-sync = %+v, want 2 updated", res)
	}
	assertRows(t, []string{versionIDs(t, client, bill)["ih"], versionIDs(t, client, bill)["eh"]},
		[]string{before["ih"], before["eh"]})
	assertRows(t, textCounts(t, client, bill), []string{"2|1|1"})
	assertRows(t, queryStrings(t, client, `SELECT version_code, CAST(sort_order AS STRING),
		IFNULL(CAST(date AS STRING), ''), JSON_VALUE(formats, '$[0].type')
		FROM bill_text_versions WHERE bill_id = @bill ORDER BY sort_order`, map[string]any{"bill": bill}),
		[]string{"ih|1||Formatted XML", "eh|2|2025-02-26|PDF"})
}

func TestUpsertBillTextVersions_InsertsNewVersions(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill

	upsertVersions(t, store, bill, textVersion(bill, typeIntroduced, "ih", 1))
	ihID := versionIDs(t, client, bill)["ih"]
	res := upsertVersions(
		t,
		store,
		bill,
		textVersion(bill, typeReported, "rh", 2),
		textVersion(bill, typeIntroduced, "ih", 1),
	)
	if res.Inserted != 1 || res.Updated != 1 || res.Pruned != 0 {
		t.Fatalf("sync = %+v, want 1 inserted, 1 updated", res)
	}
	ids := versionIDs(t, client, bill)
	if ids["ih"] != ihID || ids["rh"] == "" || ids["rh"] == ihID {
		t.Errorf("ids = %v, want ih kept (%s) and a new rh", ids, ihID)
	}
}

func TestUpsertBillTextVersions_RecodeByTypeKeepsID(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill

	upsertVersions(t, store, bill, textVersion(bill, typeEAS, "engrossed_amendment_senate", 1))
	oldID := versionIDs(t, client, bill)["engrossed_amendment_senate"]
	addTextAndDiff(t, store, client, bill, oldID)

	res := upsertVersions(t, store, bill, textVersion(bill, typeEAS, "eas", 1))
	if res.Updated != 1 || res.Inserted != 0 || res.Pruned != 0 {
		t.Fatalf("recode = %+v, want 1 updated", res)
	}
	if got := versionIDs(t, client, bill); len(got) != 1 || got["eas"] != oldID {
		t.Errorf("ids = %v, want only eas with the old id %s", got, oldID)
	}
	assertRows(t, textCounts(t, client, bill), []string{"1|0|0"})
}

func TestUpsertBillTextVersions_PrunesUnlistedVersionsOnly(t *testing.T) {
	store, client := newLinkStore(t)
	bill, other := testdb.FixtureHouseBill, testdb.FixtureSenateBill

	upsertVersions(t, store, bill, textVersion(bill, typeEngrossed, "eh", 3),
		textVersion(bill, typeReported, "rh", 2), textVersion(bill, typeIntroduced, "ih", 1))
	ids := versionIDs(t, client, bill)
	addTextAndDiff(t, store, client, bill, ids["ih"], ids["rh"], ids["eh"])
	upsertVersions(t, store, other, textVersion(other, "Reported in Senate", "rs", 2),
		textVersion(other, "Introduced in Senate", "is", 1))
	otherIDs := versionIDs(t, client, other)
	addTextAndDiff(t, store, client, other, otherIDs["is"], otherIDs["rs"])

	res := upsertVersions(
		t,
		store,
		bill,
		textVersion(bill, typeEngrossed, "eh", 2),
		textVersion(bill, typeIntroduced, "ih", 1),
	)
	if res.Pruned != 1 || res.Updated != 2 || len(res.PrunedCodes) != 1 || res.PrunedCodes[0] != "rh" {
		t.Fatalf("prune = %+v, want rh pruned and 2 updated", res)
	}
	after := versionIDs(t, client, bill)
	if _, ok := after["rh"]; ok || after["ih"] != ids["ih"] || after["eh"] != ids["eh"] {
		t.Errorf("ids = %v, want ih and eh kept, rh gone", after)
	}
	// rh's text, both diffs touching it and their summaries and attempts are gone.
	assertRows(t, textCounts(t, client, bill), []string{"2|0|0"})
	assertRows(t, diffAttemptCounts(t, client), []string{"1|0"})
	assertRows(t, queryStrings(t, client, "SELECT version_id FROM bill_texts WHERE version_id = @id",
		map[string]any{"id": ids["rh"]}), nil)
	assertRows(t, textCounts(t, client, other), []string{"2|1|1"})
}

// TestUpsertBillTextVersions_PruneDeletesLawRefs is the regression test for #530: a pruned
// version's law references went on listing the bill as changing a US Code section.
func TestUpsertBillTextVersions_PruneDeletesLawRefs(t *testing.T) {
	store, client := newLinkStore(t)
	graph := spannerdb.NewGraphRepo(&spannerdb.Client{Spanner: client})
	bill, other := testdb.FixtureHouseBill, testdb.FixtureSenateBill
	loadSections(t, store, secFlag, secMedicare)

	upsertVersions(t, store, bill, textVersion(bill, typeEngrossed, "eh", 3),
		textVersion(bill, typeReported, "rh", 2), textVersion(bill, typeIntroduced, "ih", 1))
	ids := versionIDs(t, client, bill)
	lawRefs(t, store, bill, ids["ih"], ref(secMedicare, model.LawRefAmends))
	lawRefs(t, store, bill, ids["rh"], ref(secFlag, model.LawRefAmends), ref(secMedicare, model.LawRefCites))
	upsertVersions(t, store, other, textVersion(other, "Introduced in Senate", "is", 1))
	lawRefs(t, store, other, versionIDs(t, client, other)["is"], ref(secFlag, model.LawRefAmends))

	changing := func(section string) []string {
		t.Helper()
		bills, err := graph.BillsChangingSection(t.Context(), section, "", testdb.FixtureCongress, 0)
		if err != nil {
			t.Fatalf("BillsChangingSection %s: %v", section, err)
		}
		ids := sectionBillIDs(bills)
		slices.Sort(ids)
		return ids
	}
	assertRows(t, changing(secFlag), []string{bill, other})

	res := upsertVersions(t, store, bill, textVersion(bill, typeEngrossed, "eh", 2),
		textVersion(bill, typeIntroduced, "ih", 1))
	if res.Pruned != 1 {
		t.Fatalf("prune = %+v, want rh pruned", res)
	}
	// Only rh's references go: ih's stay, and so do the other bill's.
	refsSQL := "SELECT version_id, section_id, ref_kind FROM bill_law_refs WHERE bill_id = @bill ORDER BY section_id"
	assertRows(t, queryStrings(t, client, refsSQL, map[string]any{"bill": bill}),
		[]string{ids["ih"] + "|" + secMedicare + "|" + model.LawRefAmends})
	if got := queryStrings(t, client, refsSQL, map[string]any{"bill": other}); len(got) != 1 {
		t.Errorf("other bill's refs = %q, want its one ref kept", got)
	}
	assertRows(t, changing(secFlag), []string{other})
	assertRows(t, changing(secMedicare), []string{bill})
}

func TestUpsertBillTextVersions_EmptyInputPrunesNothing(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill

	upsertVersions(t, store, bill, textVersion(bill, typeIntroduced, "ih", 1))
	ids := versionIDs(t, client, bill)
	addTextAndDiff(t, store, client, bill, ids["ih"])

	if res := upsertVersions(t, store, bill); res.Pruned != 0 || res.Updated != 0 || res.Inserted != 0 {
		t.Fatalf("empty sync = %+v, want no changes", res)
	}
	if got := versionIDs(t, client, bill); got["ih"] != ids["ih"] {
		t.Errorf("ids = %v, want ih kept", got)
	}
	assertRows(t, textCounts(t, client, bill), []string{"1|0|0"})
}

func TestUpsertBillTextVersions_DuplicateCodesAndSwaps(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill

	// Two entries with one code: the first (newest) wins, and the write doesn't fail.
	res := upsertVersions(
		t,
		store,
		bill,
		textVersion(bill, typeEAS, "eas", 2),
		textVersion(
			bill,
			"Engrossed Amendment Senate (duplicate)",
			"eas",
			1,
		),
		textVersion(bill, typeIntroduced, "ih", 1),
	)
	if res.Inserted != 2 || res.Duplicates != 1 {
		t.Fatalf("duplicates = %+v, want 2 inserted, 1 duplicate", res)
	}
	assertRows(
		t,
		queryStrings(
			t,
			client,
			"SELECT version_type FROM bill_text_versions WHERE bill_id = @bill AND version_code = 'eas'",
			map[string]any{"bill": bill},
		),
		[]string{typeEAS},
	)

	// Two stored rows trading types keep their codes and IDs.
	before := versionIDs(t, client, bill)
	res = upsertVersions(
		t,
		store,
		bill,
		textVersion(bill, typeIntroduced, "eas", 2),
		textVersion(bill, typeEAS, "ih", 1),
	)
	if res.Updated != 2 || res.Inserted != 0 || res.Pruned != 0 {
		t.Fatalf("swap = %+v, want 2 updated", res)
	}
	after := versionIDs(t, client, bill)
	if after["eas"] != before["eas"] || after["ih"] != before["ih"] {
		t.Errorf("ids after swap = %v, want %v", after, before)
	}

	// An unlisted row is pruned in the same write that inserts a new one.
	res = upsertVersions(t, store, bill, textVersion(bill, typeEAS, "eas", 2), textVersion(bill, typeReported, "rh", 1))
	if res.Updated != 1 || res.Inserted != 1 || res.Pruned != 1 {
		t.Fatalf("prune and insert = %+v, want 1 updated, 1 inserted, 1 pruned", res)
	}
}

func TestUpdateBillTextSections_ReplacesOnlySections(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	bill := testdb.FixtureHouseBill
	upsertVersions(t, store, bill, textVersion(bill, typeIntroduced, "ih", 1))
	id := versionIDs(t, client, bill)["ih"]
	if err := store.InsertBillText(ctx, repository.BillTextRow{
		TextVersionID: id, Format: "Formatted XML", Content: "<bill/>", ContentHash: "h",
		Sections: json.RawMessage(`[{"id":"old","header":"","content":""}]`), FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("insert text: %v", err)
	}

	sections := json.RawMessage(`[{"id":"new","kind":"section","header":"Short title","content":"Text."}]`)
	if err := store.UpdateBillTextSections(ctx, id, sections); err != nil {
		t.Fatalf("UpdateBillTextSections: %v", err)
	}
	got, err := store.LoadSections(ctx, id)
	if err != nil {
		t.Fatalf("LoadSections: %v", err)
	}
	var parsed []map[string]string
	if err = json.Unmarshal(got, &parsed); err != nil || len(parsed) != 1 || parsed[0]["id"] != "new" {
		t.Errorf("sections = %s (%v), want the new ones", got, err)
	}
	assertRows(t, queryStrings(t, client, "SELECT content FROM bill_texts WHERE version_id = @v",
		map[string]any{"v": id}), []string{"<bill/>"})

	if err = store.UpdateBillTextSections(ctx, "no-such-version", sections); err == nil {
		t.Error("updating a version without a text: want an error")
	}
}
