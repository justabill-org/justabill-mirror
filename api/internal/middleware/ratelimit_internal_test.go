package middleware

import (
	"testing"
	"time"
)

// cleanup drops every window that has ended, so idle clients don't hold memory, and keeps a window
// still running, so a client can't reset its limit by waiting for the sweep.
func TestRateLimiterCleanupDropsOnlyEndedWindows(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	now := t0
	rl := newRateLimiter(1, defaultMaxKeys, func() time.Time { return now })

	rl.allow("ended") // its window runs t0 to t0+60s
	now = t0.Add(30 * time.Second)
	rl.allow("running") // its window runs to t0+90s, and its one request is used
	now = t0.Add(70 * time.Second)

	rl.cleanup()

	if _, ok := rl.windows["ended"]; ok {
		t.Error("the ended window is still held after cleanup")
	}
	if len(rl.windows) != 1 || rl.order.Len() != 1 {
		t.Errorf("cleanup kept %d windows (%d in order), want 1", len(rl.windows), rl.order.Len())
	}
	if ok, retry := rl.allow("running"); ok || retry != 20*time.Second {
		t.Errorf("allow(running) = %v, %v; want false, 20s: cleanup reset a running window", ok, retry)
	}
}
