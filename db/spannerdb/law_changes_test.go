package spannerdb_test

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const lawPrompt = "law-v1"

// newLawChangeStore is newSummaryStore with two loaded US Code sections.
func newLawChangeStore(t *testing.T) (*spannerdb.PipelineStoreImpl, *spanner.Client) {
	t.Helper()
	store, client := newSummaryStore(t)
	loadSections(t, store, secPhysicians, secFlag)
	return store, client
}

// loadSections writes the sections, as a US Code load does; a section written again gets a new
// updated_at.
func loadSections(t *testing.T, store *spannerdb.PipelineStoreImpl, ids ...string) {
	t.Helper()
	rows := make([]repository.USCSectionRow, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, uscRow(id, 42, "s", "Heading of "+id))
	}
	if err := store.UpsertUSCSections(t.Context(), rows); err != nil {
		t.Fatalf("UpsertUSCSections: %v", err)
	}
}

func lawRefs(t *testing.T, store *spannerdb.PipelineStoreImpl, bill, version string, rows ...repository.BillLawRefRow) {
	t.Helper()
	if err := store.ReplaceBillLawRefs(t.Context(), bill, version, rows); err != nil {
		t.Fatalf("ReplaceBillLawRefs %s: %v", bill, err)
	}
}

func ref(section, kind string) repository.BillLawRefRow {
	return repository.BillLawRefRow{SectionID: section, RefKind: kind}
}

func lawQueue(t *testing.T, store *spannerdb.PipelineStoreImpl, mod func(*repository.LawChangeQueueQuery)) []string {
	t.Helper()
	q := repository.LawChangeQueueQuery{
		Congress: testdb.FixtureCongress, Limit: 100, PromptVersion: lawPrompt, Model: summaryModel,
		Now: summaryNow(),
	}
	if mod != nil {
		mod(&q)
	}
	items, err := store.QueryBillsToExplainLaw(t.Context(), q)
	if err != nil {
		t.Fatalf("QueryBillsToExplainLaw: %v", err)
	}
	n, err := store.CountBillsToExplainLaw(t.Context(), q)
	if err != nil {
		t.Fatalf("CountBillsToExplainLaw: %v", err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.BillID+"|"+it.ContentHash)
	}
	if q.Limit >= n && n != len(out) {
		t.Errorf("count = %d, want %d", n, len(out))
	}
	return out
}

func lawAttempt(bill, hash, outcome string, at time.Time) repository.LawChangeAttemptRow {
	return repository.LawChangeAttemptRow{
		BillID: bill, ContentHash: hash, PromptVersion: lawPrompt, Model: summaryModel,
		Outcome: outcome, Reason: "r", AttemptedAt: at,
	}
}

func lawChange(section, kind, hash string) repository.BillLawChangeRow {
	return repository.BillLawChangeRow{
		SectionID: section, ChangeKind: kind, Explanation: "It would change " + section, SourceContentHash: hash,
		ReleasePoint: releasePoint, ModelUsed: summaryModel, PromptVersion: lawPrompt,
	}
}

func TestQueryBillsToExplainLaw_Due(t *testing.T) {
	store, client := newLawChangeStore(t)
	ctx := t.Context()
	// 1: its latest text amends a section. 2: only cites. 3: no references. 4: only its older
	// text amends. 5: no text. 6: voted, and changes a section that isn't loaded. 7: newer than 1.
	v1 := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"rh", "ih"}, "rh", "ih")
	lawRefs(t, store, billID(1), v1["rh"], ref(secPhysicians, model.LawRefAmends), ref(secFlag, model.LawRefCites))
	lawRefs(t, store, billID(1), v1["ih"], ref(secFlag, model.LawRefAmends))
	v2 := seedSummaryBill(t, store, client, testdb.FixtureCongress, 2, 10, []string{"ih"}, "ih")
	lawRefs(t, store, billID(2), v2["ih"], ref(secPhysicians, model.LawRefCites))
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 3, 10, []string{"ih"}, "ih")
	v4 := seedSummaryBill(t, store, client, testdb.FixtureCongress, 4, 10, []string{"rh", "ih"}, "rh", "ih")
	lawRefs(t, store, billID(4), v4["ih"], ref(secPhysicians, model.LawRefRepeals))
	lawRefs(t, store, billID(4), v4["rh"], ref(secPhysicians, model.LawRefCites))
	v5 := seedSummaryBill(t, store, client, testdb.FixtureCongress, 5, 10, []string{"ih"})
	lawRefs(t, store, billID(5), v5["ih"], ref(secPhysicians, model.LawRefAmends))
	v6 := seedSummaryBill(t, store, client, testdb.FixtureCongress, 6, 90, []string{"ih"}, "ih")
	lawRefs(t, store, billID(6), v6["ih"], ref(secNotLoaded, model.LawRefAdds))
	testdb.SeedCongressionalVote(ctx, t, client, "vote-6", new(billID(6)), testdb.FixtureCongress, "House",
		summaryNow())
	v7 := seedSummaryBill(t, store, client, testdb.FixtureCongress, 7, 2, []string{"ih"}, "ih")
	lawRefs(t, store, billID(7), v7["ih"], ref(secFlag, model.LawRefAmends))

	assertRows(t, lawQueue(t, store, nil), []string{
		billID(6) + "|hash-ih", billID(7) + "|hash-ih", billID(1) + "|hash-rh",
	})
	assertRows(t, lawQueue(t, store, func(q *repository.LawChangeQueueQuery) { q.Limit = 1 }),
		[]string{billID(6) + "|hash-ih"})
	prev := func(q *repository.LawChangeQueueQuery) { q.Congress = testdb.FixturePrevCongress }
	assertRows(t, lawQueue(t, store, prev), []string{})
}

