package upstream

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// rateLimitCooldown is how long a budget pauses after a 429 without Retry-After. It
	// doubles, up to maxRateLimitCooldown, while 429s continue.
	rateLimitCooldown    = 10 * time.Minute
	maxRateLimitCooldown = 60 * time.Minute
	// reserveCooldown is how long a budget pauses once remaining quota falls below its reserve.
	reserveCooldown = 5 * time.Minute

	percent = 100
)

// Quota is what a budget knows about its upstream quota.
type Quota struct {
	// Limit and Remaining come from the last response's X-RateLimit-Limit and
	// X-RateLimit-Remaining headers. They are meaningful only when ObservedAt isn't zero.
	Limit, Remaining int
	ObservedAt       time.Time
	// CooldownUntil is when the current cooldown ends; zero or past means none.
	CooldownUntil time.Time
}

// Budget paces every attempt to one upstream API. All jobs and all hosts that share an
// api.data.gov counter share one Budget. It is safe for concurrent use.
type Budget struct {
	name       string
	limiter    *rate.Limiter
	reservePct int

	// Cooldown lengths; constants in production, shortened by tests.
	baseCooldown, maxCooldown, lowCooldown time.Duration

	mu      sync.Mutex
	quota   Quota
	next429 time.Duration // length of the next 429 cooldown
}

// NewBudget returns a budget named name (the provider in events) that allows rps attempts
// per second with the given burst. When a response reports less than reservePct percent
// of the quota left, the budget pauses before the key gets blocked; 0 turns that off.
func NewBudget(name string, rps float64, burst, reservePct int) (*Budget, error) {
	switch {
	case name == "":
		return nil, errors.New("upstream: budget needs a name")
	case rps <= 0:
		return nil, errors.New("upstream: budget " + name + " needs a positive rate")
	case burst < 1:
		return nil, errors.New("upstream: budget " + name + " needs a burst of at least 1")
	case reservePct < 0 || reservePct >= percent:
		return nil, errors.New("upstream: budget " + name + " reserve must be 0-99 percent")
	}
	return &Budget{
		name:         name,
		limiter:      rate.NewLimiter(rate.Limit(rps), burst),
		reservePct:   reservePct,
		baseCooldown: rateLimitCooldown,
		maxCooldown:  maxRateLimitCooldown,
		lowCooldown:  reserveCooldown,
		next429:      rateLimitCooldown,
	}, nil
}

// Name returns the budget's name.
func (b *Budget) Name() string { return b.name }

// Rate returns the budget's configured attempts per second.
func (b *Budget) Rate() float64 { return float64(b.limiter.Limit()) }

// Burst returns the budget's configured burst.
func (b *Budget) Burst() int { return b.limiter.Burst() }

// Quota returns the last observed quota and the current cooldown, for progress lines,
// status pages and events.
func (b *Budget) Quota() Quota {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.quota
}

// Wait blocks until the budget is out of cooldown and has a token, or ctx ends.
func (b *Budget) Wait(ctx context.Context) error {
	for {
		d := time.Until(b.Quota().CooldownUntil)
		if d <= 0 {
			break
		}
		if err := sleepCtx(ctx, d); err != nil {
			return err
		}
	}
	return b.limiter.Wait(ctx)
}

// rateLimited records a 429. With a Retry-After, the whole budget pauses that long;
// without one, it pauses for the current 429 cooldown, which doubles for the next one.
// A 429 that arrives while a cooldown is running (another worker's request was already
// in flight) doesn't extend or double it. It returns the pause and whether it started a
// new cooldown, so the caller logs one event per cooldown.
func (b *Budget) rateLimited(retryAfter time.Duration, hasRetryAfter bool) (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if hasRetryAfter {
		d := min(retryAfter, b.maxCooldown)
		return d, b.extendLocked(now, d)
	}
	if b.quota.CooldownUntil.After(now) {
		return b.quota.CooldownUntil.Sub(now), false
	}
	d := b.next429
	b.next429 = min(b.next429+b.next429, b.maxCooldown)
	return d, b.extendLocked(now, d)
}

// succeeded resets the 429 cooldown length after a response that wasn't a 429.
func (b *Budget) succeeded() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next429 = b.baseCooldown
}

// observe records the X-RateLimit-* headers of a response. It reports two things: whether
// remaining quota is below the reserve with no cooldown running, so observe started one, and
// whether this is the first response that carried the headers.
func (b *Budget) observe(h http.Header) (bool, bool) {
	limit, errL := strconv.Atoi(h.Get("X-Ratelimit-Limit"))
	remaining, errR := strconv.Atoi(h.Get("X-Ratelimit-Remaining"))
	if errL != nil || errR != nil {
		return false, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	first := b.quota.ObservedAt.IsZero()
	b.quota.Limit, b.quota.Remaining, b.quota.ObservedAt = limit, remaining, now
	if b.reservePct == 0 || limit <= 0 || remaining*percent >= limit*b.reservePct {
		return false, first
	}
	if b.quota.CooldownUntil.After(now) {
		return false, first
	}
	return b.extendLocked(now, b.lowCooldown), first
}

// extendLocked moves the cooldown end to at least now+d and reports whether no cooldown
// was running before. b.mu must be held.
func (b *Budget) extendLocked(now time.Time, d time.Duration) bool {
	started := !b.quota.CooldownUntil.After(now)
	if until := now.Add(d); until.After(b.quota.CooldownUntil) {
		b.quota.CooldownUntil = until
	}
	return started
}

// sleepCtx waits for d or until ctx ends, whichever is first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
