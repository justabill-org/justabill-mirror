package spannerdb

import (
	"context"
	"fmt"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
)

// Each bill's passage rank (bills.passage_rank, #1090): how it passed, the order the vote deck
// deals bills in. The vote sync recomputes a congress's ranks from its stored votes on every run.

const (
	// colPassageRank is the bills column holding a bill's [model.PassageRank], NULL for none.
	colPassageRank = "passage_rank"
	// passageRankBatch is how many bills one commit rewrites: two columns each, far below
	// Spanner's 80,000 mutations per commit.
	passageRankBatch = 5000
)

// rankVotesSQL reads the congress's votes on bills oldest first, as cardVotesSQL reads a
// page's, so a rank and its card come from the same votes.
const rankVotesSQL = `SELECT bill_id, vote_id, chamber, roll_number, vote_date, question, result,
	yeas, nays, present, not_voting
FROM congressional_votes
WHERE congress = @congress AND bill_id IS NOT NULL
ORDER BY bill_id, vote_date, vote_id`

// rankBillsSQL reads the congress's bills with their stored ranks.
const rankBillsSQL = `SELECT bill_id, passage_rank FROM bills WHERE congress = @congress`

type passageRankRow struct {
	BillID string            `spanner:"bill_id"`
	Rank   spanner.NullInt64 `spanner:"passage_rank"`
}

// RefreshPassageRanks sets each of the congress's bills' passage_rank to the [model.PassageRank]
// of its passage entries, the ones the /vote card shows (each chamber's latest final vote), or
// NULL when it has none. It reads the congress's votes and bills in one read-only transaction,
// then rewrites only the bills whose rank changed, passage_rank alone (updated_at stays), and
// returns how many. A vote naming a bill that isn't stored is skipped; the next run ranks it.
func (s *PipelineStoreImpl) RefreshPassageRanks(ctx context.Context, congress int) (int, error) {
	params := map[string]any{paramCongress: int64(congress)}
	txn := s.client.ReadOnlyTransaction()
	defer txn.Close()
	votes, err := queryRows(ctx, txn, spanner.Statement{SQL: rankVotesSQL, Params: params},
		"passage rank votes", func(row cardVoteRow) cardVoteRow { return row })
	if err != nil {
		return 0, fmt.Errorf("refresh passage ranks of congress %d: %w", congress, err)
	}
	bills, err := queryRows(ctx, txn, spanner.Statement{SQL: rankBillsSQL, Params: params},
		"passage rank bills", func(row passageRankRow) passageRankRow { return row })
	if err != nil {
		return 0, fmt.Errorf("refresh passage ranks of congress %d: %w", congress, err)
	}

	passage := passageEntries(votes)
	var ms []*spanner.Mutation
	for _, b := range bills {
		rank, ok := model.PassageRank(passage[b.BillID])
		want := spanner.NullInt64{Int64: int64(rank), Valid: ok}
		if want == b.Rank {
			continue
		}
		ms = append(ms, spanner.Update(billsTable, []string{colBillID, colPassageRank}, []any{b.BillID, want}))
	}
	for start := 0; start < len(ms); start += passageRankBatch {
		if _, err = s.client.Apply(ctx, ms[start:min(start+passageRankBatch, len(ms))]); err != nil {
			return start, fmt.Errorf("refresh passage ranks of congress %d: %w", congress, err)
		}
	}
	return len(ms), nil
}
