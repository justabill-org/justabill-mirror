package spannerdb_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const (
	summaryPrompt = "bill-v2"
	summaryModel  = "gemini-3.8-flash"
)

// summaryNow is the queue's clock in these tests.
func summaryNow() time.Time { return time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC) }

func newSummaryStore(t *testing.T) (*spannerdb.PipelineStoreImpl, *spanner.Client) {
	t.Helper()
	client := testdb.New(t)
	testdb.SeedCongress(t.Context(), t, client, testdb.FixtureCongress)
	testdb.SeedCongress(t.Context(), t, client, testdb.FixturePrevCongress)
	return spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: client}), client
}

// seedSummaryBill stores an H.R. bill with a status date daysAgo before summaryNow (none if
// negative) and text versions with the given codes, listed newest first as Congress.gov lists
// them. sort_order is chronological, as the pipeline stores it since #79 (1 is the oldest), so
// the first code is the latest. Versions listed in withText get a bill_texts row
// whose hash is "hash-<code>". It returns the version IDs by code.
func seedSummaryBill(
	t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client, congress, number, daysAgo int,
	codes []string,
	withText ...string,
) map[string]string {
	t.Helper()
	ctx := t.Context()
	id := "hr-" + strconv.Itoa(congress) + "-" + strconv.Itoa(number)
	if err := store.UpsertBill(ctx, repository.BillRow{
		ID: id, Congress: congress, BillType: "hr", Number: number, Title: "Bill " + strconv.Itoa(number),
	}); err != nil {
		t.Fatalf("upsert bill: %v", err)
	}
	if daysAgo >= 0 {
		date := summaryNow().AddDate(0, 0, -daysAgo)
		if err := store.UpdateBillStatus(ctx, id, "Introduced", &date); err != nil {
			t.Fatalf("update status: %v", err)
		}
	}
	rows := make([]repository.TextVersionRow, 0, len(codes))
	for i, code := range codes {
		rows = append(rows, textVersion(id, "Version "+code, code, len(codes)-i))
	}
	if len(rows) > 0 {
		upsertVersions(t, store, id, rows...)
	}
	ids := versionIDs(t, client, id)
	for _, code := range withText {
		if err := store.InsertBillText(ctx, repository.BillTextRow{
			TextVersionID: ids[code], Format: "xml", Content: "<bill>" + code + "</bill>",
			ContentHash: "hash-" + code, FetchedAt: summaryNow(),
		}); err != nil {
			t.Fatalf("insert text: %v", err)
		}
	}
	return ids
}

func billID(number int) string { return "hr-119-" + strconv.Itoa(number) }

func queue(t *testing.T, store *spannerdb.PipelineStoreImpl, mod func(*repository.SummaryQueueQuery)) []string {
	t.Helper()
	q := repository.SummaryQueueQuery{
		Congress: testdb.FixtureCongress, Limit: 100, PromptVersion: summaryPrompt, Model: summaryModel,
		Now: summaryNow(), RecentDays: 30,
	}
	if mod != nil {
		mod(&q)
	}
	items, err := store.QueryBillsToSummarize(t.Context(), q)
	if err != nil {
		t.Fatalf("query bills to summarize: %v", err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.BillID+"|"+it.VersionCode+"|"+it.ContentHash+"|"+strconv.Itoa(it.Tier))
	}
	return out
}

func summary(bill, hash, prompt string) repository.BillSummaryRow {
	return repository.BillSummaryRow{
		BillID: bill, ShortSummary: "short", LongSummary: "long", WhoItAffects: "who", ModelUsed: summaryModel,
		SourceContentHash: hash, PromptVersion: prompt,
	}
}

func attempt(bill, hash, outcome string, at time.Time) repository.SummaryAttemptRow {
	return repository.SummaryAttemptRow{
		BillID: bill, ContentHash: hash, PromptVersion: summaryPrompt, Model: summaryModel,
		Outcome: outcome, Reason: "r", AttemptedAt: at,
	}
}

func TestQueryBillsToSummarize_LatestVersionWithText(t *testing.T) {
	store, client := newSummaryStore(t)
	// "enr" is the latest version but has no text yet, so the queue takes "rh".
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 100, []string{"enr", "rh", "ih"}, "rh", "ih")
	// No text at all: nothing to summarize.
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 2, 100, []string{"ih"})
	// Another congress.
	seedSummaryBill(t, store, client, testdb.FixturePrevCongress, 3, 100, []string{"ih"}, "ih")

	assertRows(t, queue(t, store, nil), []string{billID(1) + "|rh|hash-rh|2"})
}

