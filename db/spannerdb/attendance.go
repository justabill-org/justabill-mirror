package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// Attendance: the member_attendance table the vote sync fills and the member page reads
// (docs/design/agent-1742-member-attendance.md).

const (
	tableMemberAttendance = "member_attendance"
	colMemberID           = "member_id"
)

// attendanceRecord is a member_attendance row as Spanner stores it.
type attendanceRecord struct {
	MemberID         string    `spanner:"member_id"`
	Congress         int64     `spanner:"congress"`
	Chamber          string    `spanner:"chamber"`
	Eligible         int64     `spanner:"eligible"`
	Missed           int64     `spanner:"missed"`
	Present          int64     `spanner:"present"`
	ChamberMembers   int64     `spanner:"chamber_members"`
	ChamberMedianPct float64   `spanner:"chamber_median_pct"`
	ComputedAt       time.Time `spanner:"computed_at"`
}

// GetAttendance returns the member's member_attendance rows, newest congress first and by
// chamber within a congress (a member can serve in both in one congress). A member with no rows,
// or no such member, gets an empty slice.
func (r *MemberRepository) GetAttendance(ctx context.Context, bioguideID string) ([]model.MemberAttendance, error) {
	iter := r.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT member_id, congress, chamber, eligible, missed, present, chamber_members,
		   chamber_median_pct, computed_at
		 FROM member_attendance
		 WHERE member_id = @memberID
		 ORDER BY congress DESC, chamber`,
		Params: map[string]any{paramMemberID: bioguideID},
	})
	defer iter.Stop()

	rows := []model.MemberAttendance{}
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read attendance of %s: %w", bioguideID, err)
		}
		var rec attendanceRecord
		if err = row.ToStruct(&rec); err != nil {
			return nil, fmt.Errorf("decode attendance of %s: %w", bioguideID, err)
		}
		rows = append(rows, model.MemberAttendance{
			MemberID: rec.MemberID, Congress: int(rec.Congress), Chamber: rec.Chamber,
			Eligible: int(rec.Eligible), Missed: int(rec.Missed), Present: int(rec.Present),
			ChamberMembers: int(rec.ChamberMembers), ChamberMedianPct: rec.ChamberMedianPct,
			ComputedAt: rec.ComputedAt,
		})
	}
}

// ListRollCallRefs returns the congress's roll calls, those with a roll number (a voice vote or
// unanimous consent has none), ordered by vote ID. Question is empty when none was stored.
func (s *PipelineStoreImpl) ListRollCallRefs(ctx context.Context, congress int) ([]repository.RollCallRef, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT vote_id, chamber, IFNULL(question, '') AS question
		 FROM congressional_votes
		 WHERE congress = @congress AND roll_number IS NOT NULL
		 ORDER BY vote_id`,
		Params: map[string]any{paramCongress: int64(congress)},
	})
	defer iter.Stop()

	var refs []repository.RollCallRef
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return refs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list roll calls of congress %d: %w", congress, err)
		}
		var ref repository.RollCallRef
		if err = row.Columns(&ref.VoteID, &ref.Chamber, &ref.Question); err != nil {
			return nil, fmt.Errorf("decode roll call of congress %d: %w", congress, err)
		}
		refs = append(refs, ref)
	}
}

// CountMemberVotes groups the congress's member_votes rows by member, the roll call's chamber and
// the stored vote in Spanner, so a full congress (about 600,000 rows) comes back as a few thousand
// counts. member_votes is interleaved in congressional_votes, so the join reads each roll call's
// rows next to it. Roll calls in excludedVoteIDs and those without a roll number are left out.
func (s *PipelineStoreImpl) CountMemberVotes(
	ctx context.Context, congress int, excludedVoteIDs []string,
) ([]repository.MemberVoteCount, error) {
	if excludedVoteIDs == nil {
		// A nil slice binds as a NULL array. GoogleSQL's NOT IN UNNEST(NULL) is true, but an empty
		// array doesn't leave the filter resting on that rule.
		excludedVoteIDs = []string{}
	}
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT mv.member_id, cv.chamber, mv.vote, COUNT(*) AS n
		 FROM congressional_votes cv
		 JOIN member_votes mv ON mv.vote_id = cv.vote_id
		 WHERE cv.congress = @congress AND cv.roll_number IS NOT NULL
		   AND cv.vote_id NOT IN UNNEST(@excluded)
		 GROUP BY mv.member_id, cv.chamber, mv.vote
		 ORDER BY mv.member_id, cv.chamber, mv.vote`,
		Params: map[string]any{paramCongress: int64(congress), "excluded": excludedVoteIDs},
	})
	defer iter.Stop()

	var counts []repository.MemberVoteCount
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return counts, nil
		}
		if err != nil {
			return nil, fmt.Errorf("count member votes of congress %d: %w", congress, err)
		}
		var c repository.MemberVoteCount
		if err = row.Columns(&c.MemberID, &c.Chamber, &c.Vote, &c.Count); err != nil {
			return nil, fmt.Errorf("decode member vote count of congress %d: %w", congress, err)
		}
		counts = append(counts, c)
	}
}

// ReplaceMemberAttendance deletes the congress's member_attendance rows and inserts rows in one
// read-write transaction, so a reader sees the old rows or the new ones, never a mix. The key
// starts with member_id, so the delete is DML on the congress rather than a key range. It fails,
// writing nothing, when a row is of another congress.
func (s *PipelineStoreImpl) ReplaceMemberAttendance(
	ctx context.Context, congress int, rows []repository.MemberAttendanceRow,
) error {
	ms := make([]*spanner.Mutation, 0, len(rows))
	for _, r := range rows {
		if r.Congress != congress {
			return fmt.Errorf("replace attendance of congress %d: row for %s is of congress %d",
				congress, r.MemberID, r.Congress)
		}
		ms = append(ms, spanner.Insert(tableMemberAttendance,
			[]string{colMemberID, colCongress, colChamber, "eligible", "missed", "present",
				"chamber_members", "chamber_median_pct", "computed_at"},
			[]any{r.MemberID, int64(r.Congress), r.Chamber, r.Eligible, r.Missed, r.Present,
				r.ChamberMembers, r.ChamberMedianPct, spanner.CommitTimestamp}))
	}
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		if _, err := txn.Update(ctx, spanner.Statement{
			SQL:    `DELETE FROM member_attendance WHERE congress = @congress`,
			Params: map[string]any{paramCongress: int64(congress)},
		}); err != nil {
			return err
		}
		return txn.BufferWrite(ms)
	})
	if err != nil {
		return fmt.Errorf("replace attendance of congress %d: %w", congress, err)
	}
	return nil
}
