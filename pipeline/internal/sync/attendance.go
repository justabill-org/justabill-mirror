package sync

import (
	"context"
	"fmt"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/scoring"
	"github.com/justabill-org/justabill/pipeline/internal/attendance"
)

// computeAttendance recomputes a congress's member_attendance rows from every roll call stored
// for it, under scoring.AttendanceRule (docs/design/agent-1742-member-attendance.md, "The job
// step"), and returns how many rows it wrote. It reads the roll calls, has Spanner count each
// member's votes with the quorum calls left out, and replaces the congress's rows in one
// transaction, so the figures always match member_votes, whether or not this run stored new roll
// calls. A congress with roll calls that gets no row is logged as a warning.
func (s *Service) computeAttendance(ctx context.Context, congressNum int) (int, error) {
	rollCalls, rows, err := s.replaceAttendance(ctx, congressNum)
	if rows == 0 && rollCalls > 0 {
		s.logger.WarnContext(ctx, "no attendance rows for a congress with roll calls", "congress", congressNum,
			"roll_calls", rollCalls)
	}
	if err != nil {
		return 0, fmt.Errorf("compute attendance: %w", err)
	}
	return rows, nil
}

// replaceAttendance does computeAttendance's reads and write. It returns how many roll calls the
// congress has (0 if they couldn't be listed) and how many rows it wrote.
func (s *Service) replaceAttendance(ctx context.Context, congressNum int) (int, int, error) {
	rolls, err := s.store.ListRollCallRefs(ctx, congressNum)
	if err != nil {
		return 0, 0, err
	}
	counts, err := s.store.CountMemberVotes(ctx, congressNum, attendance.QuorumCallIDs(rolls))
	if err != nil {
		return len(rolls), 0, err
	}
	ids, err := s.store.ListMemberIDs(ctx)
	if err != nil {
		return len(rolls), 0, fmt.Errorf("list members: %w", err)
	}
	counts, unknown := storedMembersOnly(counts, ids)
	rows := attendance.Compute(congressNum, rolls, counts)
	if err = s.store.ReplaceMemberAttendance(ctx, congressNum, rows); err != nil {
		return len(rolls), 0, err
	}
	s.logger.InfoContext(ctx, "attendance computed", "congress", congressNum, "rule", scoring.AttendanceRule,
		"members", len(rows), "house_median_pct", chamberMedian(rows, chamberHouse),
		"senate_median_pct", chamberMedian(rows, chamberSenate), "unknown_members", unknown)
	return len(rolls), len(rows), nil
}

// storedMembersOnly drops the counts of members with no members row, and returns how many
// distinct members it dropped. member_attendance's foreign key needs the row, but the House's
// roll calls are stored for every bioguide ID the Clerk lists, so a member sworn in since the last
// member sync has votes before a row; they get attendance once the member sync stores them.
func storedMembersOnly(
	counts []repository.MemberVoteCount, memberIDs []string,
) ([]repository.MemberVoteCount, int) {
	stored := make(map[string]bool, len(memberIDs))
	for _, id := range memberIDs {
		stored[id] = true
	}
	kept := counts[:0:0]
	unknown := make(map[string]bool)
	for _, c := range counts {
		if stored[c.MemberID] {
			kept = append(kept, c)
		} else {
			unknown[c.MemberID] = true
		}
	}
	return kept, len(unknown)
}

// chamberMedian returns the chamber's median missed share from its rows, or 0 when it has none.
func chamberMedian(rows []repository.MemberAttendanceRow, chamber string) float64 {
	for _, r := range rows {
		if r.Chamber == chamber {
			return r.ChamberMedianPct
		}
	}
	return 0
}
