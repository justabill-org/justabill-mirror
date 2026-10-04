package main

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// comparer compares an official roll call with the stored one.
type comparer struct {
	// eastern is America/New_York when the zone database is available. The pipeline stores the
	// clerk's wall-clock time as UTC today; a stored date also matches if it's right in Eastern
	// time, so storing the true instant later doesn't turn evening votes into mismatches.
	eastern *time.Location
}

func newComparer() comparer {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	return comparer{eastern: loc}
}

// compare returns every difference between the official and the stored roll call, in a stable
// order, or nil when they match. A stored nil means the roll call isn't stored at all.
func (c comparer) compare(off *officialRollCall, stored *repository.StoredRollCall) []string {
	if stored == nil {
		return []string{"not stored"}
	}
	var diffs []string
	field := func(name, official, got string) {
		if official != got {
			diffs = append(diffs, fmt.Sprintf("%s: official %q, stored %q", name, official, got))
		}
	}
	field("question", off.Question, squash(deref(stored.Question)))
	field("result", off.Result, squash(deref(stored.Result)))
	field("bill", off.BillID, deref(stored.BillID))
	if d := stored.VoteDate.UTC().Format(isoDate); d != off.Date &&
		stored.VoteDate.In(c.eastern).Format(isoDate) != off.Date {
		diffs = append(diffs, fmt.Sprintf("date: official %s, stored %s", off.Date, d))
	}
	total := func(name string, official int, got *int) {
		switch {
		case got == nil:
			diffs = append(diffs, fmt.Sprintf("%s: official %d, stored none", name, official))
		case *got != official:
			diffs = append(diffs, fmt.Sprintf("%s: official %d, stored %d", name, official, *got))
		}
	}
	total("yeas", off.Yeas, stored.Yeas)
	total("nays", off.Nays, stored.Nays)
	total("present", off.Present, stored.Present)
	total("not voting", off.NotVoting, stored.NotVoting)

	return append(diffs, comparePositions(off, stored.Positions)...)
}

// comparePositions compares every member's position. The official value goes through #66's
// mapping (model.NormalizeMemberVote), which is what the pipeline stores; a stored value that
// isn't in canonical form is a difference. Senate positions are matched by LIS ID.
func comparePositions(off *officialRollCall, stored []repository.StoredPosition) []string {
	var diffs []string
	byID := make(map[string]string, len(stored))
	for _, p := range stored {
		id := p.MemberID
		if off.Chamber == chamberSenate {
			if p.LISID == "" {
				diffs = append(diffs, fmt.Sprintf("member %s: stored %q, but the member has no LIS ID",
					p.MemberID, p.Vote))
				continue
			}
			id = p.LISID
		}
		byID[id] = p.Vote
	}

	for _, id := range sortedKeys(off.Positions) {
		want, _ := model.NormalizeMemberVote(off.Positions[id])
		got, ok := byID[id]
		switch {
		case !ok:
			diffs = append(diffs, fmt.Sprintf("member %s: official %q, not stored", id, want))
		case got != want:
			diffs = append(diffs, fmt.Sprintf("member %s: official %q, stored %q", id, want, got))
		}
	}
	for _, id := range sortedKeys(byID) {
		if _, ok := off.Positions[id]; !ok {
			diffs = append(diffs, fmt.Sprintf("member %s: stored %q, not in the official record", id, byID[id]))
		}
	}
	return diffs
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
