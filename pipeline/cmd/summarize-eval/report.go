package main

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Volumes behind the cost estimates (design 68, "Cost"): a backlog of about 15,000 bills, of
// which about 50 are long; about 2,500 re-summaries a month.
const (
	backlogBills     = 15_000
	backlogLongBills = 50
	monthlyBills     = 2_500
	perMillion       = 1e6
	percent          = 100
	p50              = 50
	p90              = 90
	rubricBills      = 15
	// notApplicable fills a cell with nothing to report.
	notApplicable = "n/a"
)

// price is a list price in dollars per million tokens. Output covers thinking tokens too.
type price struct {
	label  string
	input  float64
	output float64
}

// listPrices are the global-endpoint Standard prices, checked 2026-09-26 (design 68).
func listPrices(model string) []price {
	switch model {
	case "gemini-3.5-flash":
		return []price{{"list", 1.50, 9.00}}
	case "gemini-3.5-flash-lite":
		return []price{{"list", 0.30, 2.50}}
	case "gemini-3.8-flash":
		return []price{{"introductory, to 2026-12-31", 0.75, 3.75}, {"2027 (twice the introductory price)", 1.50, 7.50}}
	case "gemini-3.1-pro-preview":
		return []price{{"list, prompts up to 200K", 2.00, 12.00}}
	default:
		return nil
	}
}

// cost is what rec's call cost at p, in dollars.
func (p price) cost(rec record) float64 {
	return (float64(rec.InputTokens)*p.input + float64(rec.OutputTokens+rec.ThinkingTokens)*p.output) / perMillion
}

// estimate is the cost projection at one price.
type estimate struct {
	price    price
	run      float64 // this eval's calls
	typical  float64 // mean per bill, bills that aren't long
	long     float64 // mean per long bill
	backlog  float64
	perMonth float64
}

