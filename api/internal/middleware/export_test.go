package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// NewTestRateLimit is RateLimit with a map cap and a clock the test controls.
func NewTestRateLimit(
	requestsPerMinute, maxKeys int, now func() time.Time, opts ...RateLimitOption,
) func(http.Handler) http.Handler {
	return rateLimit(newRateLimiter(requestsPerMinute, maxKeys, now), opts...)
}

// NewTestSignedInRateLimit is SignedInRateLimit with a clock the test controls.
func NewTestSignedInRateLimit(
	authn func(http.Handler) http.Handler, perUser, perIP int, now func() time.Time, log *slog.Logger,
) func(http.Handler) http.Handler {
	return signedInRateLimit(authn,
		newRateLimiter(perUser, defaultMaxKeys, now), newRateLimiter(perIP, defaultMaxKeys, now), log)
}

// FromVisitor is fromVisitor, for tests in package middleware_test.
func FromVisitor(r *http.Request) bool { return fromVisitor(r) }
