package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/justabill-org/justabill/pipeline/internal/apikey"
)

const (
	// maxAttempts bounds the attempts for one request, the first included.
	maxAttempts = 5
	// Retry n (from 1) waits a random time in [0, min(backoffCap, backoffBase·2^n)].
	backoffBase = time.Second
	backoffCap  = 60 * time.Second
	// maxRetryAfter is the longest Retry-After a request waits for. Beyond that it fails,
	// and the item is left for a later run rather than holding a worker.
	maxRetryAfter = 5 * time.Minute
	// secondsPerHour converts api.data.gov's hourly X-RateLimit-Limit to a rate.
	secondsPerHour = 3600
)

type transport struct {
	log   *slog.Logger
	hosts map[string]Host
	// base sends one attempt; each is a CLIENT span and an http.client.request.duration point.
	base http.RoundTripper
	// quota is justabill.upstream.quota.remaining, from each response's X-RateLimit-Remaining.
	quota   metric.Int64Gauge
	archive *Archive // nil: responses aren't archived

	// Injected for tests.
	sleep  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
	now    func() time.Time
}

// RoundTrip implements [http.RoundTripper]: one call is one request, with up to maxAttempts
// attempts.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	h, hostErr := t.checkHost(req)
	if hostErr != nil {
		closeBody(req)
		return nil, hostErr
	}
	ctx := req.Context()
	var lastStatus int
	for attempt := 1; ; attempt++ {
		if waitErr := h.Budget.Wait(ctx); waitErr != nil {
			if attempt == 1 {
				// The base transport closes the body once it has it; before the first
				// attempt, RoundTrip's contract makes that our job.
				closeBody(req)
			}
			return nil, t.fail(ctx, req, h, lastStatus, attempt-1, waitErr)
		}
		resp, body, err := t.attempt(req, h, attempt)
		if err == nil && resp.StatusCode < http.StatusBadRequest {
			h.Budget.succeeded()
			t.observe(ctx, req.URL.Hostname(), h, resp.Header)
			if t.archive != nil && archivable(req, resp) {
				t.archive.add(req, resp, body, t.now())
			}
			return resp, nil
		}
		lastStatus = 0
		if resp != nil {
			lastStatus = resp.StatusCode
			t.observe(ctx, req.URL.Hostname(), h, resp.Header)
			_ = resp.Body.Close()
		}
		wait, retry := t.retryWait(req, h, resp, err, attempt)
		if !retry {
			return nil, t.fail(ctx, req, h, lastStatus, attempt, err)
		}
		t.log.DebugContext(ctx, "upstream_retry", "provider", h.Budget.Name(), "host", req.URL.Host,
			"path", req.URL.Path, "status", lastStatus, "attempt", attempt, "wait_ms", wait.Milliseconds())
		if sleepErr := t.sleep(ctx, wait); sleepErr != nil {
			return nil, t.fail(ctx, req, h, lastStatus, attempt, sleepErr)
		}
	}
}

// archivable reports whether a response goes to the archive: a 2xx with a body to keep.
func archivable(req *http.Request, resp *http.Response) bool {
	return req.Method != http.MethodHead && resp.StatusCode >= http.StatusOK &&
		resp.StatusCode < http.StatusMultipleChoices
}

// closeBody closes the request body for an error return before the base transport has
// taken it.
func closeBody(req *http.Request) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
}

func (t *transport) checkHost(req *http.Request) (Host, error) {
	h, ok := t.hosts[strings.ToLower(req.URL.Host)]
	if !ok {
		return Host{}, fmt.Errorf("%w: %s", ErrUnknownHost, req.URL.Host)
	}
	if h.APIKey != "" && req.URL.Scheme != "https" && !isLoopback(req.URL.Hostname()) {
		return Host{}, fmt.Errorf("%w: %s", ErrInsecureKey, req.URL.Host)
	}
	return h, nil
}

// attempt sends one attempt and reads its whole body under the attempt's deadline, so a
// stalled body is a failed attempt that can be retried. The returned response's body is
// in memory, and also returned as body; err is non-nil only when no response was read.
func (t *transport) attempt(req *http.Request, h Host, n int) (*http.Response, []byte, error) {
	ctx, cancel := context.WithTimeout(req.Context(), attemptTimeout(req.Context(), h))
	defer cancel()

	out := req.Clone(ctx)
	if n > 1 && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, nil, err
		}
		out.Body = body
	}
	out.Header.Del(apikey.Header)
	if h.APIKey != "" {
		out.Header.Set(apikey.Header, h.APIKey)
	}

	resp, err := t.base.RoundTrip(out)
	if err != nil {
		return nil, nil, err
	}
	body, err := readCapped(resp.Body, h.MaxBodyBytes)
	_ = resp.Body.Close()
	if err != nil {
		return nil, nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Request = req
	return resp, body, nil
}

