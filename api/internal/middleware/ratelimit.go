package middleware

import (
	"container/list"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	cleanupInterval = 1 * time.Minute
	defaultWindow   = 1 * time.Minute

	// defaultMaxKeys caps how many client windows one limiter keeps, so a
	// flood of distinct addresses can't grow the map without bound.
	defaultMaxKeys = 100_000

	// unknownClientKey is the bucket shared by every request ClientIP found
	// no trusted client IP for.
	unknownClientKey = "unknown"

	// ipv6ClientBits groups IPv6 clients by /64, the smallest block a
	// subscriber usually gets, so one host can't rotate through billions of
	// addresses to get fresh buckets.
	ipv6ClientBits = 64

	// callerMaxKeys caps a trusted caller limiter's map: it has one bucket
	// per caller class, not per client.
	callerMaxKeys = 16
)

// Rate-limit classes: the kind of bucket a request is counted in, which the
// limiter logs with every request it refuses.
const (
	// ClassIP is a public limiter's per-IP buckets, and what a [CallerClass]
	// returns for an ordinary caller.
	ClassIP = "ip"
	// ClassWebServer is the web app's own server (Next.js on Vercel), which
	// shares egress IPs with other people's renders and so gets one bucket of
	// its own instead.
	ClassWebServer = "web-server"
	// ClassReps is POST /reps's own per-IP limiter.
	ClassReps = "reps"
	// ClassSearch is bill search's own per-IP limiter, on top of the public
	// one (#619).
	ClassSearch = "search"
	// ClassWebServerSearch is the web app's server's one bucket in the
	// search limiter, apart from its [ClassWebServer] bucket in the public
	// one (#619).
	ClassWebServerSearch = "web-server-search"
	// ClassAccount is the account routes' per-IP buckets, for the requests
	// that don't carry a valid ID token ([SignedInRateLimit]).
	ClassAccount = "account"
	// ClassUser is the account routes' per-account buckets, one for each
	// signed-in identity, whatever IP its requests come from
	// ([SignedInRateLimit]).
	ClassUser = "user"
)

// CallerClass returns the rate-limit class of r's caller: [ClassIP] for an
// ordinary client, limited by IP, or the class of a trusted caller such as
// [ClassWebServer]. It's the hook that decides who is trusted, so a different
// proof (a Vercel OIDC token, say) can replace [ServerKeys] without touching
// the limiter.
type CallerClass func(r *http.Request) string

// RateLimitOption configures [RateLimit].
type RateLimitOption func(*limitConfig)

// limitConfig is what the options set.
type limitConfig struct {
	class       string
	log         *slog.Logger
	callers     CallerClass
	callerLimit int
}

// LogAs names the limiter's per-IP buckets class and logs every request it
// refuses to log, as event=rate_limited with the class of the bucket that
// was full, so 429s can be counted by class. The log line carries no client
// address.
func LogAs(class string, log *slog.Logger) RateLimitOption {
	return func(c *limitConfig) {
		c.class = class
		c.log = log
	}
}

// TrustedCallers counts every request that classify puts in a class other
// than [ClassIP] in one bucket for that class, of perMinute requests per
// window, instead of in its client's per-IP bucket. Every other request is
// limited by IP as usual, and so is a trusted caller's request that [Visitor]
// attributed to a visitor: it counts in that visitor's per-IP bucket.
func TrustedCallers(classify CallerClass, perMinute int) RateLimitOption {
	return func(c *limitConfig) {
		c.callers = classify
		c.callerLimit = perMinute
	}
}

// window is one key's fixed rate-limit window.
type window struct {
	key       string
	remaining int
	start     time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]*list.Element
	// order holds the windows by start time, oldest first, so cleanup and
	// eviction never scan the whole map.
	order   *list.List
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time
	once    sync.Once
}

func newRateLimiter(requestsPerWindow, maxKeys int, now func() time.Time) *rateLimiter {
	return &rateLimiter{
		windows: make(map[string]*list.Element),
		order:   list.New(),
		limit:   requestsPerWindow,
		window:  defaultWindow,
		maxKeys: maxKeys,
		now:     now,
	}
}

func windowOf(e *list.Element) *window {
	w, _ := e.Value.(*window)
	return w
}

func (rl *rateLimiter) startCleanup() {
	rl.once.Do(func() {
		go func() {
			ticker := time.NewTicker(cleanupInterval)
			defer ticker.Stop()
			for range ticker.C {
				rl.cleanup()
			}
		}()
	})
}

// cleanup drops every window that has ended.
func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	for e := rl.order.Front(); e != nil; e = rl.order.Front() {
		w := windowOf(e)
		if now.Sub(w.start) < rl.window {
			return
		}
		rl.order.Remove(e)
		delete(rl.windows, w.key)
	}
}

// refund gives back a request allow counted for key in its current window,
// up to the limit. A window that has ended or been dropped is left alone.
func (rl *rateLimiter) refund(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	e, ok := rl.windows[key]
	if !ok {
		return
	}
	if w := windowOf(e); rl.now().Sub(w.start) < rl.window && w.remaining < rl.limit {
		w.remaining++
	}
}