// setLawStatus sets the bill's status, dated daysAgo before summaryNow (no date if negative), and
// lists laws as the laws it became when given.
func setLawStatus(t *testing.T, store *spannerdb.PipelineStoreImpl, number int, status string, daysAgo int,
	laws ...model.BillLaw,
) {
	t.Helper()
	ctx := t.Context()
	var date *time.Time
	if daysAgo >= 0 {
		date = new(summaryNow().AddDate(0, 0, -daysAgo))
	}
	if err := store.UpdateBillStatus(ctx, billID(number), status, date); err != nil {
		t.Fatalf("UpdateBillStatus %d: %v", number, err)
	}
	if len(laws) == 0 {
		return
	}
	if err := store.UpsertBill(ctx, repository.BillRow{
		ID: billID(number), Congress: testdb.FixtureCongress, BillType: "hr", Number: number,
		Title: "Bill " + strconv.Itoa(number), Laws: laws,
	}); err != nil {
		t.Fatalf("UpsertBill %d: %v", number, err)
	}
}

// Laws come first, newest first whether or not they had a roll call, then the bills that passed
// both chambers, then the rest in the order from before tiers (#748).
func TestQueryBillsToExplainLaw_LawsFirst(t *testing.T) {
	store, client := newLawChangeStore(t)
	ctx := t.Context()
	vote := func(number int) {
		testdb.SeedCongressionalVote(ctx, t, client, "vote-"+strconv.Itoa(number), new(billID(number)),
			testdb.FixtureCongress, "House", summaryNow())
	}
	for n := 1; n <= 10; n++ {
		v := seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 0, []string{"enr"}, "enr")
		lawRefs(t, store, billID(n), v["enr"], ref(secPhysicians, model.LawRefAmends))
	}
	law := model.BillLaw{Type: model.BillLawTypePublic, Number: "119-95"}
	setLawStatus(t, store, 1, "passed_house", 1) // the newest bill, voted, not law
	vote(1)
	setLawStatus(t, store, 2, "became_law", 30) // a voice-voted law
	setLawStatus(t, store, 3, "became_law", 60) // an older law with a roll call
	vote(3)
	setLawStatus(t, store, 4, "became_law", 10) // law over a veto
	vote(4)
	setLawStatus(t, store, 5, "signed", 5, law) // law, its status not re-derived yet
	setLawStatus(t, store, 6, "vetoed", 3)      // vetoed, not overridden
	vote(6)
	setLawStatus(t, store, 7, "to_president", 20)
	setLawStatus(t, store, 8, "became_law", -1) // a law with no status date
	setLawStatus(t, store, 9, "reported", 2)
	setLawStatus(t, store, 10, "became_law", 30) // enacted the same day as 2

	want := []string{
		billID(5) + "|hash-enr", billID(4) + "|hash-enr", billID(10) + "|hash-enr", billID(2) + "|hash-enr",
		billID(3) + "|hash-enr", billID(8) + "|hash-enr",
		billID(6) + "|hash-enr", billID(7) + "|hash-enr",
		billID(1) + "|hash-enr", billID(9) + "|hash-enr",
	}
	assertRows(t, lawQueue(t, store, nil), want)
	assertRows(t, lawQueue(t, store, nil), want)
	limit := func(q *repository.LawChangeQueueQuery) { q.Limit = 3 }
	assertRows(t, lawQueue(t, store, limit), want[:3])
	n, err := store.CountBillsToExplainLaw(ctx, repository.LawChangeQueueQuery{
		Congress: testdb.FixtureCongress, Limit: 3, PromptVersion: lawPrompt, Model: summaryModel, Now: summaryNow(),
	})
	if err != nil || n != len(want) {
		t.Errorf("CountBillsToExplainLaw with limit 3 = %d, %v; want %d", n, err, len(want))
	}
}

