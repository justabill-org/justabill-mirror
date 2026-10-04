package sync

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
)

const (
	lawSecA = "/us/usc/t42/s1395w-4"
	lawSecB = "/us/usc/t4/s1"
	lawRP   = "119-111"
)

// lawStore is a law-change queue in memory. Each bill amends lawSecA (loaded) and adds lawSecB
// (not loaded). Any other PipelineStore method panics through the nil embedded interface.
type lawStore struct {
	repository.PipelineStore

	noReleasePoint bool
	used           int
	items          []repository.LawChangeQueueItem
	loadErr        map[string]error
	// refs replaces a bill's default references.
	refs map[string][]repository.LawChangeRef

	mu       gosync.Mutex
	since    time.Time
	queries  []repository.LawChangeQueueQuery
	attempts []repository.LawChangeAttemptRow
}

func (f *lawStore) CurrentUSCReleasePoint(context.Context) (*model.USCReleasePoint, error) {
	if f.noReleasePoint {
		return nil, nil //nolint:nilnil // the store's "none loaded"
	}
	return &model.USCReleasePoint{ReleasePoint: lawRP}, nil
}

func (f *lawStore) CountLawChangeAttemptsSince(_ context.Context, since time.Time) (int, error) {
	f.since = since
	return f.used, nil
}

func (f *lawStore) CountBillsToExplainLaw(context.Context, repository.LawChangeQueueQuery) (int, error) {
	return len(f.items), nil
}

func (f *lawStore) QueryBillsToExplainLaw(
	_ context.Context, q repository.LawChangeQueueQuery,
) ([]repository.LawChangeQueueItem, error) {
	f.queries = append(f.queries, q)
	return f.items[:min(q.Limit, len(f.items))], nil
}

func (f *lawStore) LoadLawChangeContext(
	_ context.Context, billID, versionID string,
) (*repository.LawChangeContext, error) {
	if err := f.loadErr[billID]; err != nil {
		return nil, err
	}
	refs, ok := f.refs[billID]
	if !ok {
		refs = []repository.LawChangeRef{
			{
				SectionID: lawSecA, RefKind: model.LawRefAmends, SubsectionPath: "(t)", Instruction: "is amended",
				Loaded: true, Heading: "Payment", CurrentText: "Current text of A.",
			},
			{SectionID: lawSecB, RefKind: model.LawRefAdds, Instruction: "by adding a section"},
		}
	}
	return &repository.LawChangeContext{
		BillID: billID, Congress: 119, BillType: "hr", Number: 1, Title: "Title of " + billID,
		ShortSummary: "Short.", VersionID: versionID, VersionCode: "ih", ContentHash: "hash-" + billID,
		Refs: refs,
	}, nil
}

func (f *lawStore) RecordLawChangeAttempt(_ context.Context, a repository.LawChangeAttemptRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, a)
	return nil
}

func (f *lawStore) attemptsByBill() map[string]repository.LawChangeAttemptRow {
	out := make(map[string]repository.LawChangeAttemptRow, len(f.attempts))
	for _, a := range f.attempts {
		out[a.BillID] = a
	}
	return out
}

func lawItems(n int) []repository.LawChangeQueueItem {
	items := make([]repository.LawChangeQueueItem, 0, n)
	for i := 1; i <= n; i++ {
		id := "hr-119-" + strconv.Itoa(i)
		items = append(
			items,
			repository.LawChangeQueueItem{BillID: id, VersionID: "v-" + id, ContentHash: "hash-" + id},
		)
	}
	return items
}

// fakeExplainer answers each bill with the outcome set for it (ok by default; "" for no call),
// explaining lawSecA only, and records the contexts.
type fakeExplainer struct {
	outcomes map[string]ai.Outcome

	mu       gosync.Mutex
	contexts []ai.LawChangeContext
}

func (*fakeExplainer) Config() ai.Config {
	return ai.Config{Model: "gemini-test", RequestType: ai.RequestTypeStandard}
}

