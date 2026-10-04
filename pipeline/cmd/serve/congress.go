package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
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

// forEach runs sync for every target congress, even after one fails, and joins the errors.
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
	return errors.Join(errs...)
}
