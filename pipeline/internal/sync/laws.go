package sync

import (
	"regexp"
	"slices"
	"strings"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// lawNumberPattern is a law's number: its congress, then its place in that congress's sequence.
var lawNumberPattern = regexp.MustCompile(`^\d{1,3}-\d{1,5}$`)

// BillLaws returns the laws a bill detail lists (#709), in its order. It keeps only public and
// private laws with a well-formed number, as [model.BillLawTypePublic] or [model.BillLawTypePrivate],
// and drops repeats; a bill that isn't law has none.
func BillLaws(detail *congress.BillDetail) []model.BillLaw {
	var laws []model.BillLaw
	for _, l := range detail.Laws {
		law := model.BillLaw{Number: strings.TrimSpace(l.Number)}
		switch {
		case strings.EqualFold(strings.TrimSpace(l.Type), model.BillLawTypePublic):
			law.Type = model.BillLawTypePublic
		case strings.EqualFold(strings.TrimSpace(l.Type), model.BillLawTypePrivate):
			law.Type = model.BillLawTypePrivate
		default:
			continue
		}
		if !lawNumberPattern.MatchString(law.Number) || slices.Contains(laws, law) {
			continue
		}
		laws = append(laws, law)
	}
	return laws
}
