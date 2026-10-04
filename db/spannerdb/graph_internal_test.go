package spannerdb

import (
	"reflect"
	"testing"
)

// TestNewestRollCalls checks which roll calls the companion-votes read fetches member votes for:
// the newest ones until their tallies reach the limit, a roll call without a tally counting as a
// full chamber.
func TestNewestRollCalls(t *testing.T) {
	rc := func(id, chamber string, tally int64) companionRollCallRow {
		return companionRollCallRow{VoteID: id, Chamber: chamber, Tally: tally}
	}
	tests := []struct {
		name  string
		in    []companionRollCallRow
		limit int64
		want  []string
	}{
		{"tallies reach the limit", []companionRollCallRow{
			rc("a", "House", 430), rc("b", "House", 431), rc("c", "House", 432),
		}, 800, []string{"a", "b"}},
		{"exactly the limit", []companionRollCallRow{
			rc("a", "Senate", 100), rc("b", "Senate", 100), rc("c", "Senate", 100),
		}, 200, []string{"a", "b"}},
		{"untallied Senate roll calls count 100", []companionRollCallRow{
			rc("a", "Senate", 0), rc("b", "Senate", 0), rc("c", "Senate", 0), rc("d", "Senate", 0),
		}, 250, []string{"a", "b", "c"}},
		{"an untallied House roll call counts 435", []companionRollCallRow{
			rc("a", "House", 0), rc("b", "House", 0),
		}, 400, []string{"a"}},
		{"too few to reach the limit", []companionRollCallRow{
			rc("a", "House", 430), rc("b", "Senate", 98),
		}, 2000, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, r := range newestRollCalls(tt.in, tt.limit) {
				got = append(got, r.VoteID)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newestRollCalls = %v, want %v", got, tt.want)
			}
		})
	}
}
