package upstream

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// SetCooldowns shortens a budget's cooldowns so tests run in milliseconds.
func SetCooldowns(b *Budget, rateLimited, maxRateLimited, low time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.baseCooldown, b.maxCooldown, b.lowCooldown, b.next429 = rateLimited, maxRateLimited, low, rateLimited
}

// RateLimited exposes Budget.rateLimited.
func RateLimited(b *Budget, retryAfter time.Duration, hasRetryAfter bool) (time.Duration, bool) {
	return b.rateLimited(retryAfter, hasRetryAfter)
}

// Hooks replaces the transport's sleep, jitter and clock; nil keeps the default.
type Hooks struct {
	Sleep  func(context.Context, time.Duration) error
	Jitter func(time.Duration) time.Duration
	Now    func() time.Time
}

// NewClientWithHooks is NewClient with test hooks.
func NewClientWithHooks(log *slog.Logger, hosts map[string]Host, hooks Hooks, opts ...Option) (*http.Client, error) {
	t, err := newTransport(log, hosts, defaultBase())
	if err != nil {
		return nil, err
	}
	for _, o := range opts {
		o(t)
	}
	if hooks.Sleep != nil {
		t.sleep = hooks.Sleep
	}
	if hooks.Jitter != nil {
		t.jitter = hooks.Jitter
	}
	if hooks.Now != nil {
		t.now = hooks.Now
	}
	return &http.Client{Transport: t}, nil
}
