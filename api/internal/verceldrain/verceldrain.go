// Package verceldrain receives Vercel's log drain and team webhooks and forwards them as
// OpenTelemetry log records, so Vercel's platform problems (function timeouts, out-of-memory
// kills, failed builds and deploys, anomaly alerts) reach the same telemetry pipeline as our own
// code. See docs/design/53-observability.md, Decision 6.
//
// Every request is checked against its x-vercel-signature (HMAC-SHA1 of the body) in constant
// time before anything is parsed. Drain lines are scrubbed: only an allowlist of fields is kept,
// query strings are stripped, and the message goes through obs.Redact. Lines our own code already
// exported through obs are dropped as duplicates.
//
// Before that filter, each line goes through a census (justabill.vercel.drain.lines and .lag),
// and each lambda line that marks a crash, timeout or 5xx counts its request once in
// justabill.vercel.function.failures. See docs/design/473-vercel-function-failures.md.
package verceldrain

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Vercel signs drains and webhooks with HMAC-SHA1; we can't choose.
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
)

const (
	// MaxBodyBytes is the largest drain or webhook body accepted; larger ones get 413.
	MaxBodyBytes = 5 << 20
	// SignatureHeader carries the hex HMAC-SHA1 of the body, keyed with the drain's or the
	// webhook's secret.
	SignatureHeader = "X-Vercel-Signature"
	// scope is the instrumentation scope of the forwarded records.
	scope = "github.com/justabill-org/justabill/api/internal/verceldrain"
)

// ErrNoSecret is returned by New when either secret is empty.
var ErrNoSecret = errors.New("verceldrain: the drain and webhook secrets are required")

// Config configures the receiver. Both secrets are required, and they should differ: Vercel
// generates one per drain and one per webhook.
type Config struct {
	// DrainSecret is the log drain's signature verification secret.
	DrainSecret string
	// WebhookSecret is the team webhook's secret.
	WebhookSecret string
	// Logger is for the receiver's own logs (rejected requests, bad lines). Forwarded records don't
	// go through it.
	Logger *slog.Logger
	// Records receives the forwarded records. Defaults to a logger from the global
	// LoggerProvider, which obs.Start installs, taken when New is called.
	Records log.Logger
	// Now is the clock for delivery lag and the failure counter's dedup window. Defaults to
	// [time.Now].
	Now func() time.Time
}

// Receiver serves the drain, the webhook and the health check.
type Receiver struct {
	drainSecret   []byte
	webhookSecret []byte
	logger        *slog.Logger
	records       log.Logger
	now           func() time.Time
	metrics       instruments
	failed        *failureSet
}

// New returns a Receiver, or ErrNoSecret if either secret is empty. It takes its meter from the
// global MeterProvider, so call it after obs.Start.
func New(cfg Config) (*Receiver, error) {
	if cfg.DrainSecret == "" || cfg.WebhookSecret == "" {
		return nil, ErrNoSecret
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if cfg.Records == nil {
		cfg.Records = global.GetLoggerProvider().Logger(scope)
	}

	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	return &Receiver{
		drainSecret:   []byte(cfg.DrainSecret),
		webhookSecret: []byte(cfg.WebhookSecret),
		logger:        cfg.Logger,
		records:       cfg.Records,
		now:           cfg.Now,
		metrics:       newInstruments(),
		failed:        newFailureSet(maxFailures),
	}, nil
}

// Handler routes POST /v1/drain, POST /v1/webhook and GET /healthz.
func (rc *Receiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/drain", rc.signed(rc.drainSecret, rc.drain))
	mux.HandleFunc("POST /v1/webhook", rc.signed(rc.webhookSecret, rc.webhook))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return mux
}

// signed reads the body (413 over MaxBodyBytes), checks its signature against secret (403 when
// it's missing or wrong) and only then hands the body to next.
func (rc *Receiver) signed(secret []byte, next func(http.ResponseWriter, *http.Request, []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
		if err != nil {
			if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
				rc.logger.WarnContext(r.Context(), "vercel-drain: body too large", "path", r.URL.Path)
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)

				return
			}

			http.Error(w, "reading body", http.StatusBadRequest)

			return
		}

		if !validSignature(secret, body, r.Header.Get(SignatureHeader)) {
			rc.logger.WarnContext(r.Context(), "vercel-drain: bad signature", "path", r.URL.Path)
			http.Error(w, "invalid signature", http.StatusForbidden)

			return
		}

		next(w, r, body)
	}
}

// validSignature reports whether header is the hex HMAC-SHA1 of body under secret, comparing in
// constant time.
func validSignature(secret, body []byte, header string) bool {
	got, err := hex.DecodeString(header)
	if err != nil || len(got) != sha1.Size {
		return false
	}

	mac := hmac.New(sha1.New, secret)
	mac.Write(body)

	return hmac.Equal(got, mac.Sum(nil))
}
