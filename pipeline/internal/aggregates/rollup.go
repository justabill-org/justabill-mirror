package aggregates

import (
	"strconv"
	"strings"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// rollUp adds the snapshot's groups into national, state and district cells. Votes from users
// without a state count only nationally, and those with a state but no district nationally and
// in the state.
func rollUp(groups []repository.AggregateVoteGroup) map[CellKey]*Counts {
	cells := map[CellKey]*Counts{}
	add := func(g *repository.AggregateVoteGroup, scope, scopeKey string) {
		k := CellKey{BillID: g.BillID, Scope: scope, ScopeKey: scopeKey}
		c := cells[k]
		if c == nil {
			c = &Counts{}
			cells[k] = c
		}
		c.Yea += g.Yea
		c.Nay += g.Nay
		c.Recent += g.Recent
		c.Baseline += g.Baseline
		c.New += g.New[scope]
		c.Young += g.Young[scope]
	}
	for i := range groups {
		g := &groups[i]
		add(g, scopeNational, "")
		if g.State == "" {
			continue
		}
		add(g, scopeState, g.State)
		if g.District != nil {
			add(g, scopeDistrict, districtKey(g.State, *g.District))
		}
	}
	return cells
}

// districtKey is a district's scope key: "CA-12", or "AK-0" for an at-large seat.
func districtKey(state string, district int) string {
	return state + "-" + strconv.Itoa(district)
}

// alignKey names one rep_alignment row.
type alignKey struct {
	memberID string
	congress int
	scopeKey string
}

// alignments compares each member's Yea or Nay position with the majority of their
// constituency's shown cell on the same bill: the district cell for a House member, the state
// cell for a senator. A tie has no majority and isn't compared; Present, Not Voting and other
// positions are left out, as in the scorecard (docs/design/69-scorecard-methodology.md).
func alignments(
	shown map[CellKey]model.VoteAggregate,
	positions []repository.SeatPosition,
) map[alignKey]*model.RepAlignment {
	rows := map[alignKey]*model.RepAlignment{}
	for _, p := range positions {
		if p.Vote != model.PositionYea && p.Vote != model.PositionNay {
			continue
		}
		scope := scopeState
		if strings.Contains(p.ScopeKey, "-") {
			scope = scopeDistrict
		}
		cell, ok := shown[CellKey{BillID: p.BillID, Scope: scope, ScopeKey: p.ScopeKey}]
		if !ok || cell.YeaPct == nil || cell.NayPct == nil || *cell.YeaPct == *cell.NayPct {
			continue
		}
		majority := model.PositionNay
		if *cell.YeaPct > *cell.NayPct {
			majority = model.PositionYea
		}
		k := alignKey{memberID: p.MemberID, congress: p.Congress, scopeKey: p.ScopeKey}
		row := rows[k]
		if row == nil {
			row = &model.RepAlignment{MemberID: p.MemberID, Congress: p.Congress, ScopeKey: p.ScopeKey}
			rows[k] = row
		}
		row.BillsCompared++
		if p.Vote == majority {
			row.BillsAgreed++
		}
	}
	return rows
}
