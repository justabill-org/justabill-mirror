package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

const (
	tableVoteAggregates = "vote_aggregates"
	tableRepAlignment   = "rep_alignment"
	paramBillID         = "billID"
	paramMemberID       = "memberID"
	paramBillIDs        = "billIDs"
	paramStatuses       = "statuses"

	// aggregatePublicColumns are the vote_aggregates columns the API may serve.
	aggregatePublicColumns = "bill_id, scope, scope_key, status, yea_pct, nay_pct, voters_floor, published_at, computed_at"
	// aggregateBasisColumns are the aggregation job's bookkeeping, read only by the pipeline.
	aggregateBasisColumns = "basis_yea, basis_nay, hold_reason"
	repAlignmentColumns   = "member_id, congress, scope_key, bills_compared, bills_agreed, computed_at"

	// aggregateScopeOrder sorts national cells first, then states, then districts.
	aggregateScopeOrder = "CASE scope WHEN 'national' THEN 0 WHEN 'state' THEN 1 ELSE 2 END, scope_key"
)

// servedAggregateStatuses are the statuses AggregateReader returns. Suppressed cells are left out.
func servedAggregateStatuses() []string {
	return []string{model.AggregateStatusPublished, model.AggregateStatusHeld}
}

// AggregateRepository implements repository.AggregateReader with Spanner.
type AggregateRepository struct {
	client *spanner.Client
}

// BillAggregates returns the bill's published and held cells, national first.
func (r *AggregateRepository) BillAggregates(ctx context.Context, billID string) ([]model.VoteAggregate, error) {
	cells, err := queryAggregates(ctx, r.client.Single(), spanner.Statement{
		SQL: "SELECT " + aggregatePublicColumns + " FROM vote_aggregates" +
			" WHERE bill_id = @billID AND status IN UNNEST(@statuses) ORDER BY " + aggregateScopeOrder,
		Params: map[string]any{paramBillID: billID, paramStatuses: servedAggregateStatuses()},
	}, false)
	if err != nil {
		return nil, fmt.Errorf("bill aggregates: %w", err)
	}
	return cells, nil
}

// BillAggregate returns the bill's published or held cell for scopeKey, or nil.
func (r *AggregateRepository) BillAggregate(
	ctx context.Context,
	billID, scopeKey string,
) (*model.VoteAggregate, error) {
	cells, err := queryAggregates(ctx, r.client.Single(), spanner.Statement{
		SQL: "SELECT " + aggregatePublicColumns + " FROM vote_aggregates" +
			" WHERE bill_id = @billID AND scope_key = @scopeKey AND status IN UNNEST(@statuses)" +
			" ORDER BY " + aggregateScopeOrder + " LIMIT 1",
		Params: map[string]any{paramBillID: billID, "scopeKey": scopeKey, paramStatuses: servedAggregateStatuses()},
	}, false)
	if err != nil {
		return nil, fmt.Errorf("bill aggregate: %w", err)
	}
	if len(cells) == 0 {
		return nil, nil //nolint:nilnil // not found returns nil
	}
	return &cells[0], nil
}

// MemberAlignment returns the member's rep_alignment rows, newest congress first.
func (r *AggregateRepository) MemberAlignment(ctx context.Context, memberID string) ([]model.RepAlignment, error) {
	iter := r.client.Single().Query(ctx, spanner.Statement{
		SQL: "SELECT " + repAlignmentColumns +
			" FROM rep_alignment WHERE member_id = @memberID ORDER BY congress DESC, scope_key",
		Params: map[string]any{paramMemberID: memberID},
	})
	defer iter.Stop()

	var rows []model.RepAlignment
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("member alignment: %w", err)
		}
		var (
			a                                    model.RepAlignment
			congress, billsCompared, billsAgreed int64
		)
		if err = row.Columns(&a.MemberID, &congress, &a.ScopeKey, &billsCompared, &billsAgreed,
			&a.ComputedAt); err != nil {
			return nil, fmt.Errorf("member alignment: %w", err)
		}
		a.Congress, a.BillsCompared, a.BillsAgreed = int(congress), int(billsCompared), int(billsAgreed)
		rows = append(rows, a)
	}
}

// ListVoteAggregates returns every cell of the given bills, whatever its status, with the job's
// bookkeeping columns. Cells are ordered by bill, then national first.
func (s *PipelineStoreImpl) ListVoteAggregates(ctx context.Context, billIDs []string) ([]model.VoteAggregate, error) {
	if len(billIDs) == 0 {
		return nil, nil
	}
	cells, err := queryAggregates(ctx, s.client.Single(), listAggregatesStatement(billIDs), true)
	if err != nil {
		return nil, fmt.Errorf("list vote aggregates: %w", err)
	}
	return cells, nil
}

