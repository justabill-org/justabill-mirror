package spannerdb

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// CongressRepository implements repository.CongressRepo with Spanner.
type CongressRepository struct {
	client *spanner.Client
}

// List returns all congresses ordered by number descending, each marked with whether any of its
// roll calls (votes with a roll number, the ones that carry member positions) is loaded.
func (r *CongressRepository) List(ctx context.Context) ([]model.Congress, error) {
	stmt := spanner.NewStatement(
		`SELECT c.number, c.start_date, c.end_date, c.is_current,
		        EXISTS(SELECT 1 FROM congressional_votes v
		               WHERE v.congress = c.number AND v.roll_number IS NOT NULL) AS has_votes
		 FROM congresses c ORDER BY c.number DESC`)

	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var congresses []model.Congress
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}

		var (
			number    int64
			startDate spanner.NullDate
			endDate   spanner.NullDate
			isCurrent bool
			hasVotes  bool
		)
		if err = row.Columns(&number, &startDate, &endDate, &isCurrent, &hasVotes); err != nil {
			return nil, err
		}

		c := model.Congress{
			Number:    int(number),
			IsCurrent: isCurrent,
			HasVotes:  hasVotes,
		}
		if startDate.Valid {
			sd := nullDatePtr(startDate)
			c.StartDate = *sd
		}
		c.EndDate = nullDatePtr(endDate)
		congresses = append(congresses, c)
	}
	if congresses == nil {
		congresses = []model.Congress{}
	}
	return congresses, nil
}

// --- Pipeline writes (#116) ---

// congressDates is the part of a congresses row UpsertCongress writes.
type congressDates struct {
	Number    int64      `spanner:"number"`
	StartDate civil.Date `spanner:"start_date"`
	EndDate   civil.Date `spanner:"end_date"`
}

// UpsertCongress writes the congress's dates. Leaving is_current out of the mutation means a
// new row gets the column's default (false) and an existing row keeps its value.
func (s *PipelineStoreImpl) UpsertCongress(ctx context.Context, c repository.CongressRow) error {
	m, err := spanner.InsertOrUpdateStruct("congresses", congressDates{
		Number:    int64(c.Number),
		StartDate: timeToCivilDate(c.StartDate),
		EndDate:   timeToCivilDate(c.EndDate),
	})
	if err == nil {
		_, err = s.client.Apply(ctx, []*spanner.Mutation{m})
	}
	if err != nil {
		return fmt.Errorf("upsert congress %d: %w", c.Number, err)
	}
	return nil
}

// SetCurrentCongress moves is_current to the congress in one read-write transaction, so readers
// never see two current congresses or none. It fails, changing nothing, when the congress has
// no row.
func (s *PipelineStoreImpl) SetCurrentCongress(ctx context.Context, congress int) (bool, error) {
	params := map[string]any{paramCongress: int64(congress)}
	var changed int64
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		n, err := countRows(txn.Query(ctx, spanner.Statement{
			SQL: "SELECT COUNT(*) FROM congresses WHERE number = @congress", Params: params,
		}))
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("congress %d has no row", congress)
		}
		changed, err = txn.Update(ctx, spanner.Statement{
			SQL: `UPDATE congresses SET is_current = (number = @congress)
			      WHERE is_current != (number = @congress)`,
			Params: params,
		})
		return err
	})
	if err != nil {
		return false, fmt.Errorf("set current congress %d: %w", congress, err)
	}
	return changed > 0, nil
}

// CountMemberTerms counts the congress's member_terms rows.
func (s *PipelineStoreImpl) CountMemberTerms(ctx context.Context, congress int) (int, error) {
	n, err := countRows(s.client.Single().Query(ctx, spanner.Statement{
		SQL:    "SELECT COUNT(*) FROM member_terms WHERE congress = @congress",
		Params: map[string]any{paramCongress: int64(congress)},
	}))
	if err != nil {
		return 0, fmt.Errorf("count member terms of congress %d: %w", congress, err)
	}
	return int(n), nil
}

// countRows reads the single INT64 a SELECT COUNT(*) returns.
func countRows(iter *spanner.RowIterator) (int64, error) {
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return 0, err
	}
	var n int64
	if err = row.Columns(&n); err != nil {
		return 0, err
	}
	return n, nil
}
