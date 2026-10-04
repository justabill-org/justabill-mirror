package spannerdb

import (
	"context"
	"errors"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
)

// VoteRepository implements repository.VoteRepo with Spanner.
type VoteRepository struct {
	client *spanner.Client
}

// GetCongressionalVotes returns all congressional votes for a bill, newest first: by date, then
// session and roll number for votes on the same day, then vote_id so the order is stable (#453).
func (r *VoteRepository) GetCongressionalVotes(
	ctx context.Context,
	billID string,
) ([]model.CongressionalVote, error) {
	stmt := spanner.Statement{
		SQL: `SELECT vote_id, bill_id, congress, chamber, session, roll_number,
		        vote_date, question, result, yeas, nays, present, not_voting
		 FROM congressional_votes WHERE bill_id = @billID
		 ORDER BY vote_date DESC, session DESC, roll_number DESC, vote_id DESC`,
		Params: map[string]any{paramBillID: billID},
	}

	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var votes []model.CongressionalVote
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}

		var (
			id         string
			bid        spanner.NullString
			congress   int64
			chamber    string
			session    spanner.NullInt64
			rollNumber spanner.NullInt64
			voteDate   spanner.NullTime
			question   spanner.NullString
			result     spanner.NullString
			yeas       spanner.NullInt64
			nays       spanner.NullInt64
			present    spanner.NullInt64
			notVoting  spanner.NullInt64
		)
		if err = row.Columns(
			&id, &bid, &congress, &chamber, &session, &rollNumber,
			&voteDate, &question, &result, &yeas, &nays, &present, &notVoting,
		); err != nil {
			return nil, err
		}

		v := model.CongressionalVote{
			ID:         id,
			BillID:     nullStringPtr(bid),
			Congress:   int(congress),
			Chamber:    chamber,
			Session:    nullInt64Ptr(session),
			RollNumber: nullInt64Ptr(rollNumber),
			Question:   nullStringPtr(question),
			Result:     nullStringPtr(result),
			Yeas:       nullInt64Ptr(yeas),
			Nays:       nullInt64Ptr(nays),
			Present:    nullInt64Ptr(present),
			NotVoting:  nullInt64Ptr(notVoting),
		}
		if voteDate.Valid {
			v.VoteDate = voteDate.Time
		}
		votes = append(votes, v)
	}
	if votes == nil {
		votes = []model.CongressionalVote{}
	}
	return votes, nil
}

// GetMemberVotes returns how members voted on a congressional vote.
func (r *VoteRepository) GetMemberVotes(ctx context.Context, voteID string) ([]model.MemberVote, error) {
	stmt := spanner.Statement{
		SQL: `SELECT vote_id, member_id, vote
		 FROM member_votes WHERE vote_id = @voteID`,
		Params: map[string]any{"voteID": voteID},
	}

	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var votes []model.MemberVote
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}

		var v model.MemberVote
		if err = row.Columns(&v.CongressionalVoteID, &v.MemberID, &v.Vote); err != nil {
			return nil, err
		}
		votes = append(votes, v)
	}
	if votes == nil {
		votes = []model.MemberVote{}
	}
	return votes, nil
}

// MemberPositions returns memberID's position on each bill with a final-action roll call in
// congress (0 for every congress), under the scorecard rule (scoring.RuleName): the member's vote
// on the latest final-action roll call on the bill. Voice votes, roll calls with no bill and
// procedural votes are left out. Newest first.
func (r *VoteRepository) MemberPositions(
	ctx context.Context,
	memberID string,
	congress int,
) ([]model.MemberPosition, error) {
	byMember, err := memberPositions(ctx, r.client.Single(), []string{memberID}, congress)
	if err != nil {
		return nil, err
	}
	picked := byMember[memberID]
	positions := make([]model.MemberPosition, 0, len(picked))
	for _, p := range picked {
		positions = append(positions, model.MemberPosition{
			BillID: p.BillID, Vote: p.Vote, VoteID: p.VoteID, Chamber: p.Chamber, VoteDate: p.VoteDate,
			Question: optionalString(p.Question),
		})
	}
	return positions, nil
}

// optionalString returns nil for "" and a pointer to s otherwise.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