func TestQueryBillsToSummarize_StaleWhenHashDiffers(t *testing.T) {
	store, client := newSummaryStore(t)
	for n := 1; n <= 4; n++ {
		seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 100, []string{"ih"}, "ih")
	}
	ctx := t.Context()
	for _, s := range []repository.BillSummaryRow{
		summary(billID(1), "hash-ih", summaryPrompt),  // current: not due
		summary(billID(2), "hash-old", summaryPrompt), // older text: due
		summary(billID(3), "", ""),                    // NULL hash (pre-#194 summary): due
	} {
		if err := store.UpsertBillSummary(ctx, s); err != nil {
			t.Fatalf("upsert summary: %v", err)
		}
	}

	assertRows(t, queue(t, store, nil), []string{
		billID(2) + "|ih|hash-ih|2", billID(3) + "|ih|hash-ih|2", billID(4) + "|ih|hash-ih|2",
	})
}

func TestQueryBillsToSummarize_Attempts(t *testing.T) {
	store, client := newSummaryStore(t)
	for n := 1; n <= 5; n++ {
		seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 100, []string{"ih"}, "ih")
	}
	ctx := t.Context()
	anHourAgo := summaryNow().Add(-90 * time.Minute)
	blockedOtherModel := attempt(billID(4), "hash-ih", repository.SummaryOutcomeBlocked, anHourAgo)
	blockedOtherModel.Model = "gemini-3.5-flash"
	for _, a := range []repository.SummaryAttemptRow{
		attempt(billID(1), "hash-ih", repository.SummaryOutcomeBlocked, anHourAgo),
		attempt(billID(2), "hash-ih", repository.SummaryOutcomeError, summaryNow().Add(-time.Minute)),
		attempt(billID(3), "hash-ih", repository.SummaryOutcomeInvalid, anHourAgo), // 1 h wait is over
		blockedOtherModel,
		attempt(billID(5), "hash-old", repository.SummaryOutcomeBlocked, anHourAgo), // other text
	} {
		if err := store.RecordSummaryAttempt(ctx, a); err != nil {
			t.Fatalf("record attempt: %v", err)
		}
	}

	assertRows(t, queue(t, store, nil), []string{
		billID(3) + "|ih|hash-ih|2", billID(4) + "|ih|hash-ih|2", billID(5) + "|ih|hash-ih|2",
	})
	// Once the error's hour has passed it's due again; the block never is.
	later := func(q *repository.SummaryQueueQuery) { q.Now = summaryNow().Add(2 * time.Hour) }
	assertRows(t, queue(t, store, later), []string{
		billID(2) + "|ih|hash-ih|2", billID(3) + "|ih|hash-ih|2", billID(4) + "|ih|hash-ih|2",
		billID(5) + "|ih|hash-ih|2",
	})
}