// A bill whose only changes are to "nonusc:" laws isn't queued: the site shows no explanation
// for them (#538). One that also changes the US Code, or a statutory note, is.
func TestQueryBillsToExplainLaw_NonUSC(t *testing.T) {
	const (
		nonUSC = model.NonUSCSectionPrefix + "Section 4 of the Example Act"
		note   = "/us/usc/t10/s4271/note"
	)
	tests := []struct {
		name   string
		refs   []repository.BillLawRefRow
		queued bool
	}{
		{"only a nonusc law", []repository.BillLawRefRow{ref(nonUSC, model.LawRefAmends)}, false},
		{
			"a nonusc law and a cited section",
			[]repository.BillLawRefRow{ref(nonUSC, model.LawRefRepeals), ref(secFlag, model.LawRefCites)}, false,
		},
		{
			"a nonusc law and a US Code section",
			[]repository.BillLawRefRow{ref(nonUSC, model.LawRefAmends), ref(secPhysicians, model.LawRefAmends)}, true,
		},
		{"a statutory note", []repository.BillLawRefRow{ref(note, model.LawRefAmends)}, true},
	}
	store, client := newLawChangeStore(t)
	var want []string
	for i, tt := range tests {
		number := i + 1
		v := seedSummaryBill(t, store, client, testdb.FixtureCongress, number, 10+i, []string{"ih"}, "ih")
		lawRefs(t, store, billID(number), v["ih"], tt.refs...)
		if tt.queued {
			want = append(want, billID(number)+"|hash-ih")
		}
	}
	got := lawQueue(t, store, nil)
	for i, tt := range tests {
		if queued := slices.Contains(got, billID(i+1)+"|hash-ih"); queued != tt.queued {
			t.Errorf("%s: queued = %v, want %v", tt.name, queued, tt.queued)
		}
	}
	assertRows(t, got, want)
}

func TestQueryBillsToExplainLaw_Explained(t *testing.T) {
	store, client := newLawChangeStore(t)
	ctx := t.Context()
	v := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"ih"}, "ih")
	lawRefs(t, store, billID(1), v["ih"], ref(secPhysicians, model.LawRefAmends), ref(secNotLoaded, model.LawRefAdds))

	a := lawAttempt(billID(1), "hash-ih", repository.SummaryOutcomeOK, summaryNow())
	a.Changes = []repository.BillLawChangeRow{
		lawChange(secPhysicians, model.LawRefAmends, "hash-ih"), lawChange(secNotLoaded, model.LawRefAdds, "hash-ih"),
	}
	if err := store.RecordLawChangeAttempt(ctx, a); err != nil {
		t.Fatalf("RecordLawChangeAttempt: %v", err)
	}
	assertRows(t, lawQueue(t, store, nil), []string{})

	// Another prompt version makes it due; so does another model only through the attempt.
	assertRows(t, lawQueue(t, store, func(q *repository.LawChangeQueueQuery) { q.PromptVersion = "law-v2" }),
		[]string{billID(1) + "|hash-ih"})
	assertRows(t, lawQueue(t, store, func(q *repository.LawChangeQueueQuery) { q.Model = "gemini-3.5-flash" }),
		[]string{})

	// A section the bill doesn't change is rewritten: still explained.
	loadSections(t, store, secFlag)
	assertRows(t, lawQueue(t, store, nil), []string{})
	// A section it changes is rewritten by a new release point: due again.
	loadSections(t, store, secPhysicians)
	assertRows(t, lawQueue(t, store, nil), []string{billID(1) + "|hash-ih"})
}

func TestQueryBillsToExplainLaw_NewText(t *testing.T) {
	store, client := newLawChangeStore(t)
	v := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"ih"}, "ih")
	lawRefs(t, store, billID(1), v["ih"], ref(secPhysicians, model.LawRefAmends))
	a := lawAttempt(billID(1), "hash-old", repository.SummaryOutcomeOK, summaryNow())
	a.Changes = []repository.BillLawChangeRow{lawChange(secPhysicians, model.LawRefAmends, "hash-old")}
	if err := store.RecordLawChangeAttempt(t.Context(), a); err != nil {
		t.Fatalf("RecordLawChangeAttempt: %v", err)
	}
	assertRows(t, lawQueue(t, store, nil), []string{billID(1) + "|hash-ih"})
}

