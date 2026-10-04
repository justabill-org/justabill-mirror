package spannerdb

import (
	"context"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/scoring"
)

// paramMembers is the scorecard queries' member ID list parameter (paramUserID is in users.go).
const paramMembers = "members"

// candidatesSQL reads the members' recorded roll calls on bills: the candidates the scorecard
// rule (db/scoring) picks positions from. idx_member_votes_member serves the member_id lookup,
// since member_votes' own key starts with vote_id. A member only has rows on their own
// chamber's roll calls, so the chamber needs no filter. The bill join is a LEFT JOIN so a roll
// call on a bill that isn't synced yet still counts, with an empty title.
const candidatesSQL = `SELECT mv.member_id, cv.bill_id, b.title AS bill_title, cv.vote_id, cv.chamber,
       cv.congress, cv.session, cv.roll_number, cv.vote_date, cv.question, mv.vote
FROM member_votes@{FORCE_INDEX=idx_member_votes_member} AS mv
JOIN congressional_votes AS cv ON cv.vote_id = mv.vote_id
LEFT JOIN bills AS b ON b.bill_id = cv.bill_id
WHERE mv.member_id IN UNNEST(@members)
  AND cv.bill_id IS NOT NULL AND cv.roll_number IS NOT NULL
  AND (@congress = 0 OR cv.congress = @congress)`

// candidateRow is one row of candidatesSQL.
type candidateRow struct {
	MemberID   string             `spanner:"member_id"`
	BillID     string             `spanner:"bill_id"`
	BillTitle  spanner.NullString `spanner:"bill_title"`
	VoteID     string             `spanner:"vote_id"`
	Chamber    string             `spanner:"chamber"`
	Congress   int64              `spanner:"congress"`
	Session    spanner.NullInt64  `spanner:"session"`
	RollNumber int64              `spanner:"roll_number"`
	VoteDate   time.Time          `spanner:"vote_date"`
	Question   spanner.NullString `spanner:"question"`
	Vote       string             `spanner:"vote"`
}

func (r candidateRow) candidate() scoring.Candidate {
	return scoring.Candidate{
		BillID:     r.BillID,
		BillTitle:  r.BillTitle.StringVal,
		VoteID:     r.VoteID,
		Chamber:    r.Chamber,
		Congress:   int(r.Congress),
		Session:    int(r.Session.Int64),
		RollNumber: int(r.RollNumber),
		VoteDate:   r.VoteDate,
		Question:   r.Question.StringVal,
		Vote:       r.Vote,
	}
}

// memberPositions applies the scorecard rule to each member's roll calls in congress (0 for
// every congress) and returns their positions by member ID, newest first. Members with no
// positions aren't keys.
func memberPositions(
	ctx context.Context,
	q querier,
	memberIDs []string,
	congress int,
) (map[string][]scoring.Candidate, error) {
	positions := map[string][]scoring.Candidate{}
	if len(memberIDs) == 0 {
		return positions, nil
	}
	stmt := spanner.Statement{
		SQL:    candidatesSQL,
		Params: map[string]any{paramMembers: memberIDs, paramCongress: int64(congress)},
	}
	candidates := map[string][]scoring.Candidate{}
	err := eachRow(ctx, q, stmt, func(row *spanner.Row) error {
		var r candidateRow
		if err := row.ToStruct(&r); err != nil {
			return err
		}
		candidates[r.MemberID] = append(candidates[r.MemberID], r.candidate())
		return nil
	})
	if err != nil {
		return nil, err
	}
	for id, cs := range candidates {
		if picked := scoring.PickPositions(cs); len(picked) > 0 {
			positions[id] = picked
		}
	}
	return positions, nil
}
