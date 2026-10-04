package sync

import (
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// listChamberHouse is how the Congress.gov member list names the House in a member's terms.
const listChamberHouse = "House of Representatives"

// termEndDate returns the end_date to store for a member's term in chamber ("House" or
// "Senate") during a congress, from the member list's terms: nil while the member still serves.
// The list gives only the year a member left, so the date is January 1 of that year, moved up
// to the congress's first day when that's later. It has year precision, which is enough for the
// "has this member left" check GetByDistrict and GetSenators make (docs/design/70-district-lookup.md,
// Option H). A member with no term in chamber that overlaps the congress counts as serving.
func termEndDate(terms []congress.MemberTerm, chamber string, congressNum int) *time.Time {
	listChamber := chamber
	if chamber == chamberHouse {
		listChamber = listChamberHouse
	}
	start, end := congressDates(congressNum)

	// The overlapping term that started last: a member who came back (Tenney: 2017–2019, then
	// 2021 on) has an older, ended term in the same chamber.
	var match *congress.MemberTerm
	for i := range terms {
		t := &terms[i]
		if t.Chamber != listChamber || t.StartYear >= end.Year() || (t.EndYear != nil && *t.EndYear < start.Year()) {
			continue
		}
		if match == nil || t.StartYear > match.StartYear {
			match = t
		}
	}
	if match == nil || match.EndYear == nil {
		return nil
	}
	endDate := time.Date(*match.EndYear, time.January, 1, 0, 0, 0, 0, time.UTC)
	if endDate.Before(start) {
		endDate = start
	}
	return &endDate
}
