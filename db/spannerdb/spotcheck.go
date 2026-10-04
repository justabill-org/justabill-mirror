package spannerdb

import (
	"context"
	"fmt"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
)

const paramVoteID = "voteID"

// SpotcheckReader implements repository.SpotcheckReader on one read-only transaction, so all
// its reads see a single snapshot and it can't write.
type SpotcheckReader struct {
	txn *spanner.ReadOnlyTransaction
}

var _ repository.SpotcheckReader = (*SpotcheckReader)(nil)

// NewSpotcheckReader opens a read-only transaction for pipeline-spotcheck. Close it when done.
func NewSpotcheckReader(c *Client) *SpotcheckReader {
	return &SpotcheckReader{txn: c.Spanner.ReadOnlyTransaction()}
}

// Close ends the read-only transaction.
func (r *SpotcheckReader) Close() { r.txn.Close() }

type storedRollCallRow struct {
	VoteID    string             `spanner:"vote_id"`
	BillID    spanner.NullString `spanner:"bill_id"`
	VoteDate  spanner.NullTime   `spanner:"vote_date"`
	Question  spanner.NullString `spanner:"question"`
	Result    spanner.NullString `spanner:"result"`
	Yeas      spanner.NullInt64  `spanner:"yeas"`
	Nays      spanner.NullInt64  `spanner:"nays"`
	Present   spanner.NullInt64  `spanner:"present"`
	NotVoting spanner.NullInt64  `spanner:"not_voting"`
}

type storedPositionRow struct {
	MemberID string             `spanner:"member_id"`
	LISID    spanner.NullString `spanner:"lis_id"`
	Vote     string             `spanner:"vote"`
}

