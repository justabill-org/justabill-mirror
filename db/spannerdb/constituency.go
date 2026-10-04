package spannerdb

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/scoring"
)

// stateCodeLen is the length of a state code, the start of every state or district scope key.
const stateCodeLen = 2

// constituencyCandidatesSQL is seatCandidatesSQL for one bill and one state, with each member's
// name and party in the roll call's congress.
const constituencyCandidatesSQL = `SELECT mv.member_id, cv.bill_id, cv.vote_id, cv.chamber, cv.congress,
       cv.session, cv.roll_number, cv.vote_date, cv.question, mv.vote, mt.state, mt.district,
       mt.party, m.first_name, m.last_name
FROM congressional_votes@{FORCE_INDEX=idx_cv_bill} AS cv
JOIN member_votes AS mv ON mv.vote_id = cv.vote_id
JOIN member_terms AS mt
  ON mt.member_id = mv.member_id AND mt.congress = cv.congress AND mt.chamber = cv.chamber
JOIN members AS m ON m.bioguide_id = mv.member_id
WHERE cv.bill_id = @billID AND cv.roll_number IS NOT NULL AND mt.state = @state`

// constituencyCandidateRow is one row of constituencyCandidatesSQL.
type constituencyCandidateRow struct {
	seatCandidateRow

	Party     string `spanner:"party"`
	FirstName string `spanner:"first_name"`
	LastName  string `spanner:"last_name"`
}

// constituencyMember is one member's seat in the constituency, and their roll calls on the bill.
type constituencyMember struct {
	row        constituencyCandidateRow
	candidates []scoring.Candidate
}

// ConstituencyPositions picks, with the scorecard rule (db/scoring), the position on the bill of
// each member whose seat in the roll call's congress and chamber was scopeKey. The seat is
// matched the way the aggregation job matches it (seatCandidateRow.scopeKey), so these are the
// votes rep_alignment compares with the cell.
func (r *AggregateRepository) ConstituencyPositions(
	ctx context.Context,
	billID, scopeKey string,
) ([]model.ConstituencyPosition, error) {
	if len(scopeKey) < stateCodeLen {
		return nil, nil
	}
	stmt := spanner.Statement{
		SQL:    constituencyCandidatesSQL,
		Params: map[string]any{paramBillID: billID, paramState: scopeKey[:stateCodeLen]},
	}
	members := map[seatKey]*constituencyMember{}
	err := eachRow(ctx, r.client.Single(), stmt, func(row *spanner.Row) error {
		var c constituencyCandidateRow
		if err := row.ToStruct(&c); err != nil {
			return err
		}
		if c.scopeKey() != scopeKey {
			return nil
		}
		k := seatKey{memberID: c.MemberID, congress: int(c.Congress), scopeKey: scopeKey}
		m, ok := members[k]
		if !ok {
			m = &constituencyMember{row: c}
			members[k] = m
		}
		m.candidates = append(m.candidates, scoring.Candidate{
			BillID: c.BillID, VoteID: c.VoteID, Chamber: c.Chamber, Congress: int(c.Congress),
			Session: int(c.Session.Int64), RollNumber: int(c.RollNumber), VoteDate: c.VoteDate,
			Question: c.Question.StringVal, Vote: c.Vote,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("constituency positions: %w", err)
	}
	positions := []model.ConstituencyPosition{}
	for _, m := range members {
		for _, p := range scoring.PickPositions(m.candidates) {
			positions = append(positions, model.ConstituencyPosition{
				MemberID: m.row.MemberID, FirstName: m.row.FirstName, LastName: m.row.LastName,
				Party: m.row.Party, Congress: p.Congress, Vote: p.Vote, VoteID: p.VoteID, Chamber: p.Chamber,
				VoteDate: p.VoteDate, Question: optionalString(p.Question),
			})
		}
	}
	slices.SortFunc(positions, func(a, b model.ConstituencyPosition) int {
		return cmp.Or(cmp.Compare(a.LastName, b.LastName), cmp.Compare(a.FirstName, b.FirstName),
			cmp.Compare(a.MemberID, b.MemberID), cmp.Compare(a.Congress, b.Congress))
	})
	return positions, nil
}
