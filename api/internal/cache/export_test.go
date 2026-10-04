package cache

import "time"

// SetClock replaces the clock and back-off, so tests can step past the back-off.
func (c *Cache) SetClock(now func() time.Time, backoff time.Duration) {
	c.now, c.backoff = now, backoff
}
