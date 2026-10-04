package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/apikey"
)

// fullCoverage is a load that passes every threshold: 1,000 bills of each type upstream and
// stored, every roll call of two strata, full chambers.
func fullCoverage() coverage {
	up, stored := map[string]int{}, map[string]int{}
	for _, t := range billTypes() {
		up[t], stored[t] = 1000, 1000
	}
	return coverage{
		congress: 119,
		upstream: up,
		strata: []stratum{
			{Chamber: chamberHouse, Congress: 119, Session: 1, Numbers: []int{1, 2, 3}},
			{Chamber: chamberSenate, Congress: 119, Session: 1, Numbers: []int{1, 2}},
			{Chamber: chamberSenate, Congress: 119, Session: 2},
		},
		stored: &repository.StoredCoverage{
			BillsByType: stored,
			RollCalls: []repository.StoredRollCallCount{
				{Chamber: chamberHouse, Session: 1, Distinct: 3, Highest: 3},
				{Chamber: chamberSenate, Session: 1, Distinct: 2, Highest: 2},
			},
			MembersByChamber: map[string]int{chamberHouse: 445, chamberSenate: 101},
			VotedBills:       40,
			TextVersions:     90,
		},
	}
}

func checkCoverage(c coverage) (*report, string) {
	var out bytes.Buffer
	r := &report{w: &out}
	c.check(r)
	return r, out.String()
}

func TestCoveragePasses(t *testing.T) {
	r, out := checkCoverage(fullCoverage())
	if r.failures != 0 || r.warnings != 0 {
		t.Errorf("failures %d, warnings %d, want none:\n%s", r.failures, r.warnings, out)
	}
	for _, want := range []string{
		"bills hr: 1000 of 1000 on Congress.gov (100.0%)",
		"roll calls, House 2025 session 1: 3 of 3",
		"roll calls, Senate 2026 session 2: 0 of 0",
		"voted bills: all 40 stored",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestCoverageThresholds(t *testing.T) {
	tests := []struct {
		name     string
		change   func(c *coverage)
		failures int
		warnings int
		want     string
	}{
		{"bills at 99%", func(c *coverage) { c.stored.BillsByType["hr"] = 990 }, 0, 0, "bills hr: 990 of 1000"},
		{"bills under 99%", func(c *coverage) { c.stored.BillsByType["hr"] = 989 }, 1, 0,
			"FAIL  bills hr: 989 of 1000 on Congress.gov (98.9%), under 99%"},
		{"bills under 99% with the cut line", func(c *coverage) {
			c.stored.BillsByType["s"] = 10
			c.cutLine = true
		}, 0, 1, "WARN  bills s: 10 of 1000 on Congress.gov (1.0%), under 99%; allowed by --cut-line"},
		{"a roll call missing", func(c *coverage) { c.stored.RollCalls[0].Distinct = 2 }, 1, 0,
			"FAIL  roll calls, House 2025 session 1: 2 of 3 (66.7%)"},
		{"a stratum not loaded", func(c *coverage) { c.stored.RollCalls = c.stored.RollCalls[:1] }, 1, 0,
			"FAIL  roll calls, Senate 2025 session 1: 0 of 2 (0.0%)"},
		{"more stored than listed", func(c *coverage) { c.stored.RollCalls[1].Highest = 9 }, 0, 1,
			"but roll 9 is stored and the official list ends at 2"},
		{"a senator without an LIS ID", func(c *coverage) {
			c.stored.SenatorsWithoutLISID = []string{"L000570"}
		}, 1, 0, "FAIL  senators without an LIS ID: 1: L000570"},
		{"a voted bill missing", func(c *coverage) { c.stored.MissingVotedBills = []string{"hr-119-4"} }, 1, 0,
			"FAIL  voted bills: 1 of 40 not stored: hr-119-4"},
		{"a voted bill without a summary", func(c *coverage) {
			c.stored.VotedBillsWithoutSummary = []string{"hr-119-4"}
		}, 0, 1, "WARN  voted bills without a summary: 1: hr-119-4"},
		{"versions without text", func(c *coverage) { c.stored.VersionsWithoutText = 3 }, 0, 1,
			"WARN  text versions without text: 3 of 90"},
		{"a short Senate", func(c *coverage) { c.stored.MembersByChamber[chamberSenate] = 99 }, 0, 1,
			"members with a term in congress 119: House 445, Senate 99"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fullCoverage()
			tt.change(&c)
			r, out := checkCoverage(c)
			if r.failures != tt.failures || r.warnings != tt.warnings {
				t.Errorf("failures %d, warnings %d; want %d, %d:\n%s",
					r.failures, r.warnings, tt.failures, tt.warnings, out)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, out)
			}
		})
	}
}

func TestListIDs(t *testing.T) {
	ids := make([]string, 23)
	for i := range ids {
		ids[i] = "x"
	}
	if got := listIDs(ids); !strings.HasSuffix(got, "x and 3 more") || strings.Count(got, "x") != 20 {
		t.Errorf("listIDs = %q", got)
	}
	if got := listIDs([]string{"a", "b"}); got != "a, b" {
		t.Errorf("listIDs = %q", got)
	}
}

func TestCongressCounter(t *testing.T) {
	var gotKey, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath, gotQuery = r.Header.Get(apikey.Header), r.URL.Path, r.URL.RawQuery
		switch r.URL.Path {
		case "/bill/119/hr":
			_, _ = w.Write([]byte(`{"bills":[{}],"pagination":{"count":10614,"next":"x"}}`))
		case "/bill/119/sres":
			_, _ = w.Write([]byte(`{"bills":[]}`))
		case "/bill/119/moved":
			http.Redirect(w, r, "https://elsewhere.example/bill", http.StatusFound)
		default:
			http.Error(w, "nope", http.StatusTooManyRequests)
		}
	}))
	defer srv.Close()
	c := newCongressCounter("secret-key")
	c.baseURL = srv.URL

	n, err := c.billCount(t.Context(), 119, "hr")
	if err != nil || n != 10614 {
		t.Fatalf("count = %d, %v; want 10614", n, err)
	}
	if gotKey != "secret-key" || gotPath != "/bill/119/hr" || gotQuery != "format=json&limit=1" {
		t.Errorf("request: key %q, path %q, query %q", gotKey, gotPath, gotQuery)
	}
	if _, err = c.billCount(
		t.Context(),
		119,
		"sres",
	); err == nil ||
		!strings.Contains(err.Error(), "no pagination.count") {
		t.Errorf("no count: err = %v", err)
	}
	if _, err = c.billCount(t.Context(), 119, "hjres"); err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Errorf("429: err = %v", err)
	}
	// A redirect isn't followed, so the key header never leaves Congress.gov.
	if _, err = c.billCount(t.Context(), 119, "moved"); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Errorf("redirect: err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Errorf("error quotes the key: %v", err)
	}
}
