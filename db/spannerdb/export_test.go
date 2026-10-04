package spannerdb

import (
	"context"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
)

// HoldSummaryBatchInChunks is HoldSummaryBatch with a smaller commit size, so a test can see the
// chunks without seeding thousands of bills.
func (s *PipelineStoreImpl) HoldSummaryBatchInChunks(
	ctx context.Context, h repository.SummaryBatchHolds, chunk int,
) error {
	return s.holdSummaryBatch(ctx, h, chunk)
}

// ClientConfig is the configuration NewClient builds from opts.
func ClientConfig(opts ...Option) spanner.ClientConfig { return clientConfig(opts...) }

// QueryMissingDiffPairsInPages is QueryMissingDiffPairs with pages of pageBills bills, so a test
// can cross page boundaries without seeding thousands of bills.
func (s *PipelineStoreImpl) QueryMissingDiffPairsInPages(
	ctx context.Context, limit, pageBills int,
) ([]repository.DiffPair, error) {
	return s.queryMissingDiffPairs(ctx, limit, pageBills)
}

// DeleteNonConsecutiveDiffsInPages is DeleteNonConsecutiveDiffs with pages of pageBills bills.
func (s *PipelineStoreImpl) DeleteNonConsecutiveDiffsInPages(
	ctx context.Context, pageBills int,
) (repository.DeletedDiffs, error) {
	return s.deleteNonConsecutiveDiffs(ctx, pageBills)
}
