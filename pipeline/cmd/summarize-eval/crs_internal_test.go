package main

import (
	"context"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

func TestCopiedShare(t *testing.T) {
	source := "This bill requires the Federal Communications Commission to issue an order permitting alerts."
	tests := map[string]struct {
		summary string
		want    float64
	}{
		// 9 words, 2 sequences of 8: both copied, whatever the case and punctuation.
		"copied":       {"THIS BILL requires the Federal Communications Commission, to issue", 1},
		"own words":    {"The Federal Communications Commission would have to allow shark attack alerts.", 0},
		"a quarter":    {"requires the Federal Communications Commission to issue an alert about sharks", 0.25},
		"too short":    {"This bill requires alerts.", 0},
		"curly quotes": {"the Commission’s order permits alerts that the Commission’s order permits", 0},
	}
	for name, tt := range tests {
		if got := copiedShare(tt.summary, source, copyGram); math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("%s: copiedShare = %v, want %v", name, got, tt.want)
		}
	}
}

func TestLatestCRS(t *testing.T) {
	got := latestCRS([]congress.CRSSummary{
		{
			ActionDate: "2025-03-12",
			ActionDesc: "Introduced in Senate",
			Text:       "<p>Old.</p>",
			UpdateDate: "2025-04-28T13:39:37Z",
		},
		{ActionDate: "2026-06-26", ActionDesc: "Public Law", Text: "<p><strong>Lulu’s Law</strong></p><p>New.</p>",
			UpdateDate: "2026-06-30T12:39:56Z"},
		{ActionDate: "2026-06-26", ActionDesc: "Passed Senate", Text: "<p>Earlier update.</p>",
			UpdateDate: "2026-06-27T00:00:00Z"},
	})
	want := &ai.CRSContext{
		VersionDesc: "Public Law", ActionDate: time.Date(2026, time.June, 26, 0, 0, 0, 0, time.UTC),
		Text: "Lulu’s Law\n\nNew.",
	}
	if got == nil || *got != *want {
		t.Errorf("latestCRS = %+v, want %+v", got, want)
	}
	if latestCRS(nil) != nil || latestCRS([]congress.CRSSummary{{Text: "<p></p>"}}) != nil {
		t.Error("latestCRS of none or an empty text isn't nil")
	}
}

type fakeCRSFetcher struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeCRSFetcher) BillSummaries(
	_ context.Context, _ int, billType string, number int,
) ([]congress.CRSSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, billType)
	if number == 3 {
		return nil, nil
	}
	return []congress.CRSSummary{
		{ActionDate: "2025-01-02", ActionDesc: "Introduced in House", Text: "<p>CRS.</p>"},
	}, nil
}

func TestCRSCacheLoad(t *testing.T) {
	bills := []evalBill{
		{BillID: "hr-119-1", Congress: 119, Type: "hr", Number: 1},
		{BillID: "s-119-3", Congress: 119, Type: "s", Number: 3},
	}
	fetch := &fakeCRSFetcher{}
	cache := &crsCache{dir: t.TempDir(), fetch: fetch}
	for range 2 {
		got, err := cache.load(t.Context(), bills)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got["hr-119-1"].Text != "CRS." {
			t.Errorf("load = %+v, want hr-119-1's summary only", got)
		}
	}
	if len(fetch.calls) != 2 {
		t.Errorf("fetched %d times, want once per bill: the rerun reads the cache", len(fetch.calls))
	}
}

// crsSummarizer copies the CRS summary it's given into the summary, so copying is 100%, and
// records whether each call carried one.
type crsSummarizer struct {
	fakeSummarizer

	withCRS []string
}

func (f *crsSummarizer) SummarizeBill(ctx context.Context, bc ai.BillContext) (*ai.BillSummary, error) {
	out, err := f.fakeSummarizer.SummarizeBill(ctx, bc)
	if bc.CRSSummary != nil {
		f.mu.Lock()
		f.withCRS = append(f.withCRS, bc.BillID)
		f.mu.Unlock()
		out.ShortSummary, out.LongSummary, out.WhoItAffects = "", bc.CRSSummary.Text, ""
	}
	return out, err
}

func TestRunWithCRS(t *testing.T) {
	r, _ := newTestRunner(t, 1)
	crsText := "This bill requires the Secretary of the Interior to report on every national park each year."
	r.crs = map[string]*ai.CRSContext{"s-119-3": {VersionDesc: "Introduced in Senate", Text: crsText}}
	s := &crsSummarizer{}
	s.model = "m"
	for _, withCRS := range []bool{false, true} {
		r.variant = variant{crs: withCRS}
		if err := r.run(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(s.withCRS, []string{"s-119-3"}) {
		t.Errorf("calls with a CRS summary = %v, want s-119-3 in the +crs run only", s.withCRS)
	}
	base, crs := r.results.latest("m"), r.results.latest("m"+crsSuffix)
	if len(base) != 3 || len(crs) != 3 {
		t.Fatalf("runs have %d and %d results, want 3 each", len(base), len(crs))
	}
	if c := crs["s-119-3"].CRSCopied; c == nil || *c != 1 || !crs["s-119-3"].CRS {
		t.Errorf("+crs record = %+v, want CRS set and all of it copied", crs["s-119-3"])
	}
	if c := base["s-119-3"].CRSCopied; c == nil || *c != 0 {
		t.Errorf("base record copied = %v, want the base rate 0", c)
	}
	if base["hr-119-1"].CRSCopied != nil {
		t.Error("a bill without a CRS summary has a copy share")
	}

	var b strings.Builder
	if err := writeModelReport(&b, newModelReport("m"+crsSuffix, r.bills, crs), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Summary eval: m+crs", "## Copying from the CRS summary",
		"Mean / max: 100.0% / 100.0% over 1 summaries; 1 at or over the target.", "| s-119-3 | 100.0% |"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, b.String())
		}
	}
	b.Reset()
	if err := writeComparison(&b, []modelReport{newModelReport("m", r.bills, base)}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "| 0.0% / 0.0% | n/a | n/a |") {
		t.Errorf("comparison lacks the copy column:\n%s", b.String())
	}
}

func TestOptionsLabels(t *testing.T) {
	o := options{models: []string{"a", "b"}}
	if got := o.labels(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("labels = %v", got)
	}
	o.crs = true
	if got := o.labels(); !slices.Equal(got, []string{"a", "a+crs", "b", "b+crs"}) {
		t.Errorf("labels with --crs = %v", got)
	}
}