// allow reports whether key may make a request now. When it may not, it also
// returns how long until key's window resets.
func (rl *rateLimiter) allow(key string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	if e, ok := rl.windows[key]; ok {
		w := windowOf(e)
		if elapsed := now.Sub(w.start); elapsed < rl.window {
			if w.remaining <= 0 {
				return false, rl.window - elapsed
			}
			w.remaining--
			return true, 0
		}
		w.start = now
		w.remaining = rl.limit - 1
		rl.order.MoveToBack(e)
		return true, 0
	}

	if len(rl.windows) >= rl.maxKeys {
		oldest := rl.order.Front()
		rl.order.Remove(oldest)
		delete(rl.windows, windowOf(oldest).key)
	}
	rl.windows[key] = rl.order.PushBack(&window{key: key, remaining: rl.limit - 1, start: now})
	return true, 0
}

// limitKey returns the bucket key for a request: the client IP ClientIP
// recorded, with IPv6 grouped by /64, or the shared unknown bucket. It never
// reads request headers or RemoteAddr itself.
func limitKey(r *http.Request) string {
	ip := clientIPOf(r)
	if !ip.IsValid() {
		return unknownClientKey
	}
	if ip.Is6() {
		if p, err := ip.Prefix(ipv6ClientBits); err == nil {
			return p.String()
		}
	}
	return ip.String()
}

// retryAfterSeconds rounds a wait up to whole seconds, at least 1, as the
// Retry-After header needs.
func retryAfterSeconds(wait time.Duration) int {
	return max(1, int(math.Ceil(wait.Seconds())))
}

// RateLimit returns middleware that limits each client to requestsPerMinute
// requests per fixed one-minute window. The client is the IP that ClientIP
// recorded in the context (so ClientIP must run first); requests without one
// share a single unknown bucket. [TrustedCallers] gives trusted callers a
// bucket of their own instead. A request over the limit gets a 429 with
// Retry-After.
//
// Each call makes an independent limiter, so a route can have its own,
// tighter limit on top of the public one: r.With(RateLimit(10)).Post(...).
func RateLimit(requestsPerMinute int, opts ...RateLimitOption) func(http.Handler) http.Handler {
	return rateLimit(newRateLimiter(requestsPerMinute, defaultMaxKeys, time.Now), opts...)
}

func rateLimit(rl *rateLimiter, opts ...RateLimitOption) func(http.Handler) http.Handler {
	cfg := limitConfig{class: ClassIP, log: slog.New(slog.DiscardHandler)}
	for _, opt := range opts {
		opt(&cfg)
	}
	rl.startCleanup()
	var callers *rateLimiter
	if cfg.callers != nil {
		callers = newRateLimiter(cfg.callerLimit, callerMaxKeys, rl.now)
		callers.startCleanup()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limiter, key, class := rl, limitKey(r), cfg.class
			if callers != nil {
				if c := cfg.callers(r); c != ClassIP && !fromVisitor(r) {
					limiter, key, class = callers, c, c
				}
			}
			if ok, wait := limiter.allow(key); !ok {
				refuse(w, r, wait, class, cfg.log)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// refuse answers a request over its limit: a 429 with Retry-After, logged to
// log as event=rate_limited with the class of the bucket that was full.
func refuse(w http.ResponseWriter, r *http.Request, wait time.Duration, class string, log *slog.Logger) {
	log.InfoContext(r.Context(), "rate limit exceeded", "event", "rate_limited", "class", class)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(wait)))
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]string{
		keyError: "rate limit exceeded",
	})
}

// SignedInRateLimit returns middleware for the routes that need a signed-in
// user. It runs authn, the auth middleware ([Auth.Handler]), itself, and
// limits each request by who sent it:
//   - a request authn verified counts in its identity's own bucket ([ClassUser],
//     keyed by the auth UID), of perUser requests a minute, so people signed
//     in behind one IP (a carrier NAT, a campus) don't share a budget;
//   - every other request, anonymous or turned away by authn (a bad or
//     expired token, or sign-in down), counts in its client's per-IP bucket
//     ([ClassAccount]), of perIP requests a minute.
//
// Every request takes a place in its IP's bucket before authn sees it, and a
// verified one gives it back, so while an IP's bucket is full every request
// from it gets a 429 unverified: a flood of bad tokens from one address,
// concurrent or not, costs no verification and no log line per token. That
// refuses signed-in people at the same address too, until the window resets.
// ClientIP must run first.
func SignedInRateLimit(
	authn func(http.Handler) http.Handler, perUser, perIP int, log *slog.Logger,
) func(http.Handler) http.Handler {
	return signedInRateLimit(authn,
		newRateLimiter(perUser, defaultMaxKeys, time.Now), newRateLimiter(perIP, defaultMaxKeys, time.Now), log)
}

func signedInRateLimit(
	authn func(http.Handler) http.Handler, users, clients *rateLimiter, log *slog.Logger,
) func(http.Handler) http.Handler {
	users.startCleanup()
	clients.startCleanup()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Every request reserves a place in its IP's bucket before authn
			// runs, so a concurrent burst of bad tokens can't all get past a
			// bucket that isn't full yet; a verified one gives it back.
			ip := limitKey(r)
			if ok, wait := clients.allow(ip); !ok {
				refuse(w, r, wait, ClassAccount, log)
				return
			}
			authn(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if p, ok := PrincipalFromContext(r.Context()); ok {
					clients.refund(ip)
					if allowed, wait := users.allow(p.UID); !allowed {
						refuse(w, r, wait, ClassUser, log)
						return
					}
				}
				next.ServeHTTP(w, r)
			})).ServeHTTP(w, r)
		})
	}
}