func TestQueryBillsToSummarize_TierOrder(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 200, []string{"ih"}, "ih") // other, older
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 2, 5, []string{"ih"}, "ih")   // recent
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 3, 300, []string{"eh"}, "eh") // voted, old
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 4, 20, []string{"ih"}, "ih")  // recent, older
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 5, 100, []string{"ih"}, "ih") // other, newer
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 6, -1, []string{"ih"}, "ih")  // no status date
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 7, 1, []string{"ih"}, "ih")   // old prompt only
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 8, 400, []string{"ih"}, "ih") // voted, older
	bill3, bill8 := billID(3), billID(8)
	testdb.SeedCongressionalVote(ctx, t, client, "house-119-s1-roll101", &bill3, testdb.FixtureCongress,
		"House", summaryNow())
	testdb.SeedCongressionalVote(ctx, t, client, "house-119-s1-roll102", &bill8, testdb.FixtureCongress,
		"House", summaryNow())
	if err := store.UpsertBillSummary(ctx, summary(billID(7), "hash-ih", "bill-v1")); err != nil {
		t.Fatalf("upsert summary: %v", err)
	}

	want := []string{
		billID(3) + "|eh|hash-eh|0", billID(8) + "|ih|hash-ih|0",
		billID(2) + "|ih|hash-ih|1", billID(4) + "|ih|hash-ih|1",
		billID(5) + "|ih|hash-ih|2", billID(1) + "|ih|hash-ih|2", billID(6) + "|ih|hash-ih|2",
	}
	assertRows(t, queue(t, store, nil), want)

	onPromptChange := func(q *repository.SummaryQueueQuery) { q.ResummarizeOnPromptChange = true }
	assertRows(t, queue(t, store, onPromptChange), append(want, billID(7)+"|ih|hash-ih|3"))

	limited := func(q *repository.SummaryQueueQuery) { q.Limit = 3 }
	assertRows(t, queue(t, store, limited), want[:3])

	backlog := func(mod func(*repository.SummaryQueueQuery)) map[int]int {
		t.Helper()
		q := repository.SummaryQueueQuery{
			Congress: testdb.FixtureCongress, Limit: 1, PromptVersion: summaryPrompt, Model: summaryModel,
			Now: summaryNow(), RecentDays: 30,
		}
		if mod != nil {
			mod(&q)
		}
		got, err := store.CountBillsToSummarize(ctx, q)
		if err != nil {
			t.Fatalf("count bills to summarize: %v", err)
		}
		return got
	}
	wantCounts := map[int]int{
		repository.SummaryTierVoted: 2, repository.SummaryTierRecent: 2, repository.SummaryTierOther: 3,
	}
	if got := backlog(nil); !reflect.DeepEqual(got, wantCounts) {
		t.Errorf("backlog = %v, want %v (the limit doesn't apply)", got, wantCounts)
	}
	wantCounts[repository.SummaryTierPromptChange] = 1
	if got := backlog(onPromptChange); !reflect.DeepEqual(got, wantCounts) {
		t.Errorf("backlog with prompt changes = %v, want %v", got, wantCounts)
	}
}

func TestCountSummaryAttemptsSince(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	midnight := time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)
	for n, at := range []time.Time{midnight.Add(-time.Second), midnight, midnight.Add(3 * time.Hour)} {
		seedSummaryBill(t, store, client, testdb.FixtureCongress, n+1, 10, nil)
		if err := store.RecordSummaryAttempt(
			ctx,
			attempt(billID(n+1), "h", repository.SummaryOutcomeOK, at),
		); err != nil {
			t.Fatalf("record attempt: %v", err)
		}
	}

	got, err := store.CountSummaryAttemptsSince(ctx, midnight)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 2 {
		t.Errorf("attempts since midnight = %d, want 2", got)
	}
}

func TestLoadBillContext(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	ids := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"rh", "ih"}, "rh", "ih")
	bill := billID(1)
	policy := "Taxation"
	if err := store.UpsertBill(ctx, repository.BillRow{
		ID: bill, Congress: testdb.FixtureCongress, BillType: "hr", Number: 1, Title: "Bill 1",
		PolicyArea:   &policy,
		LatestAction: json.RawMessage(`{"actionDate":"2026-09-17","text":"Reported by the Committee."}`),
	}); err != nil {
		t.Fatalf("upsert bill: %v", err)
	}
	if err := store.ReplaceBillCommittees(ctx, bill, []repository.BillCommitteeRow{
		{CommitteeID: "hswm00", CommitteeName: "Ways and Means Committee", Activity: "Referred to"},
		{CommitteeID: "hswm00", CommitteeName: "Ways and Means Committee", Activity: "Markup by"},
		{CommitteeID: "hsag00", CommitteeName: "Agriculture Committee", Activity: "Referred to"},
	}); err != nil {
		t.Fatalf("replace committees: %v", err)
	}
	if err := store.ReplaceBillSubjects(ctx, bill, []string{"Taxation", "Income tax credits"}); err != nil {
		t.Fatalf("replace subjects: %v", err)
	}

	got, err := store.LoadBillContext(ctx, bill, ids["rh"])
	if err != nil {
		t.Fatalf("load bill context: %v", err)
	}
	statusDate := time.Date(2026, time.September, 17, 0, 0, 0, 0, time.UTC)
	want := &repository.SummaryBillContext{
		BillID: bill, Congress: testdb.FixtureCongress, BillType: "hr", Number: 1, Title: "Bill 1",
		PolicyArea: policy, Status: "Introduced", StatusDate: &statusDate,
		LatestAction: "Reported by the Committee.",
		Committees:   []string{"Agriculture Committee", "Ways and Means Committee"},
		Subjects:     []string{"Income tax credits", "Taxation"},
		VersionID:    ids["rh"], VersionCode: "rh", VersionName: "Version rh",
		ContentHash: "hash-rh", Text: "<bill>rh</bill>",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bill context = %+v\nwant %+v", got, want)
	}

	if _, err = store.LoadBillContext(ctx, bill, "no-such-version"); !errors.Is(err, spanner.ErrRowNotFound) {
		t.Errorf("missing version error = %v, want ErrRowNotFound", err)
	}
}

