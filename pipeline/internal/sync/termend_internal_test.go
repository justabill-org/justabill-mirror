package sync

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

func TestTermEndDate(t *testing.T) {
	house := func(start int, end ...int) congress.MemberTerm {
		return memberTerm(listChamberHouse, start, end...)
	}
	senate := func(start int, end ...int) congress.MemberTerm {
		return memberTerm(chamberSenate, start, end...)
	}
	for _, tt := range []struct {
		name     string
		terms    []congress.MemberTerm
		chamber  string
		congress int
		want     string // "" for nil
	}{
		// endYear 2025 is clamped to the 119th's first day, not January 1.
		{"senator who left in 2025 (Rubio)", []congress.MemberTerm{senate(2011, 2025)}, chamberSenate, 119, "2025-01-03"},
		{"died in office (Sylvester Turner)", []congress.MemberTerm{house(2025, 2025)}, chamberHouse, 119, "2025-01-03"},
		{"resigned in 2026 (Greene)", []congress.MemberTerm{house(2021, 2026)}, chamberHouse, 119, "2026-01-01"},
		{"serving", []congress.MemberTerm{house(2003)}, chamberHouse, 119, ""},
		// Banks left the House for the Senate: only the Senate term counts for his Senate seat.
		{"moved to the Senate (Banks)", []congress.MemberTerm{house(2017, 2025), senate(2025)}, chamberSenate, 119, ""},
		// Tenney's first House term ended in 2019; she has served again since 2021.
		{"came back (Tenney)", []congress.MemberTerm{house(2017, 2019), house(2021)}, chamberHouse, 119, ""},
		{"no terms in the list", nil, chamberSenate, 119, ""},
		{"only an older term", []congress.MemberTerm{house(2017, 2019)}, chamberHouse, 119, ""},
		{"past congress", []congress.MemberTerm{senate(2011, 2025)}, chamberSenate, 118, "2025-01-01"},
		// A term that starts with the next congress isn't this congress's.
		{"next congress's term", []congress.MemberTerm{house(2019, 2025), house(2027)}, chamberHouse, 118, "2025-01-01"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := termEndDate(tt.terms, tt.chamber, tt.congress)
			if gotStr := formatDate(got); gotStr != tt.want {
				t.Errorf("termEndDate = %q, want %q", gotStr, tt.want)
			}
		})
	}
}

func memberTerm(chamber string, start int, end ...int) congress.MemberTerm {
	t := congress.MemberTerm{Chamber: chamber, StartYear: start}
	if len(end) > 0 {
		t.EndYear = new(end[0])
	}
	return t
}

func formatDate(d *time.Time) string {
	if d == nil {
		return ""
	}
	return d.Format(time.DateOnly)
}

// SyncMembers reads each member's terms from the list it already fetches and writes the end
// date of a term that ended (members of the 119th as Congress.gov listed them on 2026-09-27).
func TestSyncMembers_WritesTermEnds(t *testing.T) {
	store := newPastTermsStore(nil, nil)
	var paths []string
	s := serviceWithAPI(t, store, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if !strings.HasPrefix(r.URL.Path, "/member/congress/") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"members": [
			{"bioguideId": "R000595", "name": "Rubio, Marco", "state": "Florida", "district": null,
			 "partyName": "Republican",
			 "terms": {"item": [{"chamber": "Senate", "endYear": 2025, "startYear": 2011}]}},
			{"bioguideId": "B001299", "name": "Banks, Jim", "state": "Indiana", "district": null,
			 "partyName": "Republican", "terms": {"item": [
				{"chamber": "House of Representatives", "endYear": 2025, "startYear": 2017},
				{"chamber": "Senate", "startYear": 2025}]}},
			{"bioguideId": "T000489", "name": "Turner, Sylvester", "state": "Texas", "district": 18,
			 "partyName": "Democratic",
			 "terms": {"item": [{"chamber": "House of Representatives", "endYear": 2025, "startYear": 2025}]}},
			{"bioguideId": "T000478", "name": "Tenney, Claudia", "state": "New York", "district": 24,
			 "partyName": "Republican", "terms": {"item": [
				{"chamber": "House of Representatives", "endYear": 2019, "startYear": 2017},
				{"chamber": "House of Representatives", "startYear": 2021}]}}
		], "pagination": {"count": 4}}`)
	})

	if err := s.SyncMembers(t.Context(), 119); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"R000595": "2025-01-03", "B001299": "", "T000489": "2025-01-03", "T000478": ""}
	if len(store.terms) != len(want) {
		t.Fatalf("wrote %d terms, want %d: %+v", len(store.terms), len(want), store.terms)
	}
	for _, term := range store.terms {
		if got := formatDate(term.EndDate); got != want[term.MemberID] {
			t.Errorf("%s %s end date = %q, want %q", term.MemberID, term.Chamber, got, want[term.MemberID])
		}
	}
	// The terms come from the list, and senators' LIS IDs from the Senate's feed (#343), so the
	// list is the only Congress.gov request: no member details.
	if len(paths) != 1 {
		t.Errorf("requests = %v, want only the member list", paths)
	}
}