func (f *fakeExplainer) ExplainLawChanges(_ context.Context, lc ai.LawChangeContext) (*ai.LawChanges, error) {
	f.mu.Lock()
	f.contexts = append(f.contexts, lc)
	f.mu.Unlock()
	outcome, ok := f.outcomes[lc.BillID]
	if !ok {
		outcome = ai.OutcomeOK
	}
	if outcome == "" {
		return nil, errors.New("bill " + lc.BillID + ": no changes to law to explain")
	}
	out := &ai.LawChanges{Asked: len(lc.Sections)}
	out.Result = ai.Result{
		Outcome: outcome, Model: "gemini-test", ModelVersion: "gemini-test-001", PromptVersion: ai.PromptVersionLaw,
		RequestType: ai.RequestTypeStandard, Usage: ai.Usage{InputTokens: 900, OutputTokens: 200},
	}
	if outcome != ai.OutcomeOK {
		out.Reason = "reason-" + string(outcome)
		return out, &ai.AttemptError{Outcome: outcome, Reason: out.Reason}
	}
	out.Explanations, out.Dropped = map[string]string{lawSecA: "It would change a year."}, 1
	return out, nil
}

func TestSyncLawChanges_BatchIsBoundedByTheCap(t *testing.T) {
	tests := []struct {
		name      string
		used      int
		limit     int
		wantLimit int
	}{
		{"fresh day takes a full batch", 0, 0, DefaultLawChangeBatch},
		{"near the cap takes what's left", 480, 0, 20},
		{"a run limit lowers the batch", 0, 10, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &lawStore{used: tt.used, items: lawItems(100)}
			ex := &fakeExplainer{}
			s, _ := jobService(nil, nil)
			s.store, s.lawExplainer = store, ex

			n, err := s.syncLawChanges(t.Context(), 119, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(store.queries) != 1 || store.queries[0].Limit != tt.wantLimit {
				t.Fatalf("queue queries %+v, want one with limit %d", store.queries, tt.wantLimit)
			}
			q := store.queries[0]
			if q.PromptVersion != ai.PromptVersionLaw || q.Model != "gemini-test" || !q.Now.Equal(jobNow()) {
				t.Errorf("query = %+v", q)
			}
			if n != tt.wantLimit || len(ex.contexts) != tt.wantLimit || len(store.attempts) != tt.wantLimit {
				t.Errorf("explained %d, called %d, recorded %d; want %d", n, len(ex.contexts), len(store.attempts),
					tt.wantLimit)
			}
			if want := time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC); !store.since.Equal(want) {
				t.Errorf("attempts counted since %v, want the UTC day's start %v", store.since, want)
			}
		})
	}
}

func TestSyncLawChanges_CapReached(t *testing.T) {
	store := &lawStore{used: DefaultLawChangeDailyCap, items: lawItems(3)}
	ex := &fakeExplainer{}
	s, logs := jobService(nil, nil)
	s.store, s.lawExplainer = store, ex

	if n, err := s.syncLawChanges(t.Context(), 119, 0); err != nil || n != 0 {
		t.Fatalf("syncLawChanges = %d, %v", n, err)
	}
	if len(store.queries) != 0 || len(ex.contexts) != 0 {
		t.Errorf("queried %d times and called %d times at the cap, want neither", len(store.queries), len(ex.contexts))
	}
	budget := logLines(t, logs, "law_change_budget")
	if len(budget) != 1 || budget[0]["remaining"] != float64(0) ||
		budget[0]["cap"] != float64(DefaultLawChangeDailyCap) {
		t.Errorf("law_change_budget = %v", budget)
	}
	if backlog := logLines(t, logs, "law_change_backlog"); len(backlog) != 1 || backlog[0]["total"] != float64(3) {
		t.Errorf("law_change_backlog = %v", backlog)
	}
}

func TestSyncLawChanges_NoReleasePoint(t *testing.T) {
	store := &lawStore{noReleasePoint: true, items: lawItems(3)}
	ex := &fakeExplainer{}
	s, _ := jobService(nil, nil)
	s.store, s.lawExplainer = store, ex

	if n, err := s.syncLawChanges(t.Context(), 119, 0); err != nil || n != 0 || len(ex.contexts) != 0 {
		t.Errorf("without a US Code: %d explained, %d calls, err %v; want none", n, len(ex.contexts), err)
	}
}