func TestUpsertBillSummary_Provenance(t *testing.T) {
	store, client := newSummaryStore(t)
	ids := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"ih"}, "ih")
	bill := billID(1)
	row := repository.BillSummaryRow{
		BillID: bill, ShortSummary: "short", LongSummary: "long", WhoItAffects: "who", ModelUsed: summaryModel,
		SourceVersionID: ids["ih"], SourceVersionCode: "ih", SourceContentHash: "hash-ih", SourceCRSHash: "crs-hash",
		PromptVersion: summaryPrompt, ModelVersion: "gemini-3.8-flash-001", InputTruncated: true,
		InputTokens: 1200, OutputTokens: 300, ThinkingTokens: 7,
	}
	if err := store.UpsertBillSummary(t.Context(), row); err != nil {
		t.Fatalf("upsert summary: %v", err)
	}

	assertRows(t, queryStrings(t, client, `SELECT short_summary, long_summary, why_it_matters, model_used,
		source_version_id, source_version_code, source_content_hash, prompt_version, model_version,
		CAST(input_truncated AS STRING), CAST(input_tokens AS STRING), CAST(output_tokens AS STRING),
		CAST(thinking_tokens AS STRING), source_crs_hash
		FROM bill_summaries WHERE bill_id = @bill`, map[string]any{"bill": bill}),
		[]string{"short|long|who|" + summaryModel + "|" + ids["ih"] +
			"|ih|hash-ih|bill-v2|gemini-3.8-flash-001|true|1200|300|7|crs-hash"})

	// WhoItAffects round-trips through the why_it_matters column (design 199).
	got, err := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client}).GetSummary(t.Context(), bill)
	if err != nil || got == nil || got.WhoItAffects == nil || *got.WhoItAffects != "who" {
		t.Errorf("GetSummary = %+v, %v; want WhoItAffects %q", got, err, "who")
	}
}

// attemptRow reads a bill's attempt as outcome|attempts|next_attempt_at (RFC 3339, or "").
func attemptRow(t *testing.T, client *spanner.Client, bill string) string {
	t.Helper()
	rows := queryStrings(t, client, `SELECT outcome, CAST(attempts AS STRING),
		IFNULL(FORMAT_TIMESTAMP('%FT%TZ', next_attempt_at, 'UTC'), '')
		FROM summary_attempts WHERE bill_id = @bill`, map[string]any{"bill": bill})
	if len(rows) != 1 {
		t.Fatalf("attempt rows = %q, want one", rows)
	}
	return rows[0]
}

func TestRecordSummaryAttempt_BackoffAndReset(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"ih"}, "ih")
	bill := billID(1)
	at := time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)
	record := func(a repository.SummaryAttemptRow) string {
		t.Helper()
		if err := store.RecordSummaryAttempt(ctx, a); err != nil {
			t.Fatalf("record attempt: %v", err)
		}
		return attemptRow(t, client, bill)
	}

	steps := []struct {
		name string
		row  repository.SummaryAttemptRow
		want string
	}{
		{"first error waits 1 h", attempt(bill, "h1", repository.SummaryOutcomeError, at),
			"error|1|2026-09-27T01:00:00Z"},
		{"second failure waits 4 h", attempt(bill, "h1", repository.SummaryOutcomeInvalid, at),
			"invalid|2|2026-09-27T04:00:00Z"},
		{"third waits 24 h", attempt(bill, "h1", repository.SummaryOutcomeTruncatedOutput, at),
			"truncated_output|3|2026-09-28T00:00:00Z"},
		{"then every 24 h", attempt(bill, "h1", repository.SummaryOutcomeError, at),
			"error|4|2026-09-28T00:00:00Z"},
		{"new text restarts the count", attempt(bill, "h2", repository.SummaryOutcomeError, at),
			"error|1|2026-09-27T01:00:00Z"},
		{"blocked is never retried", attempt(bill, "h2", repository.SummaryOutcomeBlocked, at),
			"blocked|2|"},
		{"success resets", attempt(bill, "h2", repository.SummaryOutcomeOK, at), "ok|0|"},
	}
	for _, step := range steps {
		if got := record(step.row); got != step.want {
			t.Errorf("%s: attempt = %q, want %q", step.name, got, step.want)
		}
	}
}

