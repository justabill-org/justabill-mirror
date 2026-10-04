// Package apicache deletes the API's cached copies of the bills a sync changed, so the web app's
// re-render after a revalidation reads the synced bill rather than a copy the API cached before
// the sync (#400).
//
// The API keeps each bill's GET /api/v1/bills/{id} response in Redis under
// [cachekey.BillDetail] for 5 minutes, and its GET /api/v1/bills/{id}/law-changes response under
// [cachekey.BillLawChanges] for an hour. Deleting those keys is best effort, like the
// revalidation: a failure is logged and never fails the sync, since the copies expire on
// their own.
package apicache

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/justabill-org/justabill/db/cachekey"
	"github.com/justabill-org/justabill/pipeline/internal/secretfile"
)

const (
	// MaxKeysPerDel is the most keys one DEL carries, so a large run doesn't send one huge
	// command.
	MaxKeysPerDel = 500
	// DefaultTimeout bounds one Clear, every batch included.
	DefaultTimeout = 5 * time.Second
	// keysPerBill is how many keys [Keys] returns for each bill.
	keysPerBill = 2
)

// delFunc deletes keys and returns how many existed; (*redis.Client).Del is the real one.
type delFunc func(ctx context.Context, keys []string) (int64, error)

// Clearer deletes the API's cached bill responses. A nil *Clearer is valid and does nothing, so
// callers don't branch on whether Redis is configured.
type Clearer struct {
	del     delFunc
	logger  *slog.Logger
	timeout time.Duration
}

// Option configures a [Clearer].
type Option func(*Clearer)

// WithLogger sets the logger for the cleared and failed lines. The default discards them.
func WithLogger(l *slog.Logger) Option { return func(c *Clearer) { c.logger = l } }

// WithTimeout sets how long one Clear may take in all. The default is [DefaultTimeout].
func WithTimeout(d time.Duration) Option { return func(c *Clearer) { c.timeout = d } }

// errBadURL is New's error for a Redis URL that doesn't parse. It says nothing about the URL,
// since any piece of it could be the password.
var errBadURL = errors.New("REDIS_URL: doesn't parse; a password must be URL-escaped (a hex one needs none)")

// New returns a clearer for the Redis at redisURL (redis://[:password@]host:port[/db], the
// API's REDIS_URL). With redisURL empty, clearing is off and New returns nil. It doesn't
// connect: the client dials on the first Clear, so Redis being down at startup doesn't stop
// the pipeline. The client lives as long as the process.
func New(redisURL string, opts ...Option) (*Clearer, error) {
	redisURL = strings.TrimSpace(redisURL)
	if redisURL == "" {
		return nil, nil //nolint:nilnil // nil is the documented "off" clearer
	}
	ropts, err := redis.ParseURL(redisURL)
	if err != nil {
		// The parse error quotes the URL or a piece of it, which can be the password: a base64
		// password's "/" makes its start read as the port (#599). So none of it is kept.
		return nil, errBadURL
	}
	client := redis.NewClient(ropts)
	del := func(ctx context.Context, keys []string) (int64, error) {
		return client.Del(ctx, keys...).Result()
	}
	return newClearer(del, opts...), nil
}

// FromConfig builds a clearer from the pipeline's configuration, read through get (viper's
// GetString, with underscore keys): redis_url, the same REDIS_URL the API reads, or
// redis_url_file naming a file that holds it (production mounts it, since the URL carries the
// password). It returns nil when neither is set, and an error when
// both are, the file can't be read or the URL doesn't parse.
func FromConfig(get func(key string) string, opts ...Option) (*Clearer, error) {
	redisURL, err := secretfile.Resolve(get, "redis_url")
	if err != nil {
		return nil, err
	}
	return New(redisURL, opts...)
}

func newClearer(del delFunc, opts ...Option) *Clearer {
	c := &Clearer{
		del:     del,
		logger:  slog.New(slog.DiscardHandler),
		timeout: DefaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Keys returns the API cache keys for billIDs: a [cachekey.BillDetail] and a
// [cachekey.BillLawChanges] per distinct, non-empty ID, in the order given.
func Keys(billIDs []string) []string {
	seen := make(map[string]struct{}, len(billIDs))
	keys := make([]string, 0, keysPerBill*len(billIDs))
	for _, id := range billIDs {
		if _, dup := seen[id]; dup || id == "" {
			continue
		}
		seen[id] = struct{}{}
		keys = append(keys, cachekey.BillDetail(id), cachekey.BillLawChanges(id))
	}
	return keys
}

// Clear deletes the API's cached responses for billIDs, in DELs of at most [MaxKeysPerDel]
// keys. It logs the outcome and never returns an error; it stops at the first failed DEL. The
// clearer's timeout applies on top of ctx's deadline, so callers that must clear even after a
// shutdown started pass [context.WithoutCancel].
func (c *Clearer) Clear(ctx context.Context, billIDs []string) {
	if c == nil {
		return
	}
	keys := Keys(billIDs)
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var deleted int64
	for batch := range slices.Chunk(keys, MaxKeysPerDel) {
		n, err := c.del(ctx, batch)
		if err != nil {
			c.logger.WarnContext(ctx, "api cache clear failed", "bills", len(keys)/keysPerBill, "deleted", deleted,
				"error", err)
			return
		}
		deleted += n
	}
	c.logger.InfoContext(ctx, "api cache cleared", "bills", len(keys)/keysPerBill, "deleted", deleted)
}
