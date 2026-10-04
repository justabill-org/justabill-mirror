package sync

import (
	"context"
	"slices"
	"sync"

	"github.com/justabill-org/justabill/pipeline/internal/apicache"
	"github.com/justabill-org/justabill/pipeline/internal/revalidate"
)

// revalidator tells the web app which bills changed; *revalidate.Client is the real one.
type revalidator interface {
	Flush(ctx context.Context, billIDs []string)
}

// apiCacheClearer deletes the API's cached copies of bills; *apicache.Clearer is the real one.
type apiCacheClearer interface {
	Clear(ctx context.Context, billIDs []string)
}

// SetRevalidator makes every step run tell the web app which bills it stored, so their cached
// pages refresh (docs/design/297-revalidate-after-sync.md). A nil client leaves the hook off.
func (s *Service) SetRevalidator(c *revalidate.Client) {
	if c == nil {
		s.revalidator = nil
		return
	}
	s.revalidator = c
}

// SetAPICache makes every step run delete the API's cached copies of the bills it stored, just
// before the web app is told to re-render them (#400). A nil clearer leaves it off.
func (s *Service) SetAPICache(c *apicache.Clearer) {
	if c == nil {
		s.apiCache = nil
		return
	}
	s.apiCache = c
}

// changeSet is the bills one step run stored. It lives in the run's context, not on the
// Service, because serve runs steps concurrently and each run flushes only its own bills.
type changeSet struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

type changeSetKey struct{}

// withChangeSet returns ctx carrying a new, empty change set, and the set.
func withChangeSet(ctx context.Context) (context.Context, *changeSet) {
	cs := &changeSet{ids: map[string]struct{}{}}
	return context.WithValue(ctx, changeSetKey{}, cs), cs
}

// markBill records that the current step run stored bill id. Outside a step run (no change
// set in ctx) it does nothing.
func markBill(ctx context.Context, id string) {
	cs, ok := ctx.Value(changeSetKey{}).(*changeSet)
	if !ok || id == "" {
		return
	}
	cs.mu.Lock()
	cs.ids[id] = struct{}{}
	cs.mu.Unlock()
}

// billIDs returns the marked bills, sorted.
func (cs *changeSet) billIDs() []string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	ids := make([]string, 0, len(cs.ids))
	for id := range cs.ids {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// flushChanges acts on a step run's marked bills: it deletes the API's cached copies, then tells
// the web app, whose re-render reads the API. The order matters: revalidating first could
// re-render from a copy cached before the sync and keep it for the page's revalidate time. It
// runs whether the run succeeded or not, since a failed or cancelled run still stored the bills
// it marked, so it uses a context detached from the run's; each client bounds it with its own
// timeout.
func (s *Service) flushChanges(ctx context.Context, cs *changeSet) {
	if s.apiCache == nil && s.revalidator == nil {
		return
	}
	ids := cs.billIDs()
	if len(ids) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if s.apiCache != nil {
		s.apiCache.Clear(ctx, ids)
	}
	if s.revalidator != nil {
		s.revalidator.Flush(ctx, ids)
	}
}