// listAggregatesStatement selects every cell of the bills, with the bookkeeping columns, ordered
// by bill and then national first.
func listAggregatesStatement(billIDs []string) spanner.Statement {
	return spanner.Statement{
		SQL: "SELECT " + aggregatePublicColumns + ", " + aggregateBasisColumns + " FROM vote_aggregates" +
			" WHERE bill_id IN UNNEST(@billIDs) ORDER BY bill_id, " + aggregateScopeOrder,
		Params: map[string]any{paramBillIDs: billIDs},
	}
}

// UpsertVoteAggregates writes the cells in one commit, so callers batch them (the job writes one
// batch of bills at a time). It rejects the whole batch if any cell has an unknown scope or
// status, or a scope key that doesn't fit its scope.
func (s *PipelineStoreImpl) UpsertVoteAggregates(ctx context.Context, cells []model.VoteAggregate) error {
	if len(cells) == 0 {
		return nil
	}
	mutations, err := aggregateMutations(cells)
	if err != nil {
		return fmt.Errorf("upsert vote aggregates: %w", err)
	}
	if _, err = s.client.Apply(ctx, mutations); err != nil {
		return fmt.Errorf("upsert vote aggregates: %w", err)
	}
	return nil
}

// ReviseVoteAggregates reads the bills' stored cells, with their bookkeeping, and writes what
// revise returns, in one read-write transaction: Spanner aborts and retries it (calling revise
// again) if another transaction changes those cells in between. The written cells must fit one
// commit, so callers keep the bills few. Like UpsertVoteAggregates, it writes nothing if any
// cell is invalid.
func (s *PipelineStoreImpl) ReviseVoteAggregates(
	ctx context.Context,
	billIDs []string,
	revise func(stored []model.VoteAggregate) ([]model.VoteAggregate, error),
) error {
	if len(billIDs) == 0 {
		return nil
	}
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		stored, err := queryAggregates(ctx, txn, listAggregatesStatement(billIDs), true)
		if err != nil {
			return err
		}
		cells, err := revise(stored)
		if err != nil {
			return err
		}
		mutations, err := aggregateMutations(cells)
		if err != nil {
			return err
		}
		return txn.BufferWrite(mutations)
	})
	if err != nil {
		return fmt.Errorf("revise vote aggregates: %w", err)
	}
	return nil
}

// aggregateMutations validates the cells and returns their InsertOrUpdate mutations.
func aggregateMutations(cells []model.VoteAggregate) ([]*spanner.Mutation, error) {
	cols := strings.Split(aggregatePublicColumns+", "+aggregateBasisColumns, ", ")
	mutations := make([]*spanner.Mutation, 0, len(cells))
	for i := range cells {
		c := &cells[i]
		if err := validateAggregate(c); err != nil {
			return nil, err
		}
		mutations = append(mutations, spanner.InsertOrUpdate(tableVoteAggregates, cols, []any{
			c.BillID, c.Scope, c.ScopeKey, c.Status,
			ptrToNullInt64(c.YeaPct), ptrToNullInt64(c.NayPct), ptrToNullInt64(c.VotersFloor),
			ptrTimeToNullTime(c.PublishedAt), c.ComputedAt,
			ptrToNullInt64(c.BasisYea), ptrToNullInt64(c.BasisNay), ptrToNullString(c.HoldReason),
		}))
	}
	return mutations, nil
}

// cohortCondition selects the accounts of an aggregates cohort that aren't excluded yet.
const cohortCondition = "sign_in_provider = @provider AND created_at >= @createdFrom AND created_at < @createdTo" +
	" AND agg_excluded_at IS NULL"

func cohortParams(c repository.AggregateCohort) map[string]any {
	return map[string]any{"provider": c.Provider, "createdFrom": c.CreatedFrom, "createdTo": c.CreatedTo}
}

// validateCohort rejects a cohort with no provider or an empty time range, which would match
// nothing or, worse, far more than meant.
func validateCohort(c repository.AggregateCohort) error {
	if c.Provider == "" {
		return errors.New("cohort has no sign-in provider")
	}
	if !c.CreatedFrom.Before(c.CreatedTo) {
		return fmt.Errorf("cohort range %s to %s is empty", c.CreatedFrom.Format(time.RFC3339),
			c.CreatedTo.Format(time.RFC3339))
	}
	return nil
}

// CountAggregateCohort counts the cohort's accounts that aren't excluded yet.
func (s *PipelineStoreImpl) CountAggregateCohort(ctx context.Context, c repository.AggregateCohort) (int64, error) {
	if err := validateCohort(c); err != nil {
		return 0, fmt.Errorf("count aggregate cohort: %w", err)
	}
	var n int64
	err := eachRow(ctx, s.client.Single(), spanner.Statement{
		SQL: "SELECT COUNT(*) FROM users WHERE " + cohortCondition, Params: cohortParams(c),
	}, func(row *spanner.Row) error { return row.Columns(&n) })
	if err != nil {
		return 0, fmt.Errorf("count aggregate cohort: %w", err)
	}
	return n, nil
}

