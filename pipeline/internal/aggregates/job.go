// Package aggregates runs the aggregate-votes job: an hourly snapshot of eligible user votes,
// turned into published cells in vote_aggregates and rep_alignment by the publication rules
// and anomaly holds of docs/design/89-aggregate-analytics.md. The API serves only those tables,
// never a live tally.
package aggregates

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs/semconv"
)

const (
	meterScope = "github.com/justabill-org/justabill/pipeline/internal/aggregates"

	// billBatch is how many bills' cells are read, decided and written in one read-write
	// transaction. A bill has at most about 500 cells (national, 56 states and territories, 441
	// districts) of 12 columns, so 10 bills stay under Spanner's 80,000 mutations per commit.
	billBatch = 10
	// writeBatch is how many rep_alignment rows go in one commit.
	writeBatch = 2000
)

// Store is the part of [repository.PipelineStore] the job uses.
type Store interface {
	AggregateSnapshot(ctx context.Context, p repository.AggregateSnapshotParams) (*repository.AggregateSnapshot, error)
	ListVoteAggregates(ctx context.Context, billIDs []string) ([]model.VoteAggregate, error)
	ReviseVoteAggregates(
		ctx context.Context, billIDs []string, revise func(stored []model.VoteAggregate) ([]model.VoteAggregate, error),
	) error
	BillSeatPositions(ctx context.Context, billIDs []string) ([]repository.SeatPosition, error)
	UpsertRepAlignments(ctx context.Context, rows []model.RepAlignment) error
}

// Job is the aggregate-votes job.
type Job struct {
	store Store
	cfg   Config
	log   *slog.Logger
	now   func() time.Time

	cells metric.Int64Gauge
	holds metric.Int64Counter
	votes metric.Int64Gauge
}

// Option configures a [Job].
type Option func(*Job)

// WithClock sets the job's clock (tests).
func WithClock(now func() time.Time) Option {
	return func(j *Job) { j.now = now }
}

// New returns the job. Its metrics use the global meter provider, which obs.Start sets.
func New(store Store, cfg Config, log *slog.Logger, opts ...Option) (*Job, error) {
	j := &Job{store: store, cfg: cfg, log: log, now: time.Now}
	for _, opt := range opts {
		opt(j)
	}
	meter := otel.Meter(meterScope)
	var err error
	if j.cells, err = meter.Int64Gauge(semconv.AggregatesCellsName,
		metric.WithUnit(semconv.AggregatesCellsUnit),
		metric.WithDescription(semconv.AggregatesCellsDescription)); err != nil {
		return nil, fmt.Errorf("aggregates: cells gauge: %w", err)
	}
	if j.holds, err = meter.Int64Counter(semconv.AggregatesHoldsName,
		metric.WithUnit(semconv.AggregatesHoldsUnit),
		metric.WithDescription(semconv.AggregatesHoldsDescription)); err != nil {
		return nil, fmt.Errorf("aggregates: holds counter: %w", err)
	}
	if j.votes, err = meter.Int64Gauge(semconv.AggregatesVotesName,
		metric.WithUnit(semconv.AggregatesVotesUnit),
		metric.WithDescription(semconv.AggregatesVotesDescription)); err != nil {
		return nil, fmt.Errorf("aggregates: votes gauge: %w", err)
	}
	return j, nil
}

// Result sums up one run.
type Result struct {
	// Cells counts the cells written, by status.
	Cells map[string]int
	// Holds counts the cells put on hold in this run, by reason.
	Holds map[string]int
	// Eligible counts the eligible votes, and Ineligible the rest by reason.
	Eligible   int
	Ineligible map[string]int
	// Alignments counts the rep_alignment rows written.
	Alignments int
	// Decided lists every cell's outcome, in bill and scope order. Only [Job.Report] fills it.
	Decided []Outcome
}

// Run takes a snapshot, decides and writes every cell, then recomputes rep_alignment from the
// shown cells. Cells are written a batch of bills at a time, so a failed run leaves earlier
// batches written; the next run decides again from what's stored, so rerunning is safe.
func (j *Job) Run(ctx context.Context) error {
	_, err := j.RunOnce(ctx)
	return err
}

