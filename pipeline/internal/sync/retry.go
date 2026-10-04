package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/redact"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

const (
	// maxDueRetries is how many due sync_retry items one run of a step takes on, ahead of its
	// listing (docs/design/67-upstream-quota-retries.md, "sync_retry and the watermark").
	maxDueRetries = 500
	// clearRetriesBatch caps the keys in one ClearItemRetries write. A full load clears every
	// bill it synced, and one Spanner commit takes at most 80,000 mutations.
	clearRetriesBatch = 2000
)

// retryList is one run's share of a step's sync_retry queue: the due items it loaded, the
// items that synced (cleared when the run ends) and the failures it recorded. An item counts
// as handled once its failure row is committed; if a row can't be written, the run fails, so
// the watermark stays put and the item is listed again. It is safe for concurrent use.
type retryList struct {
	store    repository.PipelineStore
	logger   *slog.Logger
	step     string
	congress int

	mu       sync.Mutex
	synced   []string
	recorded int
	givenUp  int
	err      error
}

// newRetryList starts a run's retry bookkeeping for step and congress.
func (s *Service) newRetryList(step string, congressNum int) *retryList {
	return &retryList{store: s.store, logger: s.logger, step: step, congress: congressNum}
}

// due returns the IDs of the step's items whose next attempt has come, the longest overdue
// first, at most maxDueRetries.
func (r *retryList) due(ctx context.Context) ([]string, error) {
	items, err := r.store.DueRetries(ctx, r.step, r.congress, time.Now(), maxDueRetries)
	if err != nil {
		return nil, fmt.Errorf("load due %s retries: %w", r.step, err)
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ItemID)
	}
	if len(ids) > 0 {
		r.logger.InfoContext(ctx, "retrying failed items", "step", r.step, "congress", r.congress, "due", len(ids))
	}
	return ids, nil
}

// succeeded marks id synced, so its row (if any) is cleared when the run ends.
func (r *retryList) succeeded(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.synced = append(r.synced, id)
}

// failed records one failed attempt at id. A failure while the run is being cancelled isn't
// recorded: it's likely the cancellation's, and the cancelled run fails and keeps its
// watermark anyway. A failure to record fails the run (see finish).
func (r *retryList) failed(ctx context.Context, id string, cause error) {
	if ctx.Err() != nil {
		return
	}
	permanent := permanentFailure(cause)
	err := r.store.RecordItemFailure(ctx, repository.ItemFailure{
		Step: r.step, Congress: r.congress, ItemID: id,
		Error: redact.Error(redact.URLError(cause)), Permanent: permanent, FailedAt: time.Now(),
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.err = errors.Join(r.err, fmt.Errorf("record %s failure %s: %w", r.step, id, err))
		return
	}
	r.recorded++
	if permanent {
		r.givenUp++
	}
}

// finish clears the synced items' rows and returns an error if any failure couldn't be
// recorded or the rows couldn't be cleared. It clears on a context detached from the run's,
// so a cancelled run still clears what it synced.
func (r *retryList) finish(ctx context.Context) error {
	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stepRecordTimeout)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	for ids := range chunk(r.synced, clearRetriesBatch) {
		if err := r.store.ClearItemRetries(clearCtx, r.step, r.congress, ids); err != nil {
			r.err = errors.Join(r.err, fmt.Errorf("clear %s retries: %w", r.step, err))
			break
		}
	}
	r.logger.InfoContext(ctx, "sync retries updated", "step", r.step, "congress", r.congress,
		"synced", len(r.synced), "failed", r.recorded, "given_up", r.givenUp)
	return r.err
}

// chunk yields s in pieces of at most n.
func chunk[T any](s []T, n int) func(yield func([]T) bool) {
	return func(yield func([]T) bool) {
		for start := 0; start < len(s); start += n {
			if !yield(s[start:min(start+n, len(s))]) {
				return
			}
		}
	}
}

// permanentFailure reports whether err will fail the same way on a later run, so its item is
// given up at once. Every cause in err's tree has to be permanent: upstream.IsPermanent, or
// a 404 from Congress.gov. A bill whose actions answered 404 while its subjects answered 500
// is retried.
func permanentFailure(err error) bool {
	switch e := err.(type) { //nolint:errorlint // walks the tree by hand to see every cause
	case nil:
		return false
	case interface{ Unwrap() []error }:
		causes := e.Unwrap()
		for _, c := range causes {
			if !permanentFailure(c) {
				return false
			}
		}
		return len(causes) > 0
	case interface{ Unwrap() error }:
		return permanentFailure(e.Unwrap())
	default:
		//nolint:errorlint // a leaf: there's nothing left to unwrap
		return upstream.IsPermanent(err) || err == congress.ErrNotFound || err == errUnknownBill
	}
}