// StoredRollCall returns the roll call and its positions, or nil when it isn't stored.
func (r *SpotcheckReader) StoredRollCall(ctx context.Context, voteID string) (*repository.StoredRollCall, error) {
	var vote *repository.StoredRollCall
	err := eachRow(ctx, r.txn, spanner.Statement{
		SQL: `SELECT vote_id, bill_id, vote_date, question, result, yeas, nays, present, not_voting
			FROM congressional_votes WHERE vote_id = @voteID`,
		Params: map[string]any{paramVoteID: voteID},
	}, func(row *spanner.Row) error {
		var cv storedRollCallRow
		if err := row.ToStruct(&cv); err != nil {
			return err
		}
		vote = &repository.StoredRollCall{
			VoteID:    cv.VoteID,
			BillID:    nullStringPtr(cv.BillID),
			VoteDate:  cv.VoteDate.Time,
			Question:  nullStringPtr(cv.Question),
			Result:    nullStringPtr(cv.Result),
			Yeas:      nullInt64Ptr(cv.Yeas),
			Nays:      nullInt64Ptr(cv.Nays),
			Present:   nullInt64Ptr(cv.Present),
			NotVoting: nullInt64Ptr(cv.NotVoting),
		}
		return nil
	})
	if err != nil || vote == nil {
		return nil, err
	}

	err = eachRow(ctx, r.txn, spanner.Statement{
		SQL: `SELECT mv.member_id, m.lis_id, mv.vote FROM member_votes mv
			LEFT JOIN members m ON m.bioguide_id = mv.member_id
			WHERE mv.vote_id = @voteID ORDER BY mv.member_id`,
		Params: map[string]any{paramVoteID: voteID},
	}, func(row *spanner.Row) error {
		var p storedPositionRow
		if scanErr := row.ToStruct(&p); scanErr != nil {
			return scanErr
		}
		vote.Positions = append(vote.Positions, repository.StoredPosition{
			MemberID: p.MemberID, LISID: p.LISID.StringVal, Vote: p.Vote,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("member votes of %s: %w", voteID, err)
	}
	return vote, nil
}

// Coverage counts what's loaded for a congress. Each count is one query on the snapshot.
func (r *SpotcheckReader) Coverage(ctx context.Context, congress int) (*repository.StoredCoverage, error) {
	params := map[string]any{paramCongress: int64(congress)}
	cov := &repository.StoredCoverage{BillsByType: map[string]int{}, MembersByChamber: map[string]int{}}

	steps := []struct {
		name string
		run  func() error
	}{
		{"bills by type", func() error {
			return r.countBy(ctx, `SELECT bill_type, COUNT(*) FROM bills
				WHERE congress = @congress GROUP BY bill_type`, params, cov.BillsByType)
		}},
		{"roll calls", func() error {
			counts, err := r.rollCallCounts(ctx, params)
			cov.RollCalls = counts
			return err
		}},
		{"members", func() error {
			return r.countBy(ctx, `SELECT chamber, COUNT(DISTINCT member_id) FROM member_terms
				WHERE congress = @congress GROUP BY chamber`, params, cov.MembersByChamber)
		}},
		{"senators without an LIS ID", func() error {
			ids, err := readStrings(ctx, r.txn, spanner.Statement{
				SQL: `SELECT DISTINCT m.bioguide_id FROM members m
					JOIN member_terms t ON t.member_id = m.bioguide_id
					WHERE t.congress = @congress AND t.chamber = 'Senate' AND m.lis_id IS NULL
					ORDER BY m.bioguide_id`,
				Params: params,
			})
			cov.SenatorsWithoutLISID = ids
			return err
		}},
		{"voted bills", func() error { return r.votedBills(ctx, params, cov) }},
		{"text versions", func() error { return r.textVersions(ctx, params, cov) }},
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			return nil, fmt.Errorf("coverage: %s: %w", s.name, err)
		}
	}
	return cov, nil
}

// countBy runs a query returning (key STRING, count INT64) rows into counts.
func (r *SpotcheckReader) countBy(ctx context.Context, sql string, params map[string]any, counts map[string]int) error {
	return eachRow(ctx, r.txn, spanner.Statement{SQL: sql, Params: params}, func(row *spanner.Row) error {
		var (
			key   string
			count int64
		)
		if err := row.Columns(&key, &count); err != nil {
			return err
		}
		counts[key] = int(count)
		return nil
	})
}

func (r *SpotcheckReader) rollCallCounts(
	ctx context.Context, params map[string]any,
) ([]repository.StoredRollCallCount, error) {
	var out []repository.StoredRollCallCount
	err := eachRow(ctx, r.txn, spanner.Statement{
		SQL: `SELECT chamber, session, COUNT(DISTINCT roll_number), MAX(roll_number)
			FROM congressional_votes
			WHERE congress = @congress AND roll_number IS NOT NULL AND session IS NOT NULL
			GROUP BY chamber, session ORDER BY chamber, session`,
		Params: params,
	}, func(row *spanner.Row) error {
		var (
			chamber                   string
			session, distinct, maxNum int64
		)
		if err := row.Columns(&chamber, &session, &distinct, &maxNum); err != nil {
			return err
		}
		out = append(out, repository.StoredRollCallCount{
			Chamber: chamber, Session: int(session), Distinct: int(distinct), Highest: int(maxNum),
		})
		return nil
	})
	return out, err
}

// votedBills fills the voted-bill counts: every bill a roll call of the congress is linked to,
// voice votes included, since those are real votes on the bill too.
func (r *SpotcheckReader) votedBills(ctx context.Context, params map[string]any, cov *repository.StoredCoverage) error {
	const voted = `WITH voted AS (SELECT DISTINCT bill_id FROM congressional_votes
		WHERE congress = @congress AND bill_id IS NOT NULL) `
	var total int64
	err := eachRow(ctx, r.txn, spanner.Statement{SQL: voted + `SELECT COUNT(*) FROM voted`, Params: params},
		func(row *spanner.Row) error { return row.Columns(&total) })
	if err != nil {
		return err
	}
	cov.VotedBills = int(total)

	cov.MissingVotedBills, err = readStrings(ctx, r.txn, spanner.Statement{
		SQL: voted + `SELECT v.bill_id FROM voted v LEFT JOIN bills b ON b.bill_id = v.bill_id
			WHERE b.bill_id IS NULL ORDER BY v.bill_id`,
		Params: params,
	})
	if err != nil {
		return err
	}

	cov.VotedBillsWithoutSummary, err = readStrings(ctx, r.txn, spanner.Statement{
		SQL: voted + `SELECT v.bill_id FROM voted v
			JOIN bills b ON b.bill_id = v.bill_id
			LEFT JOIN bill_summaries s ON s.bill_id = v.bill_id
			WHERE s.short_summary IS NULL OR TRIM(s.short_summary) = ''
			ORDER BY v.bill_id`,
		Params: params,
	})
	return err
}

func (r *SpotcheckReader) textVersions(
	ctx context.Context,
	params map[string]any,
	cov *repository.StoredCoverage,
) error {
	return eachRow(ctx, r.txn, spanner.Statement{
		SQL: `SELECT COUNT(*), COUNTIF(t.version_id IS NULL)
			FROM bills b
			JOIN bill_text_versions v ON v.bill_id = b.bill_id
			LEFT JOIN bill_texts t ON t.version_id = v.version_id
			WHERE b.congress = @congress`,
		Params: params,
	}, func(row *spanner.Row) error {
		var total, missing int64
		if err := row.Columns(&total, &missing); err != nil {
			return err
		}
		cov.TextVersions, cov.VersionsWithoutText = int(total), int(missing)
		return nil
	})
}