func TestQueryBillsToExplainLaw_Attempts(t *testing.T) {
	store, client := newLawChangeStore(t)
	for n := 1; n <= 5; n++ {
		v := seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 100, []string{"ih"}, "ih")
		lawRefs(t, store, billID(n), v["ih"], ref(secPhysicians, model.LawRefAmends))
	}
	ctx := t.Context()
	anHourAgo := summaryNow().Add(-90 * time.Minute)
	blockedOtherModel := lawAttempt(billID(4), "hash-ih", repository.SummaryOutcomeBlocked, anHourAgo)
	blockedOtherModel.Model = "gemini-3.5-flash"
	for _, a := range []repository.LawChangeAttemptRow{
		lawAttempt(billID(1), "hash-ih", repository.SummaryOutcomeBlocked, anHourAgo),
		lawAttempt(billID(2), "hash-ih", repository.SummaryOutcomeError, summaryNow().Add(-time.Minute)),
		lawAttempt(billID(3), "hash-ih", repository.SummaryOutcomeInvalid, anHourAgo), // 1 h wait is over
		blockedOtherModel,
		lawAttempt(billID(5), "hash-old", repository.SummaryOutcomeBlocked, anHourAgo), // other text
	} {
		if err := store.RecordLawChangeAttempt(ctx, a); err != nil {
			t.Fatalf("RecordLawChangeAttempt: %v", err)
		}
	}

	assertRows(t, lawQueue(t, store, nil), []string{
		billID(3) + "|hash-ih", billID(4) + "|hash-ih", billID(5) + "|hash-ih",
	})
	later := func(q *repository.LawChangeQueueQuery) { q.Now = summaryNow().Add(2 * time.Hour) }
	assertRows(t, lawQueue(t, store, later), []string{
		billID(2) + "|hash-ih", billID(3) + "|hash-ih", billID(4) + "|hash-ih", billID(5) + "|hash-ih",
	})
}

func lawAttemptRow(t *testing.T, client *spanner.Client, bill string) string {
	t.Helper()
	rows := queryStrings(t, client, `SELECT outcome, CAST(attempts AS STRING),
		IFNULL(FORMAT_TIMESTAMP('%FT%TZ', next_attempt_at, 'UTC'), '')
		FROM law_change_attempts WHERE bill_id = @bill`, map[string]any{"bill": bill})
	if len(rows) != 1 {
		t.Fatalf("attempt rows = %q, want one", rows)
	}
	return rows[0]
}

func TestRecordLawChangeAttempt(t *testing.T) {
	store, client := newLawChangeStore(t)
	ctx := t.Context()
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"ih"}, "ih")
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 2, 10, []string{"ih"}, "ih")
	bill := billID(1)
	at := time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)
	changesQuery := `SELECT section_id, change_kind, explanation, source_content_hash, release_point,
		prompt_version FROM bill_law_changes WHERE bill_id = @bill ORDER BY section_id`

	first := lawAttempt(bill, "hash-ih", repository.SummaryOutcomeOK, at)
	first.Changes = []repository.BillLawChangeRow{
		lawChange(secPhysicians, model.LawRefAmends, "hash-ih"), lawChange(secFlag, model.LawRefRepeals, "hash-ih"),
	}
	if err := store.RecordLawChangeAttempt(ctx, first); err != nil {
		t.Fatalf("record ok: %v", err)
	}
	if got := lawAttemptRow(t, client, bill); got != "ok|0|" {
		t.Errorf("attempt = %q, want ok|0|", got)
	}

	// A failure keeps the stored explanations and waits an hour; the next ok replaces them.
	failed := lawAttempt(bill, "hash-ih", repository.SummaryOutcomeError, at)
	if err := store.RecordLawChangeAttempt(ctx, failed); err != nil {
		t.Fatalf("record error: %v", err)
	}
	if got := lawAttemptRow(t, client, bill); got != "error|1|2026-09-27T01:00:00Z" {
		t.Errorf("attempt = %q, want error|1|2026-09-27T01:00:00Z", got)
	}
	assertRows(t, queryStrings(t, client, changesQuery, map[string]any{"bill": bill}), []string{
		secFlag + "|repeals|It would change " + secFlag + "|hash-ih|" + releasePoint + "|" + lawPrompt,
		secPhysicians + "|amends|It would change " + secPhysicians + "|hash-ih|" + releasePoint + "|" + lawPrompt,
	})

	notExplained := lawChange(secPhysicians, model.LawRefAmends, "hash-2")
	notExplained.Explanation = ""
	second := lawAttempt(bill, "hash-2", repository.SummaryOutcomeOK, at.Add(time.Hour))
	second.Changes = []repository.BillLawChangeRow{notExplained}
	if err := store.RecordLawChangeAttempt(ctx, second); err != nil {
		t.Fatalf("record second ok: %v", err)
	}
	assertRows(t, queryStrings(t, client, changesQuery, map[string]any{"bill": bill}), []string{
		secPhysicians + "|amends||hash-2|" + releasePoint + "|" + lawPrompt,
	})

	// An unknown kind fails the whole transaction.
	bad := lawAttempt(billID(2), "hash-ih", repository.SummaryOutcomeOK, at)
	bad.Changes = []repository.BillLawChangeRow{lawChange(secFlag, model.LawRefCites, "hash-ih")}
	if err := store.RecordLawChangeAttempt(ctx, bad); err == nil {
		t.Error("record with a cites change: want an error")
	}
	assertRows(t, queryStrings(t, client, `SELECT bill_id FROM law_change_attempts WHERE bill_id = @bill`,
		map[string]any{"bill": billID(2)}), nil)

	n, err := store.CountLawChangeAttemptsSince(ctx, at.Add(30*time.Minute))
	if err != nil || n != 1 {
		t.Errorf("CountLawChangeAttemptsSince = %d, %v; want 1", n, err)
	}
	if n, err = store.CountLawChangeAttemptsSince(ctx, at.Add(2*time.Hour)); err != nil || n != 0 {
		t.Errorf("CountLawChangeAttemptsSince later = %d, %v; want 0", n, err)
	}
}