// estimateCost projects the sample onto the backlog: the long bills stand for the backlog's long
// tail, the rest for a typical bill. The sample over-represents long bills, so a plain mean would
// overstate the cost.
func estimateCost(p price, recs []record) estimate {
	e := estimate{price: p}
	var typical, long []float64
	for _, rec := range recs {
		c := p.cost(rec)
		e.run += c
		if rec.Category == categoryLong {
			long = append(long, c)
		} else {
			typical = append(typical, c)
		}
	}
	e.typical, e.long = mean(typical), mean(long)
	e.backlog = float64(backlogBills-backlogLongBills)*e.typical + backlogLongBills*e.long
	e.perMonth = monthlyBills * e.typical
	return e
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// percentile is the nearest-rank percentile of xs (0 for none).
func percentile(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	rank := (p*len(s) + percent - 1) / percent
	return s[max(rank, 1)-1]
}

// modelReport is everything the report says about one run: a model, with or without the CRS
// summaries or the disapproved rules (label).
type modelReport struct {
	label   string
	model   string
	bills   []evalBill
	records []record // the latest record per bill, in list order
}

func newModelReport(label string, bills []evalBill, latest map[string]record) modelReport {
	r := modelReport{label: label, model: labelModel(label), bills: bills}
	for _, b := range bills {
		if rec, ok := latest[b.BillID]; ok {
			r.records = append(r.records, rec)
		}
	}
	return r
}

func (r modelReport) outcomes() map[string]int {
	counts := make(map[string]int)
	for _, rec := range r.records {
		counts[rec.Outcome]++
	}
	return counts
}

func (r modelReport) column(f func(record) int64) []int64 {
	out := make([]int64, 0, len(r.records))
	for _, rec := range r.records {
		if rec.InputTokens > 0 {
			out = append(out, f(rec))
		}
	}
	return out
}

func (r modelReport) flagged() []record {
	var out []record
	for _, rec := range r.records {
		if len(rec.LoadedTerms) > 0 || len(rec.PartyNames) > 0 {
			out = append(out, rec)
		}
	}
	return out
}

// writeModelReport writes the Markdown report for one model.
func writeModelReport(w io.Writer, r modelReport, generated time.Time) error {
	p := &printer{w: w}
	p.f("# Summary eval: %s\n\n", r.label)
	p.f("Generated %s by `pipeline/cmd/summarize-eval` from `results.jsonl`. %d of %d bills have a result.\n\n",
		generated.UTC().Format(time.DateOnly), len(r.records), len(r.bills))
	p.f("Prompt %s · thinking %s · model versions: %s\n\n",
		distinct(r.records, func(rec record) string { return rec.PromptVersion }),
		distinct(r.records, func(rec record) string { return rec.ThinkingLevel }),
		distinct(r.records, func(rec record) string { return rec.ModelVersion }))
	r.writeOutcomes(p)
	r.writeUsage(p)
	r.writeCost(p)
	r.writeChecks(p)
	r.writeCopying(p)
	r.writeRuleSignals(p)
	r.writeRubric(p)
	r.writeRuleRubric(p)
	return p.err
}

func (r modelReport) writeOutcomes(p *printer) {
	p.f("## Outcomes\n\n| Outcome | Bills |\n|---|---|\n")
	counts := r.outcomes()
	for _, o := range sortedKeys(counts) {
		p.f("| %s | %d |\n", o, counts[o])
	}
	var notOK []record
	for _, rec := range r.records {
		if rec.Outcome != "ok" || rec.InputTruncated {
			notOK = append(notOK, rec)
		}
	}
	if len(notOK) > 0 {
		p.f("\nNot ok, or input cut to fit `AI_MAX_INPUT_TOKENS`:\n\n")
		for _, rec := range notOK {
			p.f("- %s (%s): %s%s\n", rec.BillID, rec.Category, rec.Outcome,
				cond(rec.Reason != "", ", "+rec.Reason, "")+cond(rec.InputTruncated, ", input truncated", ""))
		}
	}
	p.f("\n")
}

func (r modelReport) writeUsage(p *printer) {
	p.f("## Tokens and latency\n\n| | p50 | p90 | max |\n|---|---|---|---|\n")
	rows := []struct {
		label string
		get   func(record) int64
	}{
		{"Input tokens", func(rec record) int64 { return rec.InputTokens }},
		{"Output tokens", func(rec record) int64 { return rec.OutputTokens }},
		{"Thinking tokens", func(rec record) int64 { return rec.ThinkingTokens }},
		{"Latency (ms)", func(rec record) int64 { return rec.LatencyMS }},
	}
	for _, row := range rows {
		xs := r.column(row.get)
		p.f("| %s | %d | %d | %d |\n", row.label, percentile(xs, p50), percentile(xs, p90), percentile(xs, percent))
	}
	p.f("\n")
}

func (r modelReport) writeCost(p *printer) {
	prices := listPrices(r.model)
	if len(prices) == 0 {
		p.f("## Cost\n\nNo list price known for %s.\n\n", r.model)
		return
	}
	p.f("## Cost at list price (global, Standard)\n\n")
	p.f("A typical bill is the mean of the %d bills that aren't long; the backlog is %d typical bills "+
		"plus %d long ones; a month is %d typical re-summaries.\n\n",
		len(r.records)-countCategory(r.records, categoryLong), backlogBills-backlogLongBills,
		backlogLongBills, monthlyBills)
	p.f("| Price ($/M in, out) | This run | Typical bill | Long bill | 15k-bill backlog | Per month |\n")
	p.f("|---|---|---|---|---|---|\n")
	for _, pr := range prices {
		e := estimateCost(pr, r.records)
		p.f("| %s ($%.2f, $%.2f) | $%.2f | $%.4f | $%.3f | $%.0f | $%.0f |\n",
			pr.label, pr.input, pr.output, e.run, e.typical, e.long, e.backlog, e.perMonth)
	}
	p.f("\n")
}

func (r modelReport) writeChecks(p *printer) {
	p.f("## Neutrality checks\n\n")
	p.f("Loaded terms and party names in a summary that the bill's text and title don't use%s.\n\n",
		cond(strings.HasSuffix(r.label, ruleSuffix), ", nor the rule's Federal Register text in the prompt", ""))
	flagged := r.flagged()
	if len(flagged) == 0 {
		p.f("None in %d summaries.\n\n", len(r.records))
		return
	}
	p.f("| Bill | Loaded terms | Party names |\n|---|---|---|\n")
	for _, rec := range flagged {
		p.f("| %s | %s | %s |\n", rec.BillID, strings.Join(rec.LoadedTerms, ", "), strings.Join(rec.PartyNames, ", "))
	}
	p.f("\n")
}

// copied is the CRS copy share of each summary that has one.
func (r modelReport) copied() []float64 {
	var out []float64
	for _, rec := range r.records {
		if rec.CRSCopied != nil {
			out = append(out, *rec.CRSCopied)
		}
	}
	return out
}

// copiedCell is the mean and max CRS copy share, as percentages, or n/a.
func (r modelReport) copiedCell() string {
	xs := r.copied()
	if len(xs) == 0 {
		return notApplicable
	}
	return fmt.Sprintf("%.1f%% / %.1f%%", mean(xs)*percent, slices.Max(xs)*percent)
}

// writeCopying reports how much of each summary repeats the bill's CRS summary word for word
// (docs/design/197-crs-summaries.md: under 10% of 8-word sequences). Without the CRS summary in
// the prompt it's the base rate: both summaries describe the same text.
func (r modelReport) writeCopying(p *printer) {
	xs := r.copied()
	if len(xs) == 0 {
		return
	}
	p.f("## Copying from the CRS summary\n\n")
	p.f("Share of each summary's %d-word sequences that also appear in the bill's CRS summary (case and "+
		"punctuation ignored). Target: under %.0f%%.\n\n", copyGram, copyTarget*percent)
	over := 0
	for _, x := range xs {
		over += cond(x >= copyTarget, 1, 0)
	}
	p.f("Mean / max: %s over %d summaries; %d at or over the target.\n\n", r.copiedCell(), len(xs), over)
	p.f("| Bill | Copied |\n|---|---|\n")
	for _, rec := range r.records {
		if rec.CRSCopied != nil {
			p.f("| %s | %.1f%% |\n", rec.BillID, *rec.CRSCopied*percent)
		}
	}
	p.f("\n")
}

// ruleRecords are the records of CRA resolutions that have rule signals: those summarized ok.
func (r modelReport) ruleRecords() []record {
	var out []record
	for _, rec := range r.records {
		if rec.RuleSignals != nil {
			out = append(out, rec)
		}
	}
	return out
}

// ruleCell is how many CRA summaries say the rule loses its force or effect, and the median rule
// words of those with a matched document, or n/a.
func (r modelReport) ruleCell() string {
	recs := r.ruleRecords()
	if len(recs) == 0 {
		return notApplicable
	}
	noForce := 0
	var ruleWords []int64
	for _, rec := range recs {
		noForce += cond(rec.RuleSignals.NoForce, 1, 0)
		if rec.RuleSignals.RuleWords != nil {
			ruleWords = append(ruleWords, int64(*rec.RuleSignals.RuleWords))
		}
	}
	return fmt.Sprintf("%d/%d · %d", noForce, len(recs), percentile(ruleWords, p50))
}

// writeRuleSignals reports the automatic checks of the CRA resolutions' summaries
// (docs/design/590-cra-disapproved-rules.md). They point the reviewer at summaries to read; the
// judgement is the rubric's.
func (r modelReport) writeRuleSignals(p *printer) {
	recs := r.ruleRecords()
	if len(recs) == 0 {
		return
	}
	rules := make(map[string]*evalRule, len(r.bills))
	for _, b := range r.bills {
		rules[b.BillID] = b.Rule
	}
	p.f("## The disapproved rule (CRA resolutions)\n\n")
	p.f("Automatic signals, not verdicts. *No force or effect*: the summary says the rule would have no "+
		"force or effect, would not apply, or is nullified. *Attributed*: it attributes something to the "+
		"agency (\"the agency said\", \"according to the\"). *Rule words*: distinct words of %d letters or more "+
		"that the summary shares with the Federal Register document (title, action, abstract) and not with "+
		"the resolution's title and text; n/a when the rule wasn't matched.\n\n", ruleWordMin)
	p.f("No force or effect / median rule words: %s.\n\n", r.ruleCell())
	p.f("| Bill | Match | No force or effect | Attributed | Rule words |\n|---|---|---|---|---|\n")
	for _, rec := range recs {
		match := ""
		if rule := rules[rec.BillID]; rule != nil {
			match = rule.Match + cond(rule.Withdrawn != nil, " (withdrawal)", "")
		}
		words := notApplicable
		if rec.RuleSignals.RuleWords != nil {
			words = strconv.Itoa(*rec.RuleSignals.RuleWords)
		}
		p.f("| %s | %s | %s | %s | %s |\n", rec.BillID, match, cond(rec.RuleSignals.NoForce, "yes", "no"),
			cond(rec.RuleSignals.Attributed, "yes", "no"), words)
	}
	p.f("\n")
}

// writeRuleRubric lists the CRA resolutions for a reviewer to mark on the design's four points.
func (r modelReport) writeRuleRubric(p *printer) {
	if countCategory(r.records, categoryCRA) == 0 {
		return
	}
	p.f("## Rubric (CRA resolutions)\n\n")
	p.f("For the reviewer: (a) says what the rule does; (b) says disapproval gives it no force or effect, " +
		"so its requirements wouldn't apply; (c) attributes the agency's stated purposes rather than " +
		"adopting them; (d) adds no effects the abstract doesn't state. For an unmatched rule, (a) and (d) " +
		"ask that the summary invent no rule details.\n\n")
	p.f("| Bill | Short summary | (a) | (b) | (c) | (d) |\n|---|---|---|---|---|---|\n")
	for _, rec := range r.records {
		if rec.Category != categoryCRA {
			continue
		}
		short := rec.ShortSummary
		if rec.Outcome != "ok" {
			short = fmt.Sprintf("(%s: %s)", rec.Outcome, rec.Reason)
		}
		p.f("| %s | %s | | | | |\n", rec.BillID, cell(short))
	}
	p.f("\n")
}

// writeRubric lists the floor-vote bills for a reviewer to mark accurate and neutral: the bills
// people are most likely to read before they vote.
func (r modelReport) writeRubric(p *printer) {
	if countCategory(r.records, categoryFloorVote) == 0 {
		return
	}
	p.f("## Rubric (%d bills with floor votes)\n\n", rubricBills)
	p.f("For the reviewer: mark each summary Accurate (it says only what the text says) and Neutral " +
		"(no judgment, no party framing), with a note for any \"no\".\n\n")
	p.f("| Bill | Short summary | Who it affects | Accurate? | Neutral? |\n|---|---|---|---|---|\n")
	n := 0
	for _, rec := range r.records {
		if rec.Category != categoryFloorVote || n == rubricBills {
			continue
		}
		n++
		short := rec.ShortSummary
		if rec.Outcome != "ok" {
			short = fmt.Sprintf("(%s: %s)", rec.Outcome, rec.Reason)
		}
		p.f("| %s | %s | %s | | |\n", rec.BillID, cell(short), cell(rec.WhoItAffects))
	}
	p.f("\n")
}

// writeComparison writes the side-by-side table across models.
func writeComparison(w io.Writer, reports []modelReport) error {
	p := &printer{w: w}
	p.f("| Model | Results | ok | Blocked | Truncated output | Invalid | Error | Input truncated | " +
		"p50 / p90 latency (s) | p50 output + thinking | Flagged summaries | CRA: no force or effect · rule words | " +
		"CRS copied 8-grams (mean / max) | Backlog | Per month |\n")
	p.f("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range reports {
		c := r.outcomes()
		lat := r.column(func(rec record) int64 { return rec.LatencyMS })
		out := r.column(func(rec record) int64 { return rec.OutputTokens + rec.ThinkingTokens })
		truncated := 0
		for _, rec := range r.records {
			truncated += cond(rec.InputTruncated, 1, 0)
		}
		costs := "n/a | n/a"
		if prices := listPrices(r.model); len(prices) > 0 {
			e := estimateCost(prices[0], r.records)
			costs = fmt.Sprintf("$%.0f | $%.0f", e.backlog, e.perMonth)
		}
		const msPerSecond = 1000.0
		p.f("| %s | %d/%d | %d | %d | %d | %d | %d | %d | %.1f / %.1f | %d | %d | %s | %s | %s |\n",
			r.label, len(r.records), len(r.bills), c["ok"], c["blocked"], c["truncated_output"], c["invalid"],
			c["error"], truncated, float64(percentile(lat, p50))/msPerSecond,
			float64(percentile(lat, p90))/msPerSecond, percentile(out, p50), len(r.flagged()), r.ruleCell(),
			r.copiedCell(), costs)
	}
	return p.err
}

// printer writes formatted text; the first write error sticks and later writes are skipped.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) f(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(strings.TrimSpace(s))
}

func cond[T any](ok bool, yes, no T) T {
	if ok {
		return yes
	}
	return no
}

func countCategory(recs []record, category string) int {
	n := 0
	for _, rec := range recs {
		n += cond(rec.Category == category, 1, 0)
	}
	return n
}

func distinct(recs []record, f func(record) string) string {
	seen := make(map[string]int)
	for _, rec := range recs {
		if v := f(rec); v != "" {
			seen[v]++
		}
	}
	if len(seen) == 0 {
		return "unknown"
	}
	return strings.Join(sortedKeys(seen), ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, cmp.Compare[string])
	return keys
}
