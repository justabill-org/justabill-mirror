package cra_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/cra"
)

// disapproval is one entry of testdata/disapprovals-119.json: every 119th-Congress joint
// resolution whose title says "disapprov" (listed from Congress.gov on 2026-10-02), with, for the
// CRA resolutions, the resolving clause of its first text from GovInfo (attributes dropped).
type disapproval struct {
	Type   string `json:"type"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Text   string `json:"text,omitempty"`
}

func (d disapproval) key() string { return fmt.Sprintf("%s%d", d.Type, d.Number) }

// parsed is a [cra.Resolution] as the golden file records it.
type parsed struct {
	Agency         string     `json:"agency,omitempty"`
	RuleTitle      string     `json:"rule_title,omitempty"`
	Withdrawal     bool       `json:"withdrawal,omitempty"`
	WithdrawnTitle string     `json:"withdrawn_title,omitempty"`
	Citations      []citation `json:"citations,omitempty"`
	GAOOpinion     bool       `json:"gao_opinion,omitempty"`
	IssuedOn       string     `json:"issued_on,omitempty"`
	FromText       bool       `json:"from_text,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type citation struct {
	Text   string `json:"text"`
	Volume int    `json:"volume"`
	Page   int    `json:"page"`
	Date   string `json:"date,omitempty"`
}

func day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.DateOnly)
}

func toParsed(r cra.Resolution, err error) parsed {
	p := parsed{
		Agency: r.Agency, RuleTitle: r.RuleTitle, Withdrawal: r.Withdrawal, WithdrawnTitle: r.WithdrawnTitle,
		GAOOpinion: r.GAOOpinion, IssuedOn: day(r.IssuedOn), FromText: r.FromText,
	}
	for _, c := range r.Citations {
		p.Citations = append(p.Citations, citation{Text: c.Text, Volume: c.Volume, Page: c.Page, Date: day(c.Date)})
	}
	if err != nil {
		p.Error = err.Error()
	}
	return p
}

func loadDisapprovals(t *testing.T) []disapproval {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "disapprovals-119.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ds []disapproval
	if err = json.Unmarshal(raw, &ds); err != nil {
		t.Fatal(err)
	}
	return ds
}

// TestIsCRA119 checks detection on every "disapproval" title of the 119th: the 234 CRA
// resolutions (each has a text in the fixture) and not the 48 others (arms sales, D.C. Council
// acts, a nuclear agreement).
func TestIsCRA119(t *testing.T) {
	ds := loadDisapprovals(t)
	if len(ds) != 282 {
		t.Fatalf("fixture has %d titles, want 282", len(ds))
	}
	detected := 0
	for _, d := range ds {
		got := cra.IsCRA(d.Type, d.Title)
		if got != (d.Text != "") {
			t.Errorf("IsCRA(%s %q) = %v", d.key(), d.Title, got)
		}
		if got {
			detected++
		}
	}
	if detected != 234 {
		t.Errorf("detected %d, want 234", detected)
	}
}

func TestIsCRA(t *testing.T) {
	const title = "Providing for congressional disapproval under chapter 8 of title 5, United States Code, " +
		"of the rule submitted by the Department of Energy relating to \"Energy Conservation Program\"."
	tests := []struct {
		billType, title string
		want            bool
	}{
		{"sjres", title, true},
		{"HJRES", title, true},
		{"hr", title, false},
		{"sres", title, false},
		{"sjres", "Disapproving the rule issued by the Department of Labor relating to fiduciaries.", true},
		{"sjres", "A joint resolution providing for congressional disapproval under chapter 8 of  title 5", true},
		{"sjres", "Relating to chapter 8 of title 50, United States Code.", false},
		{"hjres", "Disapproving the action of the District of Columbia Council in approving the Act.", false},
	}
	for _, tt := range tests {
		if got := cra.IsCRA(tt.billType, tt.title); got != tt.want {
			t.Errorf("IsCRA(%q, %q) = %v, want %v", tt.billType, tt.title, got, tt.want)
		}
	}
}