// RunOnce is [Job.Run], returning what it did.
func (j *Job) RunOnce(ctx context.Context) (*Result, error) {
	return j.run(ctx, false)
}

// Report is a dry run of [Job.RunOnce] (the aggregates CLI's report): it takes the same snapshot
// and decides every cell the same way, but writes nothing, records no metrics and logs no holds.
// The Result lists every decision in Decided, and Alignments counts the rows it would write.
func (j *Job) Report(ctx context.Context) (*Result, error) {
	return j.run(ctx, true)
}

func (j *Job) run(ctx context.Context, dryRun bool) (*Result, error) {
	asOf := j.now().UTC()
	snap, err := j.store.AggregateSnapshot(ctx, repository.AggregateSnapshotParams{
		AsOf: asOf, MinAccountAge: j.cfg.MinAccountAge, YoungAccountAge: j.cfg.YoungAccountAge,
		RequireAppCheck: j.cfg.RequireAppCheck, BurstWindow: burstWindow, BaselineWindow: baselineWindow,
	})
	if err != nil {
		return nil, fmt.Errorf("aggregate votes: %w", err)
	}
	counts := rollUp(snap.Groups)
	res := &Result{Cells: map[string]int{}, Holds: map[string]int{}, Ineligible: snap.Ineligible}
	for _, g := range snap.Groups {
		res.Eligible += g.Yea + g.Nay
	}

	billSet := map[string]bool{}
	for k := range counts {
		billSet[k.BillID] = true
	}
	for _, id := range snap.CellBills {
		billSet[id] = true
	}
	bills := slices.Sorted(maps.Keys(billSet))

	shown := map[CellKey]model.VoteAggregate{}
	for batch := range slices.Chunk(bills, billBatch) {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if err = j.decideBatch(ctx, batch, counts, asOf, dryRun, res, shown); err != nil {
			return nil, err
		}
	}
	if res.Alignments, err = j.writeAlignments(ctx, shown, asOf, dryRun); err != nil {
		return nil, err
	}
	if !dryRun {
		j.record(ctx, res)
	}
	return res, nil
}

// decideBatch decides every cell of the bills, stored or counted, writes them in one read-write
// transaction (unless dryRun), and adds the shown state and district cells to shown.
func (j *Job) decideBatch(
	ctx context.Context,
	bills []string,
	counts map[CellKey]*Counts,
	asOf time.Time,
	dryRun bool,
	res *Result,
	shown map[CellKey]model.VoteAggregate,
) error {
	// The transaction may call decide more than once; only the last call's outcomes count.
	var decided []Outcome
	decide := func(stored []model.VoteAggregate) ([]model.VoteAggregate, error) {
		decided = decideCells(bills, stored, counts, j.cfg, asOf)
		cells := make([]model.VoteAggregate, len(decided))
		for i := range decided {
			cells[i] = decided[i].Cell
		}
		return cells, nil
	}
	var err error
	if dryRun {
		var stored []model.VoteAggregate
		if stored, err = j.store.ListVoteAggregates(ctx, bills); err == nil {
			_, err = decide(stored)
		}
	} else {
		err = j.store.ReviseVoteAggregates(ctx, bills, decide)
	}
	if err != nil {
		return fmt.Errorf("aggregate votes: %w", err)
	}

	for _, out := range decided {
		k := keyOf(&out.Cell)
		res.Cells[out.Cell.Status]++
		if out.NewHold != "" {
			res.Holds[out.NewHold]++
		}
		if dryRun {
			res.Decided = append(res.Decided, out)
		} else if out.NewHold != "" {
			c := counts[k]
			if c == nil {
				c = &Counts{}
			}
			// Cell keys and counts only: never a user.
			j.log.WarnContext(ctx, "aggregate cell held", "bill_id", k.BillID, "scope", k.Scope,
				"scope_key", k.ScopeKey, "reason", out.NewHold, "status", out.Cell.Status,
				"yea", c.Yea, "nay", c.Nay, "recent", c.Recent, "baseline", c.Baseline, "new", c.New, "young", c.Young)
		}
		if k.Scope != scopeNational && out.Cell.Status != statusSuppressed {
			shown[k] = out.Cell
		}
	}
	return nil
}