func TestRecordSummaryAttempt_WritesSummary(t *testing.T) {
	store, client := newSummaryStore(t)
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"ih"}, "ih")
	bill := billID(1)
	s := summary(bill, "hash-ih", summaryPrompt)
	a := attempt(bill, "hash-ih", repository.SummaryOutcomeOK, summaryNow())
	a.Summary = &s
	if err := store.RecordSummaryAttempt(t.Context(), a); err != nil {
		t.Fatalf("record attempt: %v", err)
	}

	assertRows(t, queryStrings(t, client, `SELECT short_summary, source_content_hash,
		FORMAT_TIMESTAMP('%FT%TZ', generated_at, 'UTC') FROM bill_summaries WHERE bill_id = @bill`,
		map[string]any{"bill": bill}), []string{"short|hash-ih|2026-09-27T12:00:00Z"})
	assertRows(t, queue(t, store, nil), []string{})
}

func TestQueryBillsToSummarize_PassedChamber(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	for n := 1; n <= 6; n++ {
		seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 100, []string{"ih"}, "ih")
	}
	code := func(c string) *string { return &c }
	date := summaryNow().AddDate(0, 0, -50)
	actions := map[int][]repository.BillActionRow{
		// A Library of Congress passage code, with a text the status history misses (#659).
		1: {{ActionDate: date, ActionText: "On motion to suspend the rules and pass the bill Agreed to by voice vote.",
			ActionCode: code("8000"), SortOrder: 1}},
		// No code (another source): the text says it passed.
		2: {
			{
				ActionDate: date,
				ActionText: "Passed/agreed to in Senate: Passed Senate without amendment by Unanimous Consent.",
				SortOrder:  1,
			},
		},
		// Failed in the House (9000) and only referred: not passed.
		4: {
			{
				ActionDate: date,
				ActionText: "Referred to the Committee on Agriculture.",
				ActionCode: code("H11100"),
				SortOrder:  1,
			},
			{
				ActionDate: date,
				ActionText: "Failed of passage/not agreed to in House.",
				ActionCode: code("9000"),
				SortOrder:  2,
			},
		},
		// Became law, by code.
		5: {{ActionDate: date, ActionText: "Became Public Law No: 119-1.", ActionCode: code("36000"), SortOrder: 1}},
	}
	for n, rows := range actions {
		if err := store.ReplaceBillActions(ctx, billID(n), rows); err != nil {
			t.Fatalf("replace actions: %v", err)
		}
	}
	// A passed_house status with no stored actions. The bill row keeps its seeded status and
	// date, so only the history differs.
	seeded := summaryNow().AddDate(0, 0, -100)
	for _, st := range []repository.BillStatusRow{
		{BillID: billID(3), Status: "passed_house", StatusDate: date, StatusRank: 4},
		{BillID: billID(6), Status: "reported", StatusDate: date, StatusRank: 3},
	} {
		if err := store.ReplaceBillStatus(
			ctx, st.BillID, "Introduced", &seeded, []repository.BillStatusRow{st},
		); err != nil {
			t.Fatalf("replace status: %v", err)
		}
	}
	// A passed bill whose summary is current isn't redone.
	if err := store.UpsertBillSummary(ctx, summary(billID(5), "hash-ih", summaryPrompt)); err != nil {
		t.Fatalf("upsert summary: %v", err)
	}

	passed := func(q *repository.SummaryQueueQuery) { q.PassedChamber = true }
	assertRows(t, queue(t, store, passed), []string{
		billID(1) + "|ih|hash-ih|2", billID(2) + "|ih|hash-ih|2", billID(3) + "|ih|hash-ih|2",
	})
	if got := len(queue(t, store, nil)); got != 5 {
		t.Errorf("unscoped queue has %d bills, want 5 (all but the summarized one)", got)
	}
	counts, err := store.CountBillsToSummarize(ctx, repository.SummaryQueueQuery{
		Congress: testdb.FixtureCongress, PromptVersion: summaryPrompt, Model: summaryModel,
		Now: summaryNow(), RecentDays: 30, PassedChamber: true,
	})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts[repository.SummaryTierOther] != 3 {
		t.Errorf("passed backlog = %v, want 3 in tier other", counts)
	}
}