// TestParse119 parses every CRA resolution of the 119th and compares the result with
// testdata/parsed-119.golden.json. UPDATE_GOLDEN=1 rewrites the golden file; review its diff.
func TestParse119(t *testing.T) {
	got := map[string]parsed{}
	for _, d := range loadDisapprovals(t) {
		if d.Text != "" {
			got[d.key()] = toParsed(cra.Parse(d.Title, d.Text))
		}
	}
	golden := filepath.Join("testdata", "parsed-119.golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		writeGolden(t, golden, got)
	}
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]parsed
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for k, w := range want {
		g, gotJSON := got[k], ""
		if b, mErr := json.Marshal(g); mErr == nil {
			gotJSON = string(b)
		}
		wantJSON, _ := json.Marshal(w)
		if gotJSON != string(wantJSON) {
			t.Errorf("%s:\n got  %s\n want %s", k, gotJSON, wantJSON)
		}
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d resolutions, golden has %d", len(got), len(want))
	}

	checkCounts(t, got)
}

// writeGolden writes got as a JSON object with one resolution per line, sorted by key, so the
// file stays short and a change shows as one line of diff.
func writeGolden(t *testing.T, path string, got map[string]parsed) {
	t.Helper()
	var b strings.Builder
	b.WriteString("{\n")
	for i, k := range slices.Sorted(maps.Keys(got)) {
		line, err := json.Marshal(got[k])
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "%q: %s", k, line)
	}
	b.WriteString("\n}\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// checkCounts checks the measurements the design is built on
// (docs/design/590-cra-disapproved-rules.md), as this parser makes them.
func checkCounts(t *testing.T, got map[string]parsed) {
	t.Helper()
	var cited, twoCites, uncited, unparsed, withdrawals int
	for _, p := range got {
		switch {
		case p.Error != "":
			unparsed++
		case len(p.Citations) == 0:
			uncited++
		default:
			cited++
		}
		if len(p.Citations) == 2 {
			twoCites++
		}
		if p.Withdrawal {
			withdrawals++
		}
	}
	if unparsed != 0 || cited != 208 || twoCites != 58 || withdrawals != 65 || uncited != 26 {
		t.Errorf("counts changed: cited %d (want 208), two citations %d (58), withdrawals %d (65), "+
			"uncited %d (26), unparsed %d (0)", cited, twoCites, withdrawals, uncited, unparsed)
	}
}

func TestParse(t *testing.T) {
	const sjres18Title = "A joint resolution providing for congressional disapproval under chapter 8 of title 5, " +
		"United States Code, of the rule submitted by the Bureau of Consumer Financial Protection relating to " +
		"\"Overdraft Lending: Very Large Financial Institutions\"."
	sjres18 := `<section><enum/><text>That Congress disapproves the final rule submitted by the Bureau of ` +
		`Consumer Financial Protection relating to <quote>Overdraft Lending: Very Large Financial ` +
		`Institutions</quote> (89 Fed. Reg. 106768 (December 30, 2024)), and such rule shall have no force or ` +
		`effect. </text></section>`
	tests := []struct {
		name, title, text string
		want              parsed
	}{
		{
			name: "S.J.Res. 18, from its text", title: sjres18Title, text: sjres18,
			want: parsed{
				Agency:    "Bureau of Consumer Financial Protection",
				RuleTitle: "Overdraft Lending: Very Large Financial Institutions",
				Citations: []citation{{
					Text: "89 Fed. Reg. 106768 (December 30, 2024)", Volume: 89, Page: 106768, Date: "2024-12-30",
				}},
				FromText: true,
			},
		},
		{
			name: "S.J.Res. 18 with no text yet, from its title", title: sjres18Title,
			want: parsed{
				Agency:    "Bureau of Consumer Financial Protection",
				RuleTitle: "Overdraft Lending: Very Large Financial Institutions",
			},
		},
		{
			name: "a whole HTML text with entities",
			text: `<html><body><pre>JOINT RESOLUTION
    Resolved, That Congress disapproves the rule submitted by the
Department of Labor relating to &ldquo;Fiduciary Rule&rdquo; (88 FR 1,234;
published Sept. 9, 2023), and such rule shall have no force or effect.</pre></body></html>`,
			want: parsed{
				Agency: "Department of Labor", RuleTitle: "Fiduciary Rule",
				Citations: []citation{{
					Text: "88 FR 1,234; published Sept. 9, 2023", Volume: 88, Page: 1234, Date: "2023-09-09",
				}},
				IssuedOn: "2023-09-09", FromText: true,
			},
		},
		{
			name: "a date before the citation",
			text: `That Congress disapproves the rule submitted by the Department of Health and Human Services ` +
				`relating to Restoring Flexibility in the Child Care and Development Fund (CCDF) , published May 12, ` +
				`2026 (91 Fed. Reg. 25796), and such rule shall have no force or effect.`,
			want: parsed{
				Agency:    "Department of Health and Human Services",
				RuleTitle: "Restoring Flexibility in the Child Care and Development Fund (CCDF)",
				Citations: []citation{{Text: "91 Fed. Reg. 25796", Volume: 91, Page: 25796, Date: "2026-05-12"}},
				IssuedOn:  "2026-05-12", FromText: true,
			},
		},
		{
			name:  "a clause that names no agency falls back to the title",
			title: sjres18Title,
			text:  "That Congress disapproves the rule (89 Fed. Reg. 106768), and such rule shall have no force or effect.",
			want: parsed{
				Agency:    "Bureau of Consumer Financial Protection",
				RuleTitle: "Overdraft Lending: Very Large Financial Institutions",
				Citations: []citation{{Text: "89 Fed. Reg. 106768", Volume: 89, Page: 106768}},
			},
		},
		{
			name: "neither names a rule", title: "Disapproving the rule.", text: "<text>Nothing here.</text>",
			want: parsed{Error: cra.ErrUnparsed.Error()},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := cra.Parse(tt.title, tt.text)
			got, _ := json.Marshal(toParsed(r, err))
			want, _ := json.Marshal(tt.want)
			if string(got) != string(want) {
				t.Errorf("Parse:\n got  %s\n want %s", got, want)
			}
		})
	}
}