func TestSyncLawChanges_Outcomes(t *testing.T) {
	store := &lawStore{items: lawItems(6)}
	ex := &fakeExplainer{outcomes: map[string]ai.Outcome{
		"hr-119-2": ai.OutcomeBlocked, "hr-119-3": ai.OutcomeTruncatedOutput, "hr-119-4": ai.OutcomeInvalid,
		"hr-119-5": ai.OutcomeError, "hr-119-6": "",
	}}
	s, logs := jobService(nil, nil)
	s.store, s.lawExplainer = store, ex

	n, err := s.syncLawChanges(t.Context(), 119, 0)
	if err != nil || n != 1 {
		t.Fatalf("syncLawChanges = %d, %v; want 1 explained", n, err)
	}
	attempts := store.attemptsByBill()
	wantOutcomes := map[string]string{
		"hr-119-1": "ok", "hr-119-2": "blocked", "hr-119-3": "truncated_output", "hr-119-4": "invalid",
		"hr-119-5": "error", "hr-119-6": "error",
	}
	results := map[string]map[string]any{}
	for _, line := range logLines(t, logs, "law_change_result") {
		bill, _ := line["bill_id"].(string)
		results[bill] = line
	}
	for bill, want := range wantOutcomes {
		a := attempts[bill]
		if a.Outcome != want || a.PromptVersion != ai.PromptVersionLaw || a.Model != "gemini-test" ||
			a.ContentHash != "hash-"+bill || !a.AttemptedAt.Equal(jobNow()) {
			t.Errorf("%s attempt = %+v, want outcome %s", bill, a, want)
		}
		if (want == "ok") != (a.Changes != nil) {
			t.Errorf("%s: changes %v with outcome %s", bill, a.Changes, want)
		}
		line := results[bill]
		if line == nil || line["outcome"] != want {
			t.Errorf("%s: law_change_result = %v, want outcome %s", bill, line, want)
			continue
		}
		wantLevel := "WARN"
		if want == "ok" {
			wantLevel = "INFO"
		}
		if line["level"] != wantLevel || line["sections"] != float64(2) {
			t.Errorf("%s: law_change_result = %v", bill, line)
		}
	}
	if !strings.Contains(attempts["hr-119-6"].Reason, "no changes to law") {
		t.Errorf("no-call reason = %q", attempts["hr-119-6"].Reason)
	}
	ok := results["hr-119-1"]
	if ok["invalid_items"] != float64(1) || ok["explained"] != float64(1) || ok["asked"] != float64(2) ||
		ok["prompt_version"] != ai.PromptVersionLaw || ok["input_tokens"] != float64(900) {
		t.Errorf("ok law_change_result = %v", ok)
	}
}

func TestSyncLawChanges_StoresOneRowPerSection(t *testing.T) {
	store := &lawStore{items: lawItems(1)}
	ex := &fakeExplainer{}
	s, _ := jobService(nil, nil)
	s.store, s.lawExplainer = store, ex

	if _, err := s.syncLawChanges(t.Context(), 119, 0); err != nil {
		t.Fatal(err)
	}
	if len(ex.contexts) != 1 {
		t.Fatalf("calls = %d, want 1", len(ex.contexts))
	}
	in := ex.contexts[0]
	if in.ReleasePoint != lawRP || in.Title != "Title of hr-119-1" || in.ShortSummary != "Short." ||
		len(in.Sections) != 2 || in.Sections[0].CurrentText != "Current text of A." ||
		in.Sections[1].Kind != model.LawRefAdds {
		t.Errorf("explainer input = %+v", in)
	}
	changes := store.attempts[0].Changes
	want := []repository.BillLawChangeRow{
		{
			SectionID: lawSecA, ChangeKind: model.LawRefAmends, Explanation: "It would change a year.",
			SourceContentHash: "hash-hr-119-1", ReleasePoint: lawRP, ModelUsed: "gemini-test",
			PromptVersion: ai.PromptVersionLaw,
		},
		{
			SectionID: lawSecB, ChangeKind: model.LawRefAdds, SourceContentHash: "hash-hr-119-1",
			ReleasePoint: lawRP, ModelUsed: "gemini-test", PromptVersion: ai.PromptVersionLaw,
		},
	}
	if len(changes) != len(want) || changes[0] != want[0] || changes[1] != want[1] {
		t.Errorf("changes = %+v, want %+v", changes, want)
	}
}

