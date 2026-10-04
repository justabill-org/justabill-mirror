package spannerdb_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// ruleSummary is a summary of the current text ("hash-ih") written with the rule context hash
// ruleHash ("" for none).
func ruleSummary(bill, ruleHash string) repository.BillSummaryRow {
	s := summary(bill, "hash-ih", summaryPrompt)
	s.SourceRuleHash = ruleHash
	return s
}

// seedRuleRow stores a matched bill_cra_rules row for bill with the given context hash.
func seedRuleRow(t *testing.T, store *spannerdb.PipelineStoreImpl, bill, contextHash string) {
	t.Helper()
	r := craRow(bill, new("hash-ih"))
	r.ContextHash = contextHash
	upsertCRA(t, store, r)
}

func TestQueryBillsToSummarize_RuleChange(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	for n := 1; n <= 6; n++ {
		seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 100, []string{"ih"}, "ih")
	}
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 7, 5, []string{"ih"}, "ih")
	for _, n := range []int{1, 2, 3, 5, 6, 7} {
		seedRuleRow(t, store, billID(n), "ctx-1")
	}
	blockedOtherPrompt := attempt(billID(5), "hash-ih", repository.SummaryOutcomeBlocked, summaryNow().Add(-time.Hour))
	blockedOtherPrompt.PromptVersion = "bill-v3"
	for _, a := range []repository.SummaryAttemptRow{
		blockedOtherPrompt, // blocked for this text: a rule change doesn't retry it
		attempt(billID(6), "hash-old", repository.SummaryOutcomeBlocked, summaryNow().Add(-time.Hour)), // other text
	} {
		if err := store.RecordSummaryAttempt(ctx, a); err != nil {
			t.Fatalf("record attempt: %v", err)
		}
	}
	for _, s := range []repository.BillSummaryRow{
		ruleSummary(billID(1), ""),        // written before the rule was matched: due
		ruleSummary(billID(2), "ctx-1"),   // written with the current rule data: not due
		ruleSummary(billID(3), "ctx-old"), // written with older rule data: due
		ruleSummary(billID(4), ""),        // not a CRA resolution: not due
		ruleSummary(billID(5), ""),        // blocked for its text: not due
		ruleSummary(billID(6), ""),        // blocked for other text: due
		ruleSummary(billID(7), ""),        // recent: due, in the recent tier
	} {
		if err := store.UpsertBillSummary(ctx, s); err != nil {
			t.Fatalf("upsert summary: %v", err)
		}
	}

	withRule := func(q *repository.SummaryQueueQuery) { q.RuleContext = true }
	assertRows(t, queue(t, store, withRule), []string{
		billID(7) + "|ih|hash-ih|1",
		billID(1) + "|ih|hash-ih|2", billID(3) + "|ih|hash-ih|2", billID(6) + "|ih|hash-ih|2",
	})
	counts, err := store.CountBillsToSummarize(ctx, repository.SummaryQueueQuery{
		Congress: testdb.FixtureCongress, PromptVersion: summaryPrompt, Model: summaryModel,
		Now: summaryNow(), RecentDays: 30, RuleContext: true,
	})
	if err != nil {
		t.Fatalf("count bills to summarize: %v", err)
	}
	if want := map[int]int{repository.SummaryTierRecent: 1, repository.SummaryTierOther: 3}; !reflect.DeepEqual(
		counts, want) {
		t.Errorf("backlog = %v, want %v", counts, want)
	}

	// With the rule context off (AI_RULE_CONTEXT=false), a rule change makes nothing due, and the
	// CRS context doesn't stand in for it.
	assertRows(t, queue(t, store, nil), []string{})
	assertRows(t, queue(t, store, func(q *repository.SummaryQueueQuery) { q.CRSContext = true }), []string{})

	// Once a bill is summarized with its current rule data, it isn't due again.
	if err = store.UpsertBillSummary(ctx, ruleSummary(billID(1), "ctx-1")); err != nil {
		t.Fatalf("upsert summary: %v", err)
	}
	assertRows(t, queue(t, store, withRule), []string{
		billID(7) + "|ih|hash-ih|1", billID(3) + "|ih|hash-ih|2", billID(6) + "|ih|hash-ih|2",
	})

	// Changed rule data makes it due once more.
	seedRuleRow(t, store, billID(1), "ctx-2")
	assertRows(t, queue(t, store, withRule), []string{
		billID(7) + "|ih|hash-ih|1",
		billID(1) + "|ih|hash-ih|2", billID(3) + "|ih|hash-ih|2", billID(6) + "|ih|hash-ih|2",
	})
}

