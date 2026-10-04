package sync

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/semconv"
)

// countItem counts one item of the running job on justabill.pipeline.items: ok, or failed when
// err isn't nil. Outside a job (obs.Job) it does nothing.
func countItem(ctx context.Context, err error) {
	if err != nil {
		obs.Items(ctx, semconv.ItemOutcomeFailed, 1)
		return
	}
	obs.Items(ctx, semconv.ItemOutcomeOK, 1)
}

// progressEvery is how many bills pass between progress lines.
const progressEvery = 250

// billProgress counts a bill sync's outcomes and logs a progress line every progressEvery
// bills, so a long load can be watched from its logs.
type billProgress struct {
	logger    *slog.Logger
	requests  func() int64
	start     time.Time
	startReqs int64
	total     int
	skipped   int
	processed syncCounter
	done      syncCounter
	failed    syncCounter
	// unresolved counts bills Congress.gov doesn't know (SyncBillsByID). They aren't retried.
	unresolved syncCounter
}

func newBillProgress(logger *slog.Logger, requests func() int64, total, skipped int) *billProgress {
	return &billProgress{
		logger:    logger,
		requests:  requests,
		start:     time.Now(),
		startReqs: requests(),
		total:     total,
		skipped:   skipped,
	}
}

// add records one bill's outcome. It is safe for concurrent use.
func (p *billProgress) add(ctx context.Context, ok bool) {
	if ok {
		p.done.inc()
		obs.Items(ctx, semconv.ItemOutcomeOK, 1)
	} else {
		p.failed.inc()
		obs.Items(ctx, semconv.ItemOutcomeFailed, 1)
	}
	p.tick(ctx)
}

// addUnresolved records a bill that was skipped because Congress.gov doesn't know it.
func (p *billProgress) addUnresolved(ctx context.Context) {
	p.unresolved.inc()
	obs.Items(ctx, semconv.ItemOutcomeSkipped, 1)
	p.tick(ctx)
}

func (p *billProgress) tick(ctx context.Context) {
	if p.processed.n.Add(1)%progressEvery == 0 {
		p.log(ctx, "bills progress")
	}
}

func (p *billProgress) log(ctx context.Context, msg string) {
	elapsed := time.Since(p.start)
	rps := 0.0
	if secs := elapsed.Seconds(); secs > 0 {
		const hundredths = 100
		rps = math.Round(float64(p.requests()-p.startReqs)/secs*hundredths) / hundredths
	}
	p.logger.InfoContext(ctx, msg,
		"done", p.done.get(),
		"failed", p.failed.get(),
		"unresolved", p.unresolved.get(),
		"skipped", p.skipped,
		"remaining", p.total-p.processed.get(),
		"requests_per_second", rps,
		"elapsed", elapsed.Round(time.Second).String(),
	)
}
