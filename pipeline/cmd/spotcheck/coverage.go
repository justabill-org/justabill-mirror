package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/apikey"
)

const (
	congressBaseURL = "https://api.congress.gov/v3"
	// billShareFloor is the share of Congress.gov's bills of each type that must be stored, in
	// percent (design 77).
	billShareFloor = 99
	// Seats with a vote in the Senate, and House seats including the six delegates and the
	// resident commissioner's.
	senateSeats = 100
	houseSeats  = 441
)

// billTypes are Congress.gov's eight bill and resolution types, in its order.
func billTypes() []string {
	return []string{"hr", "s", "hjres", "sjres", "hconres", "sconres", "hres", "sres"}
}

// billCounter returns how many bills of a type Congress.gov has for a congress.
type billCounter interface {
	billCount(ctx context.Context, congress int, billType string) (int, error)
}

// congressCounter asks Congress.gov for list counts: one request per bill type, eight in all.
// It has its own small client so the check doesn't share code with the sync it checks.
type congressCounter struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func newCongressCounter(key string) congressCounter {
	return congressCounter{
		baseURL: congressBaseURL,
		apiKey:  key,
		// Never follow a redirect, so the key header can't go anywhere else.
		client: &http.Client{
			Timeout:       httpTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c congressCounter) billCount(ctx context.Context, congress int, billType string) (int, error) {
	path := fmt.Sprintf("/bill/%d/%s", congress, billType)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?format=json&limit=1", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set(apikey.Header, c.apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("congress.gov %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("congress.gov %s: HTTP %d", path, resp.StatusCode)
	}
	var page struct {
		Pagination struct {
			Count *int `json:"count"`
		} `json:"pagination"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&page); err != nil {
		return 0, fmt.Errorf("congress.gov %s: %w", path, err)
	}
	if page.Pagination.Count == nil {
		return 0, fmt.Errorf("congress.gov %s: no pagination.count", path)
	}
	return *page.Pagination.Count, nil
}

// coverage is what --coverage compares.
type coverage struct {
	congress int
	upstream map[string]int // bills per type on Congress.gov
	strata   []stratum
	stored   *repository.StoredCoverage
	cutLine  bool
}

// check reports each coverage count and fails it against the design's thresholds: every roll
// call, every voted bill, every senator's LIS ID, and 99% of Congress.gov's bills of each type.
// With cutLine a bill shortfall is only a warning: launch goes ahead with the voted bills and
// the catalog finishes loading afterwards (the #77 review's cut line).
func (c coverage) check(r *report) {
	r.sectionf("Coverage of congress %d", c.congress)
	c.checkBills(r)
	c.checkRollCalls(r)
	c.checkMembers(r)

	s := c.stored
	if n := len(s.MissingVotedBills); n > 0 {
		r.failf("voted bills: %d of %d not stored: %s", n, s.VotedBills, listIDs(s.MissingVotedBills))
	} else {
		r.okf("voted bills: all %d stored", s.VotedBills)
	}
	if n := len(s.VotedBillsWithoutSummary); n > 0 {
		r.warnf("voted bills without a summary: %d: %s", n, listIDs(s.VotedBillsWithoutSummary))
	} else {
		r.okf("voted bills without a summary: none")
	}
	if s.VersionsWithoutText > 0 {
		r.warnf("text versions without text: %d of %d", s.VersionsWithoutText, s.TextVersions)
	} else {
		r.okf("text versions without text: none of %d", s.TextVersions)
	}
}

func (c coverage) checkBills(r *report) {
	for _, t := range billTypes() {
		up, stored := c.upstream[t], c.stored.BillsByType[t]
		msg := fmt.Sprintf("bills %s: %d of %d on Congress.gov (%s)", t, stored, up, percent(stored, up))
		switch {
		case stored*100 >= up*billShareFloor:
			r.okf("%s", msg)
		case c.cutLine:
			r.warnf("%s, under %d%%; allowed by --cut-line", msg, billShareFloor)
		default:
			r.failf("%s, under %d%%", msg, billShareFloor)
		}
	}
}

func (c coverage) checkRollCalls(r *report) {
	for _, st := range c.strata {
		var stored repository.StoredRollCallCount
		for _, rc := range c.stored.RollCalls {
			if rc.Chamber == st.Chamber && rc.Session == st.Session {
				stored = rc
			}
		}
		// The official lists run from 1 to the highest number with no gaps, so the count listed
		// is the highest roll number; counting the list also copes with a gap.
		want := len(st.Numbers)
		msg := fmt.Sprintf("roll calls, %s: %d of %d", st, stored.Distinct, want)
		switch {
		case stored.Distinct < want:
			r.failf("%s (%s)", msg, percent(stored.Distinct, want))
		case stored.Highest > st.highest():
			r.warnf("%s, but roll %d is stored and the official list ends at %d", msg, stored.Highest, st.highest())
		default:
			r.okf("%s", msg)
		}
	}
}

func (c coverage) checkMembers(r *report) {
	house, senate := c.stored.MembersByChamber[chamberHouse], c.stored.MembersByChamber[chamberSenate]
	msg := fmt.Sprintf("members with a term in congress %d: House %d, Senate %d", c.congress, house, senate)
	if house < houseSeats || senate < senateSeats {
		r.warnf("%s; fewer than the %d House and %d Senate seats", msg, houseSeats, senateSeats)
	} else {
		r.okf("%s", msg)
	}
	if n := len(c.stored.SenatorsWithoutLISID); n > 0 {
		r.failf("senators without an LIS ID: %d: %s", n, listIDs(c.stored.SenatorsWithoutLISID))
	} else {
		r.okf("senators without an LIS ID: none")
	}
}

// percent formats part/whole to one decimal; an empty whole is 100%.
func percent(part, whole int) string {
	if whole == 0 {
		return "100.0%"
	}
	return fmt.Sprintf("%.1f%%", float64(part)*100/float64(whole))
}
