// Package upstream builds the one *[http.Client] the pipeline uses to reach Congress.gov,
// GovInfo and the public download hosts. Every attempt, retries included, waits for a
// token from the host's shared Budget; transient failures are retried with jittered
// backoff; each attempt has a deadline that covers reading the body; bodies are capped;
// and the api.data.gov key is added as a header only for the host it belongs to.
package upstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/semconv"
)

// meterScope is the instrumentation scope of the quota gauge.
const meterScope = "github.com/justabill-org/justabill/pipeline/internal/upstream"

// Base transport timeouts. http.Client.Timeout stays 0: it would also count time spent
// waiting for a budget token, so each attempt gets its own deadline instead.
const (
	dialTimeout           = 10 * time.Second
	keepAlive             = 30 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 30 * time.Second
	idleConnTimeout       = 90 * time.Second
	maxIdleConnsPerHost   = 10
)

// Host is how the client treats one upstream host.
type Host struct {
	// Budget paces every attempt to this host. Required. Hosts may share one.
	Budget *Budget
	// APIKey is sent as the X-Api-Key header to this host only; empty for public hosts.
	APIKey string
	// AttemptTimeout bounds one attempt, from sending the request to reading the whole
	// body. WithAttemptTimeout overrides it for one request (large downloads).
	AttemptTimeout time.Duration
	// MaxBodyBytes caps a response body.
	MaxBodyBytes int64
}

// Option configures a client.
type Option func(*transport)

// WithArchive makes the client hand every 2xx response body to a.
func WithArchive(a *Archive) Option {
	return func(t *transport) { t.archive = a }
}

// NewClient returns an *[http.Client] that sends requests only to the declared hosts, keyed
// by host name as it appears in the URL (with the port, if any). Redirects are new
// attempts, paced and keyed by their own host.
//
// Each attempt is a CLIENT span from [obs.HTTPTransport], which sends no trace headers
// upstream, and responses with X-RateLimit-Remaining set justabill.upstream.quota.remaining.
// Call it after obs.Start: it takes its tracer and meter when it's called.
func NewClient(log *slog.Logger, hosts map[string]Host, opts ...Option) (*http.Client, error) {
	t, err := newTransport(log, hosts, defaultBase())
	if err != nil {
		return nil, err
	}
	for _, o := range opts {
		o(t)
	}
	return &http.Client{Transport: t}, nil
}

func defaultBase() *http.Transport {
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: keepAlive}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       idleConnTimeout,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
	}
}

func newTransport(log *slog.Logger, hosts map[string]Host, base http.RoundTripper) (*transport, error) {
	if log == nil {
		return nil, errors.New("upstream: NewClient needs a logger")
	}
	declared := make(map[string]Host, len(hosts))
	for name, h := range hosts {
		switch {
		case h.Budget == nil:
			return nil, fmt.Errorf("upstream: host %s has no budget", name)
		case h.AttemptTimeout <= 0:
			return nil, fmt.Errorf("upstream: host %s has no attempt timeout", name)
		case h.MaxBodyBytes <= 0:
			return nil, fmt.Errorf("upstream: host %s has no body cap", name)
		}
		declared[strings.ToLower(name)] = h
	}
	quota, err := otel.Meter(meterScope).Int64Gauge(semconv.UpstreamQuotaRemainingName,
		metric.WithUnit(semconv.UpstreamQuotaRemainingUnit),
		metric.WithDescription(semconv.UpstreamQuotaRemainingDescription))
	if err != nil {
		return nil, fmt.Errorf("upstream: quota gauge: %w", err)
	}
	return &transport{
		log:    log,
		hosts:  declared,
		base:   obs.HTTPTransport(base),
		quota:  quota,
		sleep:  sleepCtx,
		jitter: fullJitter,
		now:    time.Now,
	}, nil
}

type ctxKey int

const (
	idempotentKey ctxKey = iota
	attemptTimeoutKey
)

// WithIdempotent marks requests made with ctx as safe to retry even though their method
// isn't GET or HEAD (GovInfo's POST /search). The request needs a GetBody, which
// [http.NewRequest] sets for in-memory bodies.
func WithIdempotent(ctx context.Context) context.Context {
	return context.WithValue(ctx, idempotentKey, true)
}

// WithAttemptTimeout overrides the host's AttemptTimeout for requests made with ctx.
func WithAttemptTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, attemptTimeoutKey, d)
}

func attemptTimeout(ctx context.Context, h Host) time.Duration {
	if d, ok := ctx.Value(attemptTimeoutKey).(time.Duration); ok && d > 0 {
		return d
	}
	return h.AttemptTimeout
}

func retryableMethod(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead:
		return true
	}
	marked, _ := req.Context().Value(idempotentKey).(bool)
	return marked && (req.Body == nil || req.Body == http.NoBody || req.GetBody != nil)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
