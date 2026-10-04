package spannerdb

import (
	"cmp"
	"context"
	"slices"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/scoring"
)

// ScorecardService implements repository.Scorecard with Spanner. Both methods score through
// VoteRepo.MemberPositions' rule and scoring.Score, so they always agree
// (docs/design/69-scorecard-methodology.md).
type ScorecardService struct {
	client *spanner.Client
}

// userRepsSQL finds the members holding the user's seats in the current congress: their state's
// senators and their district's House member, the same members GetByDistrict and GetSenators
// return. A term with an end_date means the member has left the seat, so it's skipped (#527). A
// member with two current terms (a House member appointed to the Senate) comes back twice,
// newest term first.
const userRepsSQL = `SELECT mt.member_id, m.first_name, m.last_name, mt.chamber, mt.party
FROM users AS u
JOIN member_terms AS mt ON mt.state = u.state
JOIN members AS m ON m.bioguide_id = mt.member_id
WHERE u.user_id = @userID
  AND mt.congress = (SELECT number FROM congresses WHERE is_current = TRUE LIMIT 1)
  AND (mt.chamber = 'Senate' OR (mt.chamber = 'House' AND mt.district = u.district))
  AND mt.end_date IS NULL
ORDER BY mt.member_id, mt.start_date DESC`

type repTermRow struct {
	MemberID  string `spanner:"member_id"`
	FirstName string `spanner:"first_name"`
	LastName  string `spanner:"last_name"`
	Chamber   string `spanner:"chamber"`
	Party     string `spanner:"party"`
}

// userRep is a member holding one of the user's seats, with the chamber and party of that seat.
type userRep struct {
	id, name, chamber, party string
}

// userReps returns the user's current representatives, House first, then by name.
func userReps(ctx context.Context, q querier, userID string) ([]*userRep, error) {
	var reps []*userRep
	seen := map[string]bool{}
	stmt := spanner.Statement{SQL: userRepsSQL, Params: map[string]any{paramUserID: userID}}
	err := eachRow(ctx, q, stmt, func(row *spanner.Row) error {
		var t repTermRow
		if err := row.ToStruct(&t); err != nil {
			return err
		}
		if !seen[t.MemberID] {
			seen[t.MemberID] = true
			reps = append(reps, &userRep{
				id: t.MemberID, name: t.FirstName + " " + t.LastName, chamber: t.Chamber, party: t.Party,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(reps, func(a, b *userRep) int {
		return cmp.Or(cmp.Compare(a.chamber, b.chamber), cmp.Compare(a.name, b.name), cmp.Compare(a.id, b.id))
	})
	return reps, nil
}

// userVotes returns the user's votes by bill ID.
func userVotes(ctx context.Context, q querier, userID string) (map[string]string, error) {
	votes := map[string]string{}
	stmt := spanner.Statement{
		SQL:    `SELECT bill_id, vote FROM user_votes WHERE user_id = @userID`,
		Params: map[string]any{paramUserID: userID},
	}
	err := eachRow(ctx, q, stmt, func(row *spanner.Row) error {
		var billID, vote string
		if err := row.Columns(&billID, &vote); err != nil {
			return err
		}
		votes[billID] = vote
		return nil
	})
	return votes, err
}

// inCongresses keeps the positions from the given congresses; an empty list keeps them all.
func inCongresses(positions []scoring.Candidate, congresses []int) []scoring.Candidate {
	if len(congresses) == 0 {
		return positions
	}
	return slices.DeleteFunc(slices.Clone(positions), func(p scoring.Candidate) bool {
		return !slices.Contains(congresses, p.Congress)
	})
}

// GetScorecard scores the user's votes against each member holding one of their seats in the
// current congress (not one whose term has ended, as find-my-reps), counting that member's
// positions from the given congresses (all when empty), in either chamber. Members with no bill
// in common with the user are left out.
func (s *ScorecardService) GetScorecard(
	ctx context.Context,
	userID string,
	congresses []int,
) ([]model.RepScore, error) {
	ro := s.client.ReadOnlyTransaction()
	defer ro.Close()

	scores := []model.RepScore{}
	reps, err := userReps(ctx, ro, userID)
	if err != nil || len(reps) == 0 {
		return scores, err
	}
	votes, err := userVotes(ctx, ro, userID)
	if err != nil || len(votes) == 0 {
		return scores, err
	}
	ids := make([]string, len(reps))
	for i, rep := range reps {
		ids[i] = rep.id
	}
	positions, err := memberPositions(ctx, ro, ids, 0)
	if err != nil {
		return nil, err
	}

	for _, rep := range reps {
		res := scoring.Score(votes, inCongresses(positions[rep.id], congresses))
		if len(res.Rows) == 0 {
			continue
		}
		scores = append(scores, model.RepScore{
			MemberID: rep.id, MemberName: rep.name, Chamber: rep.chamber, Party: rep.party,
			MatchingVotes: res.Matching, TotalCompared: res.Compared, MemberAbsent: res.MemberAbsent,
			AlignmentPct: res.AlignmentPct, Rule: scoring.RuleName,
		})
	}
	return scores, nil
}

// CompareWithMember returns the user's vote and the member's position on each bill they have
// in common, from the given congresses (all when empty), newest position first. It uses the
// same positions GetScorecard does, so the two agree.
func (s *ScorecardService) CompareWithMember(
	ctx context.Context,
	userID, memberID string,
	congresses []int,
) ([]model.VoteComparison, error) {
	ro := s.client.ReadOnlyTransaction()
	defer ro.Close()

	comparisons := []model.VoteComparison{}
	votes, err := userVotes(ctx, ro, userID)
	if err != nil || len(votes) == 0 {
		return comparisons, err
	}
	positions, err := memberPositions(ctx, ro, []string{memberID}, 0)
	if err != nil {
		return nil, err
	}

	for _, row := range scoring.Score(votes, inCongresses(positions[memberID], congresses)).Rows {
		p := row.Position
		comparisons = append(comparisons, model.VoteComparison{
			BillID: p.BillID, BillTitle: p.BillTitle, VoteID: p.VoteID, Chamber: p.Chamber,
			Congress: p.Congress, VoteDate: p.VoteDate, Question: optionalString(p.Question),
			UserVote: row.UserVote, MemberVote: p.Vote, Counted: row.Counted, Matches: row.Matches,
		})
	}
	return comparisons, nil
}