func readCapped(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w (%d bytes)", ErrBodyTooLarge, limit)
	}
	return body, nil
}

// retryWait decides whether a failed attempt is retried and how long to wait first. A 429
// also pauses the host's whole budget, so the retry and every other caller wait in
// Budget.Wait.
func (t *transport) retryWait(
	req *http.Request, h Host, resp *http.Response, err error, attempt int,
) (time.Duration, bool) {
	ctx := req.Context()
	if ctx.Err() != nil || attempt >= maxAttempts || !retryableMethod(req) {
		return 0, false
	}
	backoff := t.jitter(min(backoffCap, backoffBase<<attempt))
	if resp == nil {
		return backoff, !errors.Is(err, ErrBodyTooLarge)
	}
	if !retryableStatus(resp.StatusCode) {
		return 0, false
	}
	ra, hasRA := retryAfter(resp.Header, t.now())
	if resp.StatusCode == http.StatusTooManyRequests {
		pause, started := h.Budget.rateLimited(ra, hasRA)
		if started {
			t.log.WarnContext(ctx, "quota_cooldown", "provider", h.Budget.Name(), "reason", "rate_limited",
				"cooldown_s", int(pause.Seconds()))
		}
	}
	if !hasRA {
		return backoff, true
	}
	return ra, ra <= maxRetryAfter
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryAfter parses a Retry-After header in either form: delay seconds or an HTTP date.
func retryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(header.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(max(secs, 0)) * time.Second, true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

// observe records a response's quota headers: the remaining quota on the gauge, for host, and
// in the budget. It logs quota_low when the reserve brake starts, and quota_rate_high once, on
// the first quota a budget sees, when the configured rate would spend more than the hourly limit.
func (t *transport) observe(ctx context.Context, host string, h Host, header http.Header) {
	if remaining, err := strconv.ParseInt(header.Get("X-Ratelimit-Remaining"), 10, 64); err == nil {
		t.quota.Record(ctx, remaining, metric.WithAttributes(semconv.ServerAddress(host)))
	}
	low, first := h.Budget.observe(header)
	if !low && !first {
		return
	}
	q := h.Budget.Quota()
	if first && h.Budget.Rate()*secondsPerHour > float64(q.Limit) {
		t.log.WarnContext(ctx, "quota_rate_high", "provider", h.Budget.Name(), "rps", h.Budget.Rate(),
			"quota_limit", q.Limit, "max_rps", float64(q.Limit)/secondsPerHour)
	}
	if low {
		t.log.WarnContext(ctx, "quota_low", "provider", h.Budget.Name(), "quota_limit", q.Limit,
			"quota_remaining", q.Remaining, "cooldown_s", int(h.Budget.lowCooldown.Seconds()))
	}
}

// fail builds the error for a request that won't be retried and logs upstream_error once.
// err is the last attempt's error, or nil for an HTTP error status. A request the caller
// cancelled returns the context's error and logs nothing.
func (t *transport) fail(
	ctx context.Context, req *http.Request, h Host, status, attempts int, err error,
) error {
	if ctx.Err() != nil {
		return fmt.Errorf("upstream %s%s: %w", req.URL.Host, req.URL.Path, ctx.Err())
	}
	if err == nil {
		err = &StatusError{Host: req.URL.Host, Path: req.URL.Path, Status: status, Attempts: attempts}
	} else {
		err = fmt.Errorf("upstream %s%s after %d attempt(s): %w", req.URL.Host, req.URL.Path, attempts, err)
	}
	attrs := []any{"provider", h.Budget.Name(), "host", req.URL.Host, "path", req.URL.Path,
		"status", status, "attempts", attempts}
	if q := h.Budget.Quota(); !q.ObservedAt.IsZero() {
		attrs = append(attrs, "quota_remaining", q.Remaining)
	}
	t.log.WarnContext(ctx, "upstream_error", append(attrs, "error", err.Error())...)
	return err
}

// fullJitter returns a random duration in [0, d].
func fullJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return rand.N(d + 1) //nolint:gosec // backoff jitter, not a secret
}
