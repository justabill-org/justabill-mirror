package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// The Clerk's index pages are gone (2026-10-01): with the index a 404, the House's roll numbers
// come from probing the roll files, and a year with no roll 1 has none. Past the last roll the
// Clerk answers 200 with an error document, as it really does.
func TestHouseRollNumbersWithoutTheIndex(t *testing.T) {
	const highest = 5
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var year, roll int
		if _, err := fmt.Sscanf(r.URL.Path, "/evs/%d/roll%d.xml", &year, &roll); err != nil {
			http.NotFound(w, r) // the index pages are gone
			return
		}
		switch {
		case year == 2025 && roll >= 1 && roll <= highest:
			_, _ = w.Write([]byte("<rollcall-vote/>"))
		case year == 2025:
			_, _ = fmt.Fprintf(w, `<xml>Error sanitizing file "roll%03d.xml". Please try again.</xml>`, roll)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s := sources{
		houseIndexURL: srv.URL + "/evs/%d/index.asp",
		houseRollURL:  srv.URL + "/evs/%d/roll%03d.xml",
		client:        srv.Client(),
	}

	got, err := s.rollNumbers(t.Context(), chamberHouse, 119, 1) // 2025
	if err != nil || !slices.Equal(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("rollNumbers(house, 119, 1) = %v, %v; want 1..5", got, err)
	}
	if got, err = s.rollNumbers(t.Context(), chamberHouse, 119, 2); err != nil || len(got) != 0 { // 2026
		t.Errorf("a year with no rolls: rollNumbers = %v, %v; want none", got, err)
	}
}