func TestLoadLawChangeContext(t *testing.T) {
	store, client := newLawChangeStore(t)
	ctx := t.Context()
	v := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"ih"}, "ih")
	lawRefs(t, store, billID(1), v["ih"],
		repository.BillLawRefRow{
			SectionID: secPhysicians, RefKind: model.LawRefAmends, SubsectionPath: new("(t)"),
			Instruction: new(`is amended by striking "2025"`), BillSectionRef: new("B"),
		},
		repository.BillLawRefRow{SectionID: secFlag, RefKind: model.LawRefCites, BillSectionRef: new("A")},
		repository.BillLawRefRow{SectionID: secNotLoaded, RefKind: model.LawRefAdds, BillSectionRef: new("A")},
		repository.BillLawRefRow{SectionID: secFlag, RefKind: model.LawRefRepeals, BillSectionRef: new("C")},
	)
	if err := store.UpsertBillSummary(ctx, summary(billID(1), "hash-ih", summaryPrompt)); err != nil {
		t.Fatalf("UpsertBillSummary: %v", err)
	}

	got, err := store.LoadLawChangeContext(ctx, billID(1), v["ih"])
	if err != nil {
		t.Fatalf("LoadLawChangeContext: %v", err)
	}
	if got.BillID != billID(1) || got.Congress != testdb.FixtureCongress || got.BillType != "hr" || got.Number != 1 ||
		got.Title != "Bill 1" || got.ShortSummary != "short" || got.VersionCode != "ih" || got.ContentHash != "hash-ih" {
		t.Errorf("context = %+v", got)
	}
	want := []repository.LawChangeRef{
		{SectionID: secNotLoaded, RefKind: model.LawRefAdds, BillSectionRef: "A"},
		{
			SectionID: secPhysicians, RefKind: model.LawRefAmends, SubsectionPath: "(t)",
			Instruction: `is amended by striking "2025"`, BillSectionRef: "B", Loaded: true,
			Heading: "Heading of " + secPhysicians, CurrentText: "(a) In general.—Heading of " + secPhysicians,
		},
		{
			SectionID: secFlag, RefKind: model.LawRefRepeals, BillSectionRef: "C", Loaded: true,
			Heading: "Heading of " + secFlag, CurrentText: "(a) In general.—Heading of " + secFlag,
		},
	}
	if len(got.Refs) != len(want) {
		t.Fatalf("refs = %+v, want %+v", got.Refs, want)
	}
	for i := range want {
		if got.Refs[i] != want[i] {
			t.Errorf("ref %d = %+v, want %+v", i, got.Refs[i], want[i])
		}
	}

	if _, err = store.LoadLawChangeContext(ctx, billID(1), "missing"); !errors.Is(err, spanner.ErrRowNotFound) {
		t.Errorf("missing version: err = %v, want ErrRowNotFound", err)
	}
}
