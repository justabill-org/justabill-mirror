package handler

import (
	"context"
	"maps"
	"time"

	"github.com/justabill-org/justabill/api/internal/district"
)

// electionGrace is how much longer FindReps waits for the next congress's map once the
// current one has answered (docs/design/237-election-district.md).
const electionGrace = 2 * time.Second

// Outcomes of the next-map lookup, logged as election_district on the "reps lookup" line.
const (
	electionChanged = "changed" // the election block is there and the districts differ
	electionSame    = "same"    // the election block is there and claims no change
	electionMissing = "missing" // the next map matched no district, so there's no block
	electionError   = "error"   // the next lookup failed, so there's no block
	electionTimeout = "timeout" // the next lookup outlasted the grace and was cancelled
	electionNone    = "none"    // there's no map for the next congress, so nothing was asked
)

// repsDistrict is one district in a /reps response.
type repsDistrict struct {
	State    string `json:"state"`
	District int    `json:"district"`
	AtLarge  bool   `json:"at_large"`
	Congress int    `json:"congress"`
	Source   string `json:"source"`
}

// electionBlock is the /reps "election" object: the districts an address votes in at the
// general election that elects the next congress.
type electionBlock struct {
	Congress     int            `json:"congress"`
	ElectionDate string         `json:"election_date"` // YYYY-MM-DD
	Districts    []repsDistrict `json:"districts"`
	// Changed is true when the next map's districts differ from the current map's. It's
	// false when either is empty: no match makes no claim.
	Changed bool `json:"changed"`
}

// nextResult is what the next-map lookup found.
type nextResult struct {
	districts []district.Result
	err       error
}

// startElection looks the place (an address or a point) up in the next congress's map in the
// background, when there's a map for it; otherwise the channel is nil. Call the cancel func once
// the result is no longer wanted.
func (h *Handler) startElection(
	ctx context.Context, place repsPlace, current int,
) (<-chan nextResult, context.CancelFunc) {
	if !district.HasMap(current + 1) {
		return nil, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	return h.nextMapLookup(ctx, place, current+1), cancel
}

// nextMapLookup looks the place up in a congress's map in the background. The channel holds
// the one result, so the goroutine finishes even when FindReps has stopped waiting for it.
func (h *Handler) nextMapLookup(ctx context.Context, place repsPlace, congress int) <-chan nextResult {
	ch := make(chan nextResult, 1)
	go func() {
		districts, err := place.lookup(ctx, h.District, congress)
		ch <- nextResult{districts: districts, err: err}
	}()
	return ch
}

// awaitElection waits at most the grace for the next-map lookup and builds the election block
// from it. The block is nil unless the next map matched a district; the string is the outcome
// for the log.
func (h *Handler) awaitElection(
	ctx context.Context, next <-chan nextResult, congress int, current []district.Result,
) (*electionBlock, string) {
	if next == nil {
		return nil, electionNone
	}
	timer := time.NewTimer(electionGrace)
	defer timer.Stop()

	var res nextResult
	select {
	case res = <-next:
	case <-timer.C:
		return nil, electionTimeout
	}
	if res.err != nil {
		// Never log the address or the point: errors from the lookup don't carry them either.
		h.log.WarnContext(ctx, "election district lookup failed", "congress", congress, keyError, res.err)
		return nil, electionError
	}
	if len(res.districts) == 0 {
		return nil, electionMissing
	}

	block := &electionBlock{
		Congress:     congress,
		ElectionDate: district.ElectionDate(congress).Format(time.DateOnly),
		Districts:    toRepsDistricts(res.districts, congress),
		Changed:      districtsDiffer(current, res.districts),
	}
	if block.Changed {
		return block, electionChanged
	}
	return block, electionSame
}

// toRepsDistricts converts lookup results in a congress's map to their response form.
func toRepsDistricts(results []district.Result, congress int) []repsDistrict {
	out := make([]repsDistrict, 0, len(results))
	for _, d := range results {
		out = append(out, repsDistrict{
			State: d.State, District: d.District, AtLarge: d.AtLarge, Congress: congress, Source: d.Source,
		})
	}
	return out
}

// districtsDiffer compares two lookups' sets of (state, district) pairs. It's false when either
// is empty.
func districtsDiffer(a, b []district.Result) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	type seat struct {
		state    string
		district int
	}
	setOf := func(rs []district.Result) map[seat]bool {
		s := make(map[seat]bool, len(rs))
		for _, r := range rs {
			s[seat{r.State, r.District}] = true
		}
		return s
	}
	return !maps.Equal(setOf(a), setOf(b))
}