// frDoc is a Federal Register document row with the fields LoadBillContext reads.
func frDoc(number, citation, title string, published time.Time, effective *time.Time, abstract *string,
	agencies ...string,
) repository.FRDocumentRow {
	d := repository.FRDocumentRow{
		DocumentNumber: number, Citation: citation, Volume: 89, StartPage: 1, EndPage: 2, DocType: "Rule",
		Title: title, PublicationDate: published, EffectiveOn: effective, Abstract: abstract,
		HTMLURL: "https://www.federalregister.gov/d/" + number, ContentHash: "h-" + number,
	}
	for _, a := range agencies {
		d.Agencies = append(d.Agencies, repository.FRAgency{Name: a, Slug: "slug"})
	}
	return d
}

func TestLoadBillContext_DisapprovedRule(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	ids := make(map[int]string)
	for n := 1; n <= 3; n++ {
		ids[n] = seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 10, []string{"ih"}, "ih")["ih"]
	}
	effective := time.Date(2025, time.October, 1, 0, 0, 0, 0, time.UTC)
	doc := frDoc("2024-29699", "89 FR 106768", "Overdraft Lending", time.Date(2024, time.December, 30, 0, 0, 0, 0,
		time.UTC), &effective, new("The CFPB amends Regulation Z."),
		"Consumer Financial Protection Bureau", "Treasury Department")
	doc.Action = new("Final rule.")
	withdrawn := frDoc("2023-00100", "88 FR 100", "Overdraft Guidance",
		time.Date(2023, time.January, 3, 0, 0, 0, 0, time.UTC), nil, nil, "Consumer Financial Protection Bureau")
	for _, d := range []repository.FRDocumentRow{doc, withdrawn} {
		if _, err := store.UpsertFRDocument(ctx, d); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
	}
	matched := craRow(billID(1), new("hash-ih"))
	matched.WithdrawnDocumentNumber = new(withdrawn.DocumentNumber)
	upsertCRA(t, store, matched)
	unmatched := craRow(billID(2), nil)
	unmatched.Status, unmatched.Method, unmatched.DocumentNumber = repository.CRAStatusUnmatched, nil, nil
	unmatched.Reason, unmatched.ContextHash = new(repository.CRAReasonNoCandidates), "ctx-unmatched"
	upsertCRA(t, store, unmatched)

	tests := map[string]struct {
		bill int
		want *repository.SummaryRuleContext
	}{
		"matched withdrawal": {1, &repository.SummaryRuleContext{
			Title: matched.RuleTitle, Agency: matched.RuleAgency, ContextHash: "ctx-1",
			Document: &repository.SummaryRuleDocument{
				Title:       "Overdraft Lending",
				Agencies:    []string{"Consumer Financial Protection Bureau", "Treasury Department"},
				DocType:     "Rule",
				Action:      "Final rule.",
				Citation:    "89 FR 106768",
				Published:   doc.PublicationDate,
				EffectiveOn: &effective,
				Abstract:    "The CFPB amends Regulation Z.",
			},
			Withdrawn: &repository.SummaryRuleDocument{
				Title: "Overdraft Guidance", Agencies: []string{"Consumer Financial Protection Bureau"},
				DocType: "Rule", Citation: "88 FR 100", Published: withdrawn.PublicationDate,
			},
		}},
		"unmatched": {2, &repository.SummaryRuleContext{
			Title: unmatched.RuleTitle, Agency: unmatched.RuleAgency, ContextHash: "ctx-unmatched",
		}},
		"not a CRA resolution": {3, nil},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := store.LoadBillContext(t.Context(), billID(tt.bill), ids[tt.bill])
			if err != nil {
				t.Fatalf("load bill context: %v", err)
			}
			if !reflect.DeepEqual(got.Rule, tt.want) {
				t.Errorf("rule = %s, want %s", describeRule(got.Rule), describeRule(tt.want))
			}
		})
	}
}

// describeRule prints a rule context with its documents, which %+v shows as pointers.
func describeRule(r *repository.SummaryRuleContext) string {
	if r == nil {
		return "nil"
	}
	return fmt.Sprintf("%+v", *r) + " document " + fmtDoc(r.Document) + " withdrawn " + fmtDoc(r.Withdrawn)
}

func fmtDoc(d *repository.SummaryRuleDocument) string {
	if d == nil {
		return "nil"
	}
	s := fmt.Sprintf("%+v", *d)
	if d.EffectiveOn != nil {
		s += " effective " + d.EffectiveOn.Format(time.DateOnly)
	}
	return s
}
