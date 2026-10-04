// Package cache is the API's Redis cache: string values with a TTL, used for cacheable
// responses. The API runs without it when Redis isn't configured, and serves uncached while
// Redis is down.
package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

// defaultBackoff is how long a failed command makes Get, Set and Delete skip Redis.
const defaultBackoff = 10 * time.Second

// ErrBadURL is returned by [New] for a Redis URL that doesn't parse. It says nothing about the
// URL, since any piece of it could be the password.
var ErrBadURL = errors.New("REDIS_URL doesn't parse; a password must be URL-escaped (a hex one needs none)")

// ErrUnavailable is returned by Get, Set and Delete without calling Redis, while a recent
// command failed. Callers serve uncached; there's nothing to log, the first failure was.
var ErrUnavailable = errors.New("redis cache unavailable")

// Cache wraps a Redis client.
type Cache struct {
	client  *redis.Client
	backoff time.Duration
	now     func() time.Time
	// retryAt is when Get, Set and Delete call Redis again after a failure, in Unix
	// nanoseconds; zero while Redis answers.
	retryAt atomic.Int64
}

// New creates a new Cache from a Redis URL. Every command is a CLIENT span and feeds the
// db.client.* metrics, through the global OpenTelemetry providers. Spans leave out the command
// text (db.statement), which holds cache keys and cached responses.
//
// New doesn't connect: it fails only on a bad URL. The client dials on demand, so a cache made
// while Redis is down starts working once Redis is up (#457). After a failed command, Get, Set
// and Delete return [ErrUnavailable] for a short back-off rather than dial on every request.
// [Cache.Ping] always calls Redis, and its success ends the back-off.
func New(redisURL string) (*Cache, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		// The parse error quotes the URL or a piece of it, which can be the password (#599).
		return nil, ErrBadURL
	}

	client := redis.NewClient(opts)
	if err = errors.Join(
		redisotel.InstrumentTracing(client, redisotel.WithDBStatement(false)),
		redisotel.InstrumentMetrics(client),
	); err != nil {
		_ = client.Close()
		return nil, err
	}

	return &Cache{client: client, backoff: defaultBackoff, now: time.Now}, nil
}

// Get retrieves a value by key.
func (c *Cache) Get(ctx context.Context, key string) (string, error) {
	if c.backingOff() {
		return "", ErrUnavailable
	}
	val, err := c.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return val, c.observe(ctx, err)
}

// Set stores a value with a TTL.
func (c *Cache) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	if c.backingOff() {
		return ErrUnavailable
	}
	return c.observe(ctx, c.client.Set(ctx, key, value, ttl).Err())
}

// Delete removes one or more keys.
func (c *Cache) Delete(ctx context.Context, keys ...string) error {
	if c.backingOff() {
		return ErrUnavailable
	}
	return c.observe(ctx, c.client.Del(ctx, keys...).Err())
}

// Ping checks that Redis answers. Unlike the other commands it ignores the back-off, so the
// readiness probe sees Redis come back, and a successful Ping ends the back-off.
func (c *Cache) Ping(ctx context.Context) error {
	return c.observe(ctx, c.client.Ping(ctx).Err())
}

// Close closes the underlying Redis client.
func (c *Cache) Close() error {
	return c.client.Close()
}

// backingOff reports whether a recent failure means commands skip Redis.
func (c *Cache) backingOff() bool {
	at := c.retryAt.Load()
	return at != 0 && c.now().UnixNano() < at
}

// observe starts the back-off when a command fails, unless the caller's context ended
// (a canceled request says nothing about Redis), and ends it when one succeeds.
func (c *Cache) observe(ctx context.Context, err error) error {
	switch {
	case err == nil:
		c.retryAt.Store(0)
	case ctx.Err() == nil:
		c.retryAt.Store(c.now().Add(c.backoff).UnixNano())
	}
	return err
}
