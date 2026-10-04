package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/obs/semconv"
)

// AppCheckHeader carries the Firebase App Check token (X-Firebase-AppCheck).
const AppCheckHeader = "X-Firebase-Appcheck"

const appCheckKey contextKey = "app_check_ok"

// meterScope is the instrumentation scope of the App Check counter.
const meterScope = "github.com/justabill-org/justabill/api/internal/middleware"

// AppCheck checks the request's App Check token on write routes
// (docs/design/89-aggregate-analytics.md) and puts the result in the
// context for AppCheckFromContext:
//   - off (or a nil checker): nothing is checked and there is no result;
//   - audit: a missing or invalid token is recorded as failed, and the
//     request continues;
//   - enforce: a missing token is a 401 app_check_required, an invalid one a
//     401 invalid_app_check_token, and a verifier outage a 503.
//
// Every check in audit or enforce mode adds one to the
// justabill.app_check.verifications counter, labeled with its result (ok,
// missing, invalid or error) and the mode. Logs carry the reason and the
// counter the result, never the token or the user.
func AppCheck(checker auth.AppChecker, mode auth.AppCheckMode, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if checker == nil || mode == auth.AppCheckOff || mode == "" {
			return next
		}
		verifications := newVerificationCounter()
		modeAttr := semconv.AppCheckModeKey.String(string(mode))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			ok := false
			token := r.Header.Get(AppCheckHeader)
			err := verifyAppCheck(ctx, checker, token)
			result := appCheckResult(token, err)
			verifications.Add(ctx, 1, metric.WithAttributes(semconv.AppCheckResultKey.String(result), modeAttr))
			switch {
			case result == semconv.AppCheckResultOK:
				ok = true
			case mode != auth.AppCheckEnforce:
				log.InfoContext(ctx, "app check failed (audit)", "reason", appCheckReason(token, err))
			case result == semconv.AppCheckResultMissing:
				writeAuthError(w, http.StatusUnauthorized, "app_check_required", "missing App Check token")
				return
			case result == semconv.AppCheckResultInvalid:
				log.InfoContext(ctx, "rejected app check token", "error", err)
				writeAuthError(w, http.StatusUnauthorized, "invalid_app_check_token",
					"invalid or expired App Check token")
				return
			default:
				log.ErrorContext(ctx, "verify app check token failed", "error", err)
				writeAuthError(w, http.StatusServiceUnavailable, "app_check_unavailable",
					"app verification is temporarily unavailable")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, appCheckKey, ok)))
		})
	}
}

func verifyAppCheck(ctx context.Context, checker auth.AppChecker, token string) error {
	if token == "" {
		return errors.New("missing App Check token")
	}
	return checker.VerifyAppCheck(ctx, token)
}

// newVerificationCounter creates the justabill.app_check.verifications
// counter on the global meter provider, which obs.Start sets. An error goes to
// the OpenTelemetry error handler, and the counter it returns is then a no-op.
func newVerificationCounter() metric.Int64Counter {
	c, err := otel.Meter(meterScope).Int64Counter(semconv.AppCheckVerificationsName,
		metric.WithUnit(semconv.AppCheckVerificationsUnit),
		metric.WithDescription(semconv.AppCheckVerificationsDescription))
	if err != nil {
		otel.Handle(err)
	}
	return c
}

// appCheckResult is the justabill.app_check.result value for a check that
// returned err: invalid for a bad token, error for a verifier outage.
func appCheckResult(token string, err error) string {
	switch {
	case err == nil:
		return semconv.AppCheckResultOK
	case token == "":
		return semconv.AppCheckResultMissing
	case errors.Is(err, auth.ErrInvalidAppCheckToken):
		return semconv.AppCheckResultInvalid
	default:
		return semconv.AppCheckResultError
	}
}

func appCheckReason(token string, err error) string {
	if token == "" {
		return "missing"
	}
	return err.Error()
}

// AppCheckFromContext returns the request's App Check result, or nil when
// App Check is off.
func AppCheckFromContext(ctx context.Context) *bool {
	ok, found := ctx.Value(appCheckKey).(bool)
	if !found {
		return nil
	}
	return &ok
}
