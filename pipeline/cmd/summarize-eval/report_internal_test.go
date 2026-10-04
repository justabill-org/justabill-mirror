package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestPriceCostCountsThinkingAsOutput(t *testing.T) {
	p := price{"list", 1.50, 9.00}
	rec := record{InputTokens: 2_000_000, OutputTokens: 100_000, ThinkingTokens: 100_000}
	// 2M × $1.50 + 0.2M × $9.00
	if got, want := p.cost(rec), 4.80; math.Abs(got-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

func TestEstimateCostWeightsTheLongTail(t *testing.T) {
	p := price{"list", 1.00, 10.00}
	recs := []record{
		{Category: categoryRandom, InputTokens: 1000, OutputTokens: 100},    // $0.002
		{Category: categoryFloorVote, InputTokens: 3000, OutputTokens: 100}, // $0.004
		{Category: categoryLong, InputTokens: 200_000, OutputTokens: 1000},  // $0.21
		{Category: categoryLong, InputTokens: 400_000, OutputTokens: 1000},  // $0.41
		{Category: categoryResolution},                                      // $0: blocked before usage
		{Category: categoryRandom, InputTokens: 2000},                       // $0.002
	}
	e := estimateCost(p, recs)
	near := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	near("run", e.run, 0.628)
	near("typical", e.typical, 0.002)
	near("long", e.long, 0.31)
	near("backlog", e.backlog, 14_950*0.002+50*0.31)
	near("per month", e.perMonth, 2_500*0.002)
}

func TestPercentileNearestRank(t *testing.T) {
	xs := []int64{50, 10, 40, 20, 30, 60, 70, 80, 90, 100}
	for _, tt := range []struct {
		p    int
		want int64
	}{{50, 50}, {90, 90}, {100, 100}, {1, 10}} {
		if got := percentile(xs, tt.p); got != tt.want {
			t.Errorf("p%d = %d, want %d", tt.p, got, tt.want)
		}
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("p50 of nothing = %d, want 0", got)
	}
	if xs[0] != 50 {
		t.Error("percentile sorted its input")
	}
}

func TestListPrices(t *testing.T) {
	if p := listPrices("gemini-3.8-flash"); len(p) != 2 || p[1].input != 2*p[0].input {
		t.Errorf("3.8 Flash prices = %v, want introductory and twice that", p)
	}
	if p := listPrices("gemini-3.5-flash"); len(p) != 1 || p[0].input != 1.50 || p[0].output != 9.00 {
		t.Errorf("3.5 Flash prices = %v", p)
	}
	if p := listPrices("unknown"); p != nil {
		t.Errorf("unknown model prices = %v, want none", p)
	}
}

func reportFixture() ([]evalBill, map[string]record) {
	var bills []evalBill
	latest := make(map[string]record)
	for i := range 20 {
		b := evalBill{BillID: fmt.Sprintf("hr-119-%d", i+1), Category: categoryFloorVote}
		bills = append(bills, b)
		latest[b.BillID] = record{
			BillID: b.BillID, Category: b.Category, Model: "gemini-3.8-flash", Outcome: "ok",
			ModelVersion: "gemini-3.8-flash-001", PromptVersion: "bill-v2", ThinkingLevel: "LOW",
			ShortSummary: fmt.Sprintf("Summary %d | with a pipe\nand a line", i+1),
			InputTokens:  int64(1000 * (i + 1)), OutputTokens: 300, LatencyMS: int64(100 * (i + 1)),
		}
	}
	long := evalBill{BillID: "hr-119-9999", Category: categoryLong}
	bills = append(bills, long, evalBill{BillID: "s-119-5", Category: categoryRandom})
	latest[long.BillID] = record{
		BillID: long.BillID, Category: categoryLong, Model: "gemini-3.8-flash", Outcome: "ok",
		InputTokens: 350_000, OutputTokens: 600, InputTruncated: true,
	}
	blocked := latest["hr-119-2"]
	blocked.Outcome, blocked.Reason, blocked.ShortSummary = "blocked", "SAFETY", ""
	blocked.LoadedTerms, blocked.PartyNames = []string{"landmark"}, []string{"GOP"}
	latest["hr-119-2"] = blocked
	return bills, latest
}

func TestWriteModelReport(t *testing.T) {
	bills, latest := reportFixture()
	r := newModelReport("gemini-3.8-flash", bills, latest)
	var b strings.Builder
	if err := writeModelReport(&b, r, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		"# Summary eval: gemini-3.8-flash",
		"Generated 2026-09-27",
		"21 of 22 bills have a result",
		"model versions: gemini-3.8-flash-001\n",
		"| ok | 20 |",
		"| blocked | 1 |",
		"- hr-119-2 (floor_vote): blocked, SAFETY",
		"- hr-119-9999 (long): ok, input truncated",
		"| Input tokens | 11000 | 19000 | 350000 |",
		"introductory, to 2026-12-31 ($0.75, $3.75)",
		"| hr-119-2 | landmark | GOP |",
		`| hr-119-1 | Summary 1 \| with a pipe and a line |  | | |`,
		"| hr-119-2 | (blocked: SAFETY) |  | | |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	// The rubric stops at 15 bills; hr-119-16 is the 16th floor-vote bill.
	if strings.Contains(got, "| hr-119-16 |") {
		t.Error("rubric has more than 15 bills")
	}
}

func TestWriteComparison(t *testing.T) {
	bills, latest := reportFixture()
	reports := []modelReport{
		newModelReport("gemini-3.8-flash", bills, latest),
		newModelReport("gemini-3.5-flash", bills, nil),
		newModelReport("some-model", bills, map[string]record{"s-119-5": {BillID: "s-119-5", Outcome: "error"}}),
	}
	var b strings.Builder
	if err := writeComparison(&b, reports); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("comparison has %d lines, want a header, a rule and 3 rows:\n%s", len(lines), b.String())
	}
	if !strings.HasPrefix(lines[2], "| gemini-3.8-flash | 21/22 | 20 | 1 | 0 | 0 | 0 | 1 |") {
		t.Errorf("3.8 Flash row = %q", lines[2])
	}
	if !strings.HasPrefix(lines[3], "| gemini-3.5-flash | 0/22 |") {
		t.Errorf("3.5 Flash row = %q", lines[3])
	}
	if !strings.HasSuffix(lines[4], "| n/a | n/a |") || !strings.Contains(lines[4], "| 1/22 | 0 | 0 | 0 | 0 | 1 |") {
		t.Errorf("unpriced row = %q", lines[4])
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestWriteModelReportReturnsWriteErrors(t *testing.T) {
	bills, latest := reportFixture()
	if err := writeModelReport(failingWriter{}, newModelReport("m", bills, latest), time.Now()); err == nil {
		t.Error("want the write error")
	}
}