// A "nonusc:" law is never sent to the explainer (#538): a bill that also changes the US Code
// stores it without an explanation, and a bill that changes only such laws makes no call and
// records no attempt. A statutory note is US Code material and is sent.
func TestSyncLawChanges_SkipsNonUSCLaws(t *testing.T) {
	const (
		nonUSC = model.NonUSCSectionPrefix + "Section 4 of the Example Act"
		note   = "/us/usc/t10/s4271/note"
	)
	usc := repository.LawChangeRef{SectionID: lawSecA, RefKind: model.LawRefAmends, Instruction: "strike 2025"}
	other := repository.LawChangeRef{SectionID: nonUSC, RefKind: model.LawRefAmends, Instruction: "strike 5"}
	noteRef := repository.LawChangeRef{SectionID: note, RefKind: model.LawRefAmends, Instruction: "strike 7"}
	tests := []struct {
		name      string
		refs      []repository.LawChangeRef
		wantAsked []string // nil: no call
		wantRows  []string // section IDs stored, in order; nil: no attempt
	}{
		{
			name:      "US Code section and nonusc law",
			refs:      []repository.LawChangeRef{other, usc},
			wantAsked: []string{lawSecA},
			wantRows:  []string{nonUSC, lawSecA},
		},
		{
			name:      "statutory note and nonusc law",
			refs:      []repository.LawChangeRef{noteRef, other},
			wantAsked: []string{note},
			wantRows:  []string{note, nonUSC},
		},
		{name: "only nonusc laws", refs: []repository.LawChangeRef{other}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &lawStore{items: lawItems(1), refs: map[string][]repository.LawChangeRef{"hr-119-1": tt.refs}}
			ex := &fakeExplainer{}
			s, _ := jobService(nil, nil)
			s.store, s.lawExplainer = store, ex

			n, err := s.syncLawChanges(t.Context(), 119, 0)
			if err != nil {
				t.Fatal(err)
			}
			checkLawChangeRun(t, ex, store, n, tt.wantAsked, tt.wantRows)
		})
	}
}

// checkLawChangeRun checks one bill's run: the sections sent to the explainer and the sections
// stored, none of them a "nonusc:" law with an explanation. wantRows nil means no call and no
// attempt.
func checkLawChangeRun(t *testing.T, ex *fakeExplainer, store *lawStore, n int, wantAsked, wantRows []string) {
	t.Helper()
	if asked := sentSections(ex); !slices.Equal(asked, wantAsked) {
		t.Errorf("sections sent = %v, want %v", asked, wantAsked)
	}
	if wantRows == nil {
		if len(ex.contexts) != 0 || len(store.attempts) != 0 || n != 0 {
			t.Errorf("calls = %d, attempts = %d, explained = %d; want none", len(ex.contexts), len(store.attempts), n)
		}
		return
	}
	if len(store.attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(store.attempts))
	}
	rows, explained := storedSections(store.attempts[0])
	if !slices.Equal(rows, wantRows) {
		t.Errorf("stored sections = %v, want %v", rows, wantRows)
	}
	for id, ok := range explained {
		if ok && strings.HasPrefix(id, model.NonUSCSectionPrefix) {
			t.Errorf("%s stored with an explanation, want none", id)
		}
	}
}

// sentSections lists the section IDs of every context the explainer was given.
func sentSections(ex *fakeExplainer) []string {
	var ids []string
	for _, c := range ex.contexts {
		for _, sec := range c.Sections {
			ids = append(ids, sec.SectionID)
		}
	}
	return ids
}

// storedSections lists the attempt's bill_law_changes section IDs, and which have an explanation.
func storedSections(a repository.LawChangeAttemptRow) ([]string, map[string]bool) {
	ids := make([]string, 0, len(a.Changes))
	explained := map[string]bool{}
	for _, c := range a.Changes {
		ids = append(ids, c.SectionID)
		explained[c.SectionID] = c.Explanation != ""
	}
	return ids, explained
}

