package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

// previousCongressGrace is how long after a new congress starts serve keeps syncing the one
// before it: through January 31. The old congress's last bills are presented to the President
// and signed after January 3, and GovInfo publishes their enrolled texts and public-law numbers
// weeks later. The bill and text steps are incremental, so the extra runs cost little.
const previousCongressGrace = 29 * 24 * time.Hour

// congressWriter is what the tracker needs from the sync service.
type congressWriter interface {
	EnsureCongress(ctx context.Context, congress int) error
	PromoteCongress(ctx context.Context, congress int) (bool, error)
}

// congressTracker decides which congresses serve syncs (#116). With --congress it's that one
// congress and nothing else changes. Otherwise it follows the calendar: the congress in progress
// (rollcall.Current), plus the previous one for previousCongressGrace after the new one starts.
// It writes each congress's row before the first job syncs it, and moves is_current to the
// congress in progress once a members sync has stored its members (PromoteCongress).
type congressTracker struct {
	store  congressWriter
	pinned int // 0: follow the calendar
	now    func() time.Time
	logger *slog.Logger

	mu       sync.Mutex
	ensured  map[int]bool
	promoted int
}

func newCongressTracker(store congressWriter, pinned int, logger *slog.Logger) *congressTracker {
	return &congressTracker{store: store, pinned: pinned, now: time.Now, logger: logger, ensured: map[int]bool{}}
}

// Targets returns the congresses to sync now, the one in progress first, after making sure
// each has its congresses row.
func (t *congressTracker) Targets(ctx context.Context) ([]int, error) {
	if t.pinned != 0 {
		return []int{t.pinned}, nil
	}
	now := t.now()
	current, _ := rollcall.Current(now)
	targets := []int{current}
	if now.Before(rollcall.Start(current).Add(previousCongressGrace)) {
		targets = append(targets, current-1)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range targets {
		if t.ensured[c] {
			continue
		}
		if err := t.store.EnsureCongress(ctx, c); err != nil {
			return nil, fmt.Errorf("congress %d row: %w", c, err)
		}
		t.ensured[c] = true
		t.logger.InfoContext(ctx, "syncing congress", "congress", c, "in_progress", c == current)
	}
	return targets, nil
}

// MembersSynced runs after a successful members sync of the congress. When that congress is
// the one in progress and not yet marked current, it tries to mark it.
func (t *congressTracker) MembersSynced(ctx context.Context, congress int) error {
	if t.pinned != 0 {
		return nil
	}
	if current, _ := rollcall.Current(t.now()); congress != current {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.promoted == congress {
		return nil
	}
	ok, err := t.store.PromoteCongress(ctx, congress)
	if err != nil {
		return fmt.Errorf("mark congress %d current: %w", congress, err)
	}
	if ok {
		t.promoted = congress
	}
	return nil
}

// forEach runs sync for every target congress, even after one fails, and joins the errors
// (joinCongressErrors). A run ended by upstream outages logs a WARN for each host and status.
func (t *congressTracker) forEach(ctx context.Context, sync func(ctx context.Context, congress int) error) error {
	targets, err := t.Targets(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range targets {
		if err = sync(ctx, c); err != nil {
			errs = append(errs, fmt.Errorf("congress %d: %w", c, err))
		}
	}
	outages, err := joinCongressErrors(errs)
	for _, o := range outages {
		t.logger.WarnContext(ctx, "upstream outage; the run is unavailable", "host", o.Host, "status", o.Status)
	}
	return err
}

// joinCongressErrors joins the congresses' errors. The run is unavailable only when every
// congress's error is: it wraps [obs.ErrUnavailable] (senate.gov's refusal), or every cause in
// it is an upstream outage ([upstream.Outages]: a 5xx, or a host that never answered after the
// retries). Then the join wraps [obs.ErrUnavailable], and joinCongressErrors returns each
// distinct outage's host and status for the log. [errors.Is] on a join is true if any part
// matches, so when another congress failed, an unavailable congress's error keeps its text but
// not its chain, and the run fails (#1032, #1084).
func joinCongressErrors(errs []error) ([]upstream.Outage, error) {
	unavailable := 0
	var outages []upstream.Outage
	for _, err := range errs {
		if errors.Is(err, obs.ErrUnavailable) {
			unavailable++
			continue
		}
		if parts, ok := upstream.Outages(err); ok {
			unavailable++
			for _, o := range parts {
				if !slices.Contains(outages, o) {
					outages = append(outages, o)
				}
			}
		}
	}
	joined := errors.Join(errs...)
	switch {
	case unavailable == 0:
		return nil, joined
	case unavailable == len(errs):
		if !errors.Is(joined, obs.ErrUnavailable) {
			joined = fmt.Errorf("%w: %w", obs.ErrUnavailable, joined)
		}
		return outages, joined
	}
	out := make([]error, 0, len(errs))
	for _, err := range errs {
		if errors.Is(err, obs.ErrUnavailable) {
			err = fmt.Errorf("%s", err.Error())
		}
		out = append(out, err)
	}
	return nil, errors.Join(out...)
}