// ExcludeAggregateCohort marks the cohort's accounts excluded at at with one partitioned DML
// statement, so a cohort of any size fits (a normal transaction would hit the mutation limit).
// Accounts already excluded keep their first time.
func (s *PipelineStoreImpl) ExcludeAggregateCohort(
	ctx context.Context,
	c repository.AggregateCohort,
	at time.Time,
) (int64, error) {
	if err := validateCohort(c); err != nil {
		return 0, fmt.Errorf("exclude aggregate cohort: %w", err)
	}
	params := cohortParams(c)
	params["at"] = at
	n, err := s.client.PartitionedUpdate(ctx, spanner.Statement{
		SQL: "UPDATE users SET agg_excluded_at = @at WHERE " + cohortCondition, Params: params,
	})
	if err != nil {
		return 0, fmt.Errorf("exclude aggregate cohort: %w", err)
	}
	return n, nil
}

// UpsertRepAlignments writes the rows in one commit.
func (s *PipelineStoreImpl) UpsertRepAlignments(ctx context.Context, rows []model.RepAlignment) error {
	if len(rows) == 0 {
		return nil
	}
	cols := strings.Split(repAlignmentColumns, ", ")
	mutations := make([]*spanner.Mutation, 0, len(rows))
	for _, a := range rows {
		if a.BillsAgreed > a.BillsCompared {
			return fmt.Errorf("upsert rep alignments: %s agreed on %d of %d bills",
				a.MemberID, a.BillsAgreed, a.BillsCompared)
		}
		mutations = append(mutations, spanner.InsertOrUpdate(tableRepAlignment, cols, []any{
			a.MemberID, int64(a.Congress), a.ScopeKey, int64(a.BillsCompared), int64(a.BillsAgreed), a.ComputedAt,
		}))
	}
	if _, err := s.client.Apply(ctx, mutations); err != nil {
		return fmt.Errorf("upsert rep alignments: %w", err)
	}
	return nil
}

// validateAggregate checks a cell's scope, scope key and status before it's written.
func validateAggregate(c *model.VoteAggregate) error {
	switch c.Scope {
	case model.AggregateScopeNational:
		if c.ScopeKey != "" {
			return fmt.Errorf("bill %s: national cell has scope key %q, want empty", c.BillID, c.ScopeKey)
		}
	case model.AggregateScopeState, model.AggregateScopeDistrict:
		if c.ScopeKey == "" {
			return fmt.Errorf("bill %s: %s cell has no scope key", c.BillID, c.Scope)
		}
	default:
		return fmt.Errorf("bill %s: unknown scope %q", c.BillID, c.Scope)
	}
	switch c.Status {
	case model.AggregateStatusPublished, model.AggregateStatusSuppressed, model.AggregateStatusHeld:
		return nil
	default:
		return fmt.Errorf("bill %s %s %q: unknown status %q", c.BillID, c.Scope, c.ScopeKey, c.Status)
	}
}

// queryAggregates reads vote_aggregates rows selected as aggregatePublicColumns, followed by
// aggregateBasisColumns when withBasis is set.
func queryAggregates(
	ctx context.Context,
	txn querier,
	stmt spanner.Statement,
	withBasis bool,
) ([]model.VoteAggregate, error) {
	iter := txn.Query(ctx, stmt)
	defer iter.Stop()

	var cells []model.VoteAggregate
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return cells, nil
		}
		if err != nil {
			return nil, err
		}
		cell, err := scanAggregate(row, withBasis)
		if err != nil {
			return nil, err
		}
		cells = append(cells, cell)
	}
}

func scanAggregate(row *spanner.Row, withBasis bool) (model.VoteAggregate, error) {
	var (
		c                           model.VoteAggregate
		yeaPct, nayPct, votersFloor spanner.NullInt64
		publishedAt                 spanner.NullTime
		computedAt                  time.Time
		basisYea, basisNay          spanner.NullInt64
		holdReason                  spanner.NullString
	)
	dest := []any{&c.BillID, &c.Scope, &c.ScopeKey, &c.Status, &yeaPct, &nayPct, &votersFloor,
		&publishedAt, &computedAt}
	if withBasis {
		dest = append(dest, &basisYea, &basisNay, &holdReason)
	}
	if err := row.Columns(dest...); err != nil {
		return c, err
	}
	c.YeaPct, c.NayPct, c.VotersFloor = nullInt64Ptr(yeaPct), nullInt64Ptr(nayPct), nullInt64Ptr(votersFloor)
	c.PublishedAt = nullTimePtr(publishedAt)
	c.ComputedAt = computedAt
	c.BasisYea, c.BasisNay, c.HoldReason = nullInt64Ptr(basisYea), nullInt64Ptr(basisNay), nullStringPtr(holdReason)
	return c, nil
}

// Compile-time interface check.
var _ repository.AggregateReader = (*AggregateRepository)(nil)