func TestSyncLawChanges_StoreErrors(t *testing.T) {
	store := &lawStore{items: lawItems(2), loadErr: map[string]error{"hr-119-2": errors.New("boom")}}
	s, _ := jobService(nil, nil)
	s.store, s.lawExplainer = store, &fakeExplainer{}

	n, err := s.syncLawChanges(t.Context(), 119, 0)
	if err == nil || n != 1 || !strings.Contains(err.Error(), "1 of 2 bills") {
		t.Errorf("syncLawChanges = %d, %v; want 1 explained and an error for the other", n, err)
	}
}

func TestAILawChangeContext_MergesReferences(t *testing.T) {
	lc := &repository.LawChangeContext{BillID: "hr-119-1", Refs: []repository.LawChangeRef{
		{SectionID: lawSecA, RefKind: model.LawRefAmends, SubsectionPath: "(t)", Instruction: "strike 2025"},
		{SectionID: lawSecB, RefKind: model.LawRefAmends, Instruction: "insert a word"},
		{SectionID: lawSecA, RefKind: model.LawRefRepeals, SubsectionPath: "(u), (c)(2)", Instruction: "repeal (u)"},
		{SectionID: lawSecA, RefKind: model.LawRefAdds, SubsectionPath: "(t)", Instruction: "strike 2025"},
	}}
	got := aiLawChangeContext(lc, lawRP)
	if len(got.Sections) != 2 || got.Sections[0].SectionID != lawSecA || got.Sections[1].SectionID != lawSecB {
		t.Fatalf("sections = %+v, want A then B", got.Sections)
	}
	a := got.Sections[0]
	if a.Kind != model.LawRefRepeals || strings.Join(a.Subsections, ",") != "(t),(u),(c)(2)" ||
		a.Instruction != "strike 2025\n\nrepeal (u)" {
		t.Errorf("merged A = %+v", a)
	}
}

// End to end through the real summarizer on a fake Gemini: the prompt carries the current text,
// and a section the model wasn't asked about is dropped.
func TestSyncLawChanges_WithTheRealSummarizer(t *testing.T) {
	store := &lawStore{items: lawItems(1)}
	summarizer, prompts := fakeGemini(t, answer(`{"changes":[`+
		`{"section_id":"`+lawSecA+`","explanation":"Today the section says 2025; it would say 2026."},`+
		`{"section_id":"/us/usc/t1/s1","explanation":"Not asked."}]}`, "STOP"))
	s, logs := jobService(nil, nil)
	s.store = store
	s.SetSummarizer(summarizer)

	if _, err := s.syncLawChanges(t.Context(), 119, 0); err != nil {
		t.Fatal(err)
	}
	if len(*prompts) != 1 || !strings.Contains((*prompts)[0], "Current text of A.") {
		t.Errorf("prompts = %v, want one with the section's current text", *prompts)
	}
	if len(store.attempts) != 1 || store.attempts[0].Outcome != "ok" || len(store.attempts[0].Changes) != 2 ||
		store.attempts[0].Changes[0].Explanation == "" || store.attempts[0].Changes[1].Explanation != "" ||
		store.attempts[0].Changes[0].ModelUsed != ai.DefaultModel {
		t.Errorf("attempts = %+v", store.attempts)
	}
	if lines := logLines(t, logs, "law_change_result"); len(lines) != 1 || lines[0]["invalid_items"] != float64(1) {
		t.Errorf("law_change_result = %v, want one with 1 invalid item", lines)
	}
}

func TestSyncLawChanges_OffWithoutASummarizer(t *testing.T) {
	s, _ := jobService(nil, &fakeSummarizer{})
	s.SetSummarizer(nil)
	if s.lawExplainer != nil {
		t.Fatal("a nil *ai.Summarizer left a law explainer")
	}
	if err := s.SyncLawChanges(t.Context(), 119, 0); err != nil {
		t.Errorf("SyncLawChanges without a summarizer = %v", err)
	}
}
