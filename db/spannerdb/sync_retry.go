package spannerdb

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
)

// --- Sync Retry (docs/design/67-upstream-quota-retries.md) ---

const paramStep = "step"

// syncRetryRecord is a sync_retry row as Spanner stores it.
type syncRetryRecord struct {
	Step          string             `spanner:"step"`
	Congress      int64              `spanner:"congress"`
	ItemID        string             `spanner:"item_id"`
	Attempts      int64              `spanner:"attempts"`
	FirstFailedAt time.Time          `spanner:"first_failed_at"`
	LastFailedAt  time.Time          `spanner:"last_failed_at"`
	NextAttemptAt spanner.NullTime   `spanner:"next_attempt_at"`
	LastError     spanner.NullString `spanner:"last_error"`
}

// syncRetryPrev is what RecordItemFailure reads back before counting a new failure.
type syncRetryPrev struct {
	Attempts      int64     `spanner:"attempts"`
	FirstFailedAt time.Time `spanner:"first_failed_at"`
}

func (r syncRetryRecord) toRow() repository.RetryItem {
	return repository.RetryItem{
		Step:          r.Step,
		Congress:      int(r.Congress),
		ItemID:        r.ItemID,
		Attempts:      int(r.Attempts),
		FirstFailedAt: r.FirstFailedAt,
		LastFailedAt:  r.LastFailedAt,
		NextAttemptAt: nullTimeValue(r.NextAttemptAt),
		LastError:     r.LastError.StringVal,
	}
}

// DueRetries implements [repository.PipelineStore].
func (s *PipelineStoreImpl) DueRetries(
	ctx context.Context, step string, congress int, now time.Time, limit int,
) ([]repository.RetryItem, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT step, congress, item_id, attempts, first_failed_at, last_failed_at,
				next_attempt_at, last_error
			FROM sync_retry
			WHERE step = @step AND congress = @congress AND next_attempt_at <= @now
			ORDER BY next_attempt_at, item_id
			LIMIT @limit`,
		Params: map[string]any{paramStep: step, paramCongress: int64(congress), paramNow: now, "limit": int64(limit)},
	})
	defer iter.Stop()

	var items []repository.RetryItem
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return items, nil
		}
		if err != nil {
			return nil, err
		}
		var r syncRetryRecord
		if err = row.ToStruct(&r); err != nil {
			return nil, err
		}
		items = append(items, r.toRow())
	}
}

// RecordItemFailure reads the item's attempts and first failure, then writes the row back
// with one more attempt, in one read-write transaction. A missing row counts from zero.
func (s *PipelineStoreImpl) RecordItemFailure(ctx context.Context, f repository.ItemFailure) error {
	failedAt := f.FailedAt
	if failedAt.IsZero() {
		failedAt = time.Now()
	}
	key := spanner.Key{f.Step, int64(f.Congress), f.ItemID}
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		prev := syncRetryPrev{FirstFailedAt: failedAt}
		row, readErr := txn.ReadRow(ctx, "sync_retry", key, []string{"attempts", "first_failed_at"})
		switch {
		case errors.Is(readErr, spanner.ErrRowNotFound):
		case readErr != nil:
			return readErr
		default:
			if readErr = row.ToStruct(&prev); readErr != nil {
				return readErr
			}
		}

		attempts := int(prev.Attempts) + 1
		var next spanner.NullTime
		if at, ok := repository.NextRetryAt(attempts, failedAt, f.Permanent); ok {
			next = spanner.NullTime{Time: at, Valid: true}
		}
		m, mErr := spanner.InsertOrUpdateStruct("sync_retry", syncRetryRecord{
			Step:          f.Step,
			Congress:      int64(f.Congress),
			ItemID:        f.ItemID,
			Attempts:      int64(attempts),
			FirstFailedAt: prev.FirstFailedAt,
			LastFailedAt:  failedAt,
			NextAttemptAt: next,
			LastError: spanner.NullString{
				StringVal: truncateUTF8(f.Error, repository.MaxRetryErrorBytes),
				Valid:     true,
			},
		})
		if mErr != nil {
			return mErr
		}
		return txn.BufferWrite([]*spanner.Mutation{m})
	})
	return err
}

// ClearItemRetries implements [repository.PipelineStore].
func (s *PipelineStoreImpl) ClearItemRetries(ctx context.Context, step string, congress int, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	keys := make([]spanner.KeySet, 0, len(itemIDs))
	for _, id := range itemIDs {
		keys = append(keys, spanner.Key{step, int64(congress), id})
	}
	_, err := s.client.Apply(ctx, []*spanner.Mutation{spanner.Delete("sync_retry", spanner.KeySets(keys...))})
	return err
}

// RetryStats implements [repository.PipelineStore].
func (s *PipelineStoreImpl) RetryStats(
	ctx context.Context, step string, congress int, now time.Time,
) (repository.RetryStats, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT COUNTIF(next_attempt_at <= @now) AS due,
				COUNTIF(next_attempt_at > @now) AS waiting,
				COUNTIF(next_attempt_at IS NULL) AS given_up
			FROM sync_retry
			WHERE step = @step AND congress = @congress`,
		Params: map[string]any{paramStep: step, paramCongress: int64(congress), paramNow: now},
	})
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		return repository.RetryStats{}, err
	}
	var due, waiting, givenUp int64
	if err = row.Columns(&due, &waiting, &givenUp); err != nil {
		return repository.RetryStats{}, err
	}
	return repository.RetryStats{Due: int(due), Waiting: int(waiting), GivenUp: int(givenUp)}, nil
}

// truncateUTF8 returns at most maxBytes of s, cut at a rune boundary.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
