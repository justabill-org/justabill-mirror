package sync

import (
	"cmp"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

// Tally adds up what a backfill run stored, for the one summary line a past-congress load logs
// (docs/design/78-118th-backfill.md): votes per chamber and session, voted bills fetched,
// failed and unresolved, and the senators no stored member matched. A Service counts into it
// once SetTally is called; it is safe for concurrent use.
type Tally struct {
	mu                sync.Mutex
	votes             map[voteKey]int
	billsFetched      int
	billsFailed       int
	billsUnresolved   int
	unmatchedSenators map[string]bool
}

// voteKey is one chamber's roll calls in one session.
type voteKey struct {
	chamber string
	session int
}

// NewTally returns an empty tally.
func NewTally() *Tally {
	return &Tally{votes: map[voteKey]int{}, unmatchedSenators: map[string]bool{}}
}

// SetTally makes the service count what it stores into t. A nil t counts nothing.
func (s *Service) SetTally(t *Tally) { s.tally = t }

// addVotes counts one session's stored roll calls.
func (t *Tally) addVotes(session, house, senate int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.votes[voteKey{chamberHouse, session}] += house
	t.votes[voteKey{chamberSenate, session}] += senate
}

// addBills counts the outcome of one SyncBillsByID run.
func (t *Tally) addBills(fetched, failed, unresolved int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.billsFetched += fetched
	t.billsFailed += failed
	t.billsUnresolved += unresolved
}

// addUnmatchedSenator records a Senate LIS ID whose positions weren't stored because no member
// matched it. Each ID counts once, however many roll calls it appears on.
func (t *Tally) addUnmatchedSenator(lisID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.unmatchedSenators[lisID] = true
}

// Votes returns how many roll calls were stored for a chamber ("House" or "Senate") in a
// session.
func (t *Tally) Votes(chamber string, session int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.votes[voteKey{chamber, session}]
}

// UnmatchedSenators returns the LIS IDs no stored member matched, sorted.
func (t *Tally) UnmatchedSenators() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.unmatchedSenators))
	for id := range t.unmatchedSenators {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Attrs returns the tally as log attributes: a "votes" group keyed house_s1, senate_s2 and so
// on (only the sessions that ran), then the bill and senator counts.
func (t *Tally) Attrs() []any {
	t.mu.Lock()
	keys := make([]voteKey, 0, len(t.votes))
	for k := range t.votes {
		keys = append(keys, k)
	}
	// "House" sorts before "Senate".
	slices.SortFunc(keys, func(a, b voteKey) int {
		return cmp.Or(cmp.Compare(a.chamber, b.chamber), cmp.Compare(a.session, b.session))
	})
	votes := make([]any, 0, len(keys))
	for _, k := range keys {
		votes = append(votes, slog.Int(fmt.Sprintf("%s_s%d", strings.ToLower(k.chamber), k.session), t.votes[k]))
	}
	fetched, failed, unresolved := t.billsFetched, t.billsFailed, t.billsUnresolved
	t.mu.Unlock()

	unmatched := t.UnmatchedSenators()
	return []any{
		slog.Group("votes", votes...),
		"bills_fetched", fetched,
		"bills_failed", failed,
		"unresolved_bill_refs", unresolved,
		"unmatched_senators", len(unmatched),
		"unmatched_senator_lis_ids", unmatched,
	}
}
