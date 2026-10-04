package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
)

// summaryBatchChunk is how many bills one commit holds or releases
// (docs/design/198-corpus-resummarization.md): a hold is one attempt row of 11 columns plus its
// attempted_at index entry, so 1,000 bills stay well under Spanner's 80,000 mutations per commit.
const summaryBatchChunk = 1000

const (
	tableSummaryBatches = "summary_batches"

	colBatchID     = "batch_id"
	colContentHash = "content_hash"

	paramBatch   = "batch"
	paramPending = "pending"
)

// summaryBatchRecord is a summary_batches row.
type summaryBatchRecord struct {
	BatchID       string             `spanner:"batch_id"`
	Congress      int64              `spanner:"congress"`
	Model         string             `spanner:"model"`
	PromptVersion string             `spanner:"prompt_version"`
	JobName       spanner.NullString `spanner:"job_name"`
	State         string             `spanner:"state"`
	BillCount     int64              `spanner:"bill_count"`
	InputURI      string             `spanner:"input_uri"`
	OutputURI     spanner.NullString `spanner:"output_uri"`
	OKCount       spanner.NullInt64  `spanner:"ok_count"`
	FailedCount   spanner.NullInt64  `spanner:"failed_count"`
	CreatedAt     time.Time          `spanner:"created_at"`
	FinishedAt    spanner.NullTime   `spanner:"finished_at"`
	ImportedAt    spanner.NullTime   `spanner:"imported_at"`
}

func (r summaryBatchRecord) batch() repository.SummaryBatch {
	return repository.SummaryBatch{
		BatchID: r.BatchID, Congress: int(r.Congress), Model: r.Model, PromptVersion: r.PromptVersion,
		JobName: r.JobName.StringVal, State: r.State, BillCount: int(r.BillCount),
		InputURI: r.InputURI, OutputURI: r.OutputURI.StringVal,
		OKCount: nullInt64Ptr(r.OKCount), FailedCount: nullInt64Ptr(r.FailedCount),
		CreatedAt: r.CreatedAt, FinishedAt: nullTimePtr(r.FinishedAt), ImportedAt: nullTimePtr(r.ImportedAt),
	}
}

// CreateSummaryBatch inserts a batch row; see [repository.PipelineStore].
func (s *PipelineStoreImpl) CreateSummaryBatch(ctx context.Context, b repository.SummaryBatch) error {
	m, err := spanner.InsertStruct(tableSummaryBatches, summaryBatchRecord{
		BatchID: b.BatchID, Congress: int64(b.Congress), Model: b.Model, PromptVersion: b.PromptVersion,
		JobName: emptyToNull(b.JobName), State: b.State, BillCount: int64(b.BillCount),
		InputURI: b.InputURI, OutputURI: emptyToNull(b.OutputURI),
		OKCount: ptrToNullInt64(b.OKCount), FailedCount: ptrToNullInt64(b.FailedCount),
		CreatedAt:  b.CreatedAt,
		FinishedAt: ptrTimeToNullTime(b.FinishedAt), ImportedAt: ptrTimeToNullTime(b.ImportedAt),
	})
	if err != nil {
		return fmt.Errorf("summary batch mutation: %w", err)
	}
	if _, err = s.client.Apply(ctx, []*spanner.Mutation{m}); err != nil {
		return fmt.Errorf("create summary batch %s: %w", b.BatchID, err)
	}
	return nil
}

// UpdateSummaryBatch writes the batch's state and the fields u sets; see
// [repository.PipelineStore].
func (s *PipelineStoreImpl) UpdateSummaryBatch(ctx context.Context, u repository.SummaryBatchUpdate) error {
	cols, vals := []string{colBatchID, "state"}, []any{u.BatchID, u.State}
	set := func(col string, ok bool, v any) {
		if ok {
			cols, vals = append(cols, col), append(vals, v)
		}
	}
	set("job_name", u.JobName != "", u.JobName)
	set("output_uri", u.OutputURI != "", u.OutputURI)
	set("ok_count", u.OKCount != nil, ptrToNullInt64(u.OKCount))
	set("failed_count", u.FailedCount != nil, ptrToNullInt64(u.FailedCount))
	set("finished_at", u.FinishedAt != nil, ptrTimeToNullTime(u.FinishedAt))
	set("imported_at", u.ImportedAt != nil, ptrTimeToNullTime(u.ImportedAt))

	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		if _, err := txn.ReadRow(ctx, tableSummaryBatches, spanner.Key{u.BatchID}, []string{colBatchID}); err != nil {
			if errors.Is(err, spanner.ErrRowNotFound) {
				return fmt.Errorf("summary batch %s: %w", u.BatchID, repository.ErrNotFound)
			}
			return fmt.Errorf("read summary batch %s: %w", u.BatchID, err)
		}
		return txn.BufferWrite([]*spanner.Mutation{spanner.Update(tableSummaryBatches, cols, vals)})
	})
	if err != nil {
		return fmt.Errorf("update summary batch: %w", err)
	}
	return nil
}

// summaryBatchColumns are the columns summaryBatchRecord reads.
const summaryBatchColumns = `batch_id, congress, model, prompt_version, job_name, state, bill_count,
	input_uri, output_uri, ok_count, failed_count, created_at, finished_at, imported_at`

