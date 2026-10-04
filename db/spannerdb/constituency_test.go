package spannerdb_test

import (
	"strconv"
	"testing"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/testdb"
)

func TestConstituencyPositions(t *testing.T) {
	reader, _ := newAggregateRepos(t)
	ctx := t.Context()

	tests := []struct {
		name, billID, scopeKey string
		want                   []string // member|congress|vote|chamber
	}{
		{"district", testdb.FixtureHouseBill, "CA-12", []string{testdb.FixtureHouseDem + "|119|yea|House"}},
		{"other district", testdb.FixtureHouseBill, "TX-7", []string{testdb.FixtureHouseRep + "|119|nay|House"}},
		{"state of a House-only roll call", testdb.FixtureHouseBill, "CA", nil},
		{"district with no member", testdb.FixtureHouseBill, "CA-1", nil},
		{"state", testdb.FixtureSenateBill, "OH", []string{testdb.FixtureSenatorRep + "|119|yea|Senate"}},
		{"bill without roll calls", "hr-119-999", "CA-12", nil},
		{"empty key", testdb.FixtureHouseBill, "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := reader.ConstituencyPositions(ctx, tc.billID, tc.scopeKey)
			if err != nil {
				t.Fatalf("ConstituencyPositions: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %v", got, tc.want)
			}
			for i, p := range got {
				if key := positionKey(p); key != tc.want[i] {
					t.Errorf("position %d = %s, want %s", i, key, tc.want[i])
				}
				if p.FirstName == "" || p.LastName == "" || p.Party == "" || p.VoteID == "" || p.VoteDate.IsZero() {
					t.Errorf("position %d is missing a field: %+v", i, p)
				}
			}
		})
	}
}

func positionKey(p model.ConstituencyPosition) string {
	return p.MemberID + "|" + strconv.Itoa(p.Congress) + "|" + p.Vote + "|" + p.Chamber
}