// TestParseAcceptance is #639's acceptance: S.J.Res. 18, S.J.Res. 143 (a withdrawal) and
// S.J.Res. 61 (a GAO opinion, no citation), from the 119th fixture.
func TestParseAcceptance(t *testing.T) {
	byKey := map[string]disapproval{}
	for _, d := range loadDisapprovals(t) {
		byKey[d.key()] = d
	}
	parse := func(key string) cra.Resolution {
		t.Helper()
		d := byKey[key]
		r, err := cra.Parse(d.Title, d.Text)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return r
	}

	r := parse("sjres18")
	last, ok := r.Disapproved()
	if r.Agency != "Bureau of Consumer Financial Protection" ||
		r.RuleTitle != "Overdraft Lending: Very Large Financial Institutions" || !ok ||
		last.Volume != 89 || last.Page != 106768 || day(last.Date) != "2024-12-30" {
		t.Errorf("S.J.Res. 18 = %+v", r)
	}
	if _, ok = r.Withdrawn(); ok {
		t.Error("S.J.Res. 18 has a withdrawn document")
	}

	r = parse("sjres143")
	withdrawn, wOK := r.Withdrawn()
	last, ok = r.Disapproved()
	if !r.Withdrawal || len(r.Citations) != 2 || !ok || !wOK ||
		withdrawn.Volume != 88 || withdrawn.Page != 33545 || last.Volume != 90 || last.Page != 20084 ||
		r.WithdrawnTitle != "Consumer Financial Protection Circular 2023–02: Reopening Deposit Accounts "+
			"That Consumers Previously Closed" {
		t.Errorf("S.J.Res. 143 = %+v", r)
	}

	r = parse("sjres61")
	if !r.GAOOpinion || len(r.Citations) != 0 || day(r.IssuedOn) != "2024-11-20" {
		t.Errorf("S.J.Res. 61 = %+v", r)
	}
	if _, ok = r.Disapproved(); ok {
		t.Error("S.J.Res. 61 has a disapproved citation")
	}
}

func TestPlainText(t *testing.T) {
	got := cra.PlainText("<text>relating to <quote>A</quote> ( <i>89</i> FR 1 ) , and &amp; more\n\t</text>")
	if want := "relating to A (89 FR 1), and & more"; got != want {
		t.Errorf("PlainText = %q, want %q", got, want)
	}
}