// SummaryBatch reads one batch; see [repository.PipelineStore].
func (s *PipelineStoreImpl) SummaryBatch(ctx context.Context, batchID string) (*repository.SummaryBatch, error) {
	batches, err := s.querySummaryBatches(ctx, spanner.Statement{
		SQL:    `SELECT ` + summaryBatchColumns + ` FROM summary_batches WHERE batch_id = @batch`,
		Params: map[string]any{paramBatch: batchID},
	})
	if err != nil {
		return nil, err
	}
	if len(batches) == 0 {
		return nil, fmt.Errorf("summary batch %s: %w", batchID, repository.ErrNotFound)
	}
	return &batches[0], nil
}

// OpenSummaryBatches lists the batches neither imported nor released; see
// [repository.PipelineStore]. The table holds a handful of rows a year, so it's scanned.
func (s *PipelineStoreImpl) OpenSummaryBatches(ctx context.Context) ([]repository.SummaryBatch, error) {
	return s.querySummaryBatches(ctx, spanner.Statement{
		SQL: `SELECT ` + summaryBatchColumns + ` FROM summary_batches
		 WHERE state NOT IN (@imported, @released)
		 ORDER BY created_at, batch_id`,
		Params: map[string]any{
			"imported": repository.SummaryBatchImported, "released": repository.SummaryBatchReleased,
		},
	})
}

func (s *PipelineStoreImpl) querySummaryBatches(
	ctx context.Context, stmt spanner.Statement,
) ([]repository.SummaryBatch, error) {
	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var batches []repository.SummaryBatch
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return batches, nil
		}
		if err != nil {
			return nil, fmt.Errorf("query summary batches: %w", err)
		}
		var r summaryBatchRecord
		if err = row.ToStruct(&r); err != nil {
			return nil, fmt.Errorf("read summary batch: %w", err)
		}
		batches = append(batches, r.batch())
	}
}

// HoldSummaryBatch writes the batch's batch_pending attempts; see [repository.PipelineStore].
func (s *PipelineStoreImpl) HoldSummaryBatch(ctx context.Context, h repository.SummaryBatchHolds) error {
	return s.holdSummaryBatch(ctx, h, summaryBatchChunk)
}

// holdSummaryBatch writes h's holds in commits of at most chunk bills. A hold stores attempts 0,
// since a batch_pending attempt counts like ok for the failure count.
func (s *PipelineStoreImpl) holdSummaryBatch(ctx context.Context, h repository.SummaryBatchHolds, chunk int) error {
	if h.BatchID == "" {
		return errors.New("hold summary batch: no batch ID")
	}
	for bills := range slices.Chunk(h.Bills, chunk) {
		muts := make([]*spanner.Mutation, 0, len(bills))
		for _, b := range bills {
			m, err := spanner.InsertOrUpdateStruct("summary_attempts", summaryAttemptRecord{
				BillID: b.BillID, ContentHash: b.ContentHash, PromptVersion: h.PromptVersion, Model: h.Model,
				Outcome: repository.SummaryOutcomeBatchPending, AttemptedAt: h.At,
				NextAttemptAt: spanner.NullTime{Time: h.Until, Valid: true},
				RequestType:   emptyToNull(repository.SummaryRequestBatch), BatchID: emptyToNull(h.BatchID),
			})
			if err != nil {
				return fmt.Errorf("summary hold mutation: %w", err)
			}
			muts = append(muts, m)
		}
		if _, err := s.client.Apply(ctx, muts); err != nil {
			return fmt.Errorf("hold summary batch %s: %w", h.BatchID, err)
		}
	}
	return nil
}

// releaseSummaryBatchSQL makes up to @lim of the batch's held bills due at @now. A released row
// keeps its outcome, but next_attempt_at > @now no longer matches it, so each run of the
// statement takes the next rows.
const releaseSummaryBatchSQL = `UPDATE summary_attempts SET next_attempt_at = @now
WHERE bill_id IN (
	SELECT bill_id FROM summary_attempts
	WHERE batch_id = @batch AND outcome = @pending AND next_attempt_at > @now
	LIMIT @lim)`

// ReleaseSummaryBatch makes the batch's held bills due; see [repository.PipelineStore]. It
// releases at most 1,000 bills per commit. Rows the synchronous job has since rewritten have no
// batch ID, so they're left alone.
func (s *PipelineStoreImpl) ReleaseSummaryBatch(ctx context.Context, batchID string, now time.Time) (int, error) {
	stmt := spanner.Statement{SQL: releaseSummaryBatchSQL, Params: map[string]any{
		paramBatch: batchID, paramNow: now, paramPending: repository.SummaryOutcomeBatchPending,
		paramLimit: int64(summaryBatchChunk),
	}}
	released := 0
	for {
		var n int64
		update := func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
			var err error
			n, err = txn.Update(ctx, stmt)
			return err
		}
		if _, err := s.client.ReadWriteTransaction(ctx, update); err != nil {
			return released, fmt.Errorf("release summary batch %s: %w", batchID, err)
		}
		released += int(n)
		if n < summaryBatchChunk {
			return released, nil
		}
	}
}