// decideCells applies [Publish] to every cell of the bills that is stored or has counts, in bill
// and scope order.
func decideCells(
	bills []string,
	stored []model.VoteAggregate,
	counts map[CellKey]*Counts,
	cfg Config,
	asOf time.Time,
) []Outcome {
	prev := make(map[CellKey]*model.VoteAggregate, len(stored))
	for i := range stored {
		prev[keyOf(&stored[i])] = &stored[i]
	}
	inBatch := map[string]bool{}
	for _, id := range bills {
		inBatch[id] = true
	}
	keys := slices.Collect(maps.Keys(prev))
	for k := range counts {
		if inBatch[k.BillID] && prev[k] == nil {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b CellKey) int {
		return cmp.Or(
			cmp.Compare(a.BillID, b.BillID),
			cmp.Compare(a.Scope, b.Scope),
			cmp.Compare(a.ScopeKey, b.ScopeKey),
		)
	})

	out := make([]Outcome, 0, len(keys))
	for _, k := range keys {
		var c Counts
		if n := counts[k]; n != nil {
			c = *n
		}
		out = append(out, Publish(k, prev[k], c, cfg, asOf))
	}
	return out
}

// writeAlignments recomputes rep_alignment for the members whose constituency has a shown cell,
// and writes it unless dryRun.
func (j *Job) writeAlignments(
	ctx context.Context,
	shown map[CellKey]model.VoteAggregate,
	asOf time.Time,
	dryRun bool,
) (int, error) {
	billSet := map[string]bool{}
	for k := range shown {
		billSet[k.BillID] = true
	}
	if len(billSet) == 0 {
		return 0, nil
	}
	positions, err := j.store.BillSeatPositions(ctx, slices.Sorted(maps.Keys(billSet)))
	if err != nil {
		return 0, fmt.Errorf("aggregate votes: rep alignment: %w", err)
	}
	byKey := alignments(shown, positions)
	rows := make([]model.RepAlignment, 0, len(byKey))
	for _, r := range byKey {
		r.ComputedAt = asOf
		rows = append(rows, *r)
	}
	slices.SortFunc(rows, func(a, b model.RepAlignment) int {
		return cmp.Or(cmp.Compare(a.MemberID, b.MemberID), cmp.Compare(a.Congress, b.Congress),
			cmp.Compare(a.ScopeKey, b.ScopeKey))
	})
	if dryRun {
		return len(rows), nil
	}
	for chunk := range slices.Chunk(rows, writeBatch) {
		if err = j.store.UpsertRepAlignments(ctx, chunk); err != nil {
			return 0, fmt.Errorf("aggregate votes: rep alignment: %w", err)
		}
	}
	return len(rows), nil
}

// record emits the run's metrics and a summary log line.
func (j *Job) record(ctx context.Context, res *Result) {
	for _, status := range []string{statusPublished, statusSuppressed, statusHeld} {
		j.cells.Record(ctx, int64(res.Cells[status]),
			metric.WithAttributes(semconv.AggregatesStatusKey.String(status)))
	}
	for reason, n := range res.Holds {
		j.holds.Add(ctx, int64(n), metric.WithAttributes(semconv.AggregatesHoldReasonKey.String(reason)))
	}
	eligibility := func(v string) metric.MeasurementOption {
		return metric.WithAttributes(semconv.AggregatesEligibilityKey.String(v))
	}
	j.votes.Record(ctx, int64(res.Eligible), eligibility(semconv.AggregatesEligibilityEligible))
	j.votes.Record(ctx, int64(res.Ineligible[repository.IneligibleYoung]),
		eligibility(semconv.AggregatesEligibilityYoungAccount))
	j.votes.Record(ctx, int64(res.Ineligible[repository.IneligibleNoAppCheck]),
		eligibility(semconv.AggregatesEligibilityAppCheck))
	j.votes.Record(ctx, int64(res.Ineligible[repository.IneligibleExcluded]),
		eligibility(semconv.AggregatesEligibilityExcluded))
	j.log.InfoContext(ctx, "aggregate votes done",
		"published", res.Cells[statusPublished], "suppressed", res.Cells[statusSuppressed],
		"held", res.Cells[statusHeld], "new_holds", sum(res.Holds), "eligible_votes", res.Eligible,
		"ineligible_votes", sum(res.Ineligible), "alignments", res.Alignments)
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
