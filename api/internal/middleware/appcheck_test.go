package middleware_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/justabill-org/justabill/api/internal/auth"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/obs/obstest"
	"github.com/justabill-org/justabill/obs/semconv"
)

// fakeAppCheck accepts the token "good", reports an outage for "outage" and
// rejects anything else.
type fakeAppCheck struct{ calls int }

func (f *fakeAppCheck) VerifyAppCheck(_ context.Context, token string) error {
	f.calls++
	switch token {
	case "good":
		return nil
	case "outage":
		return errors.New("key fetch failed")
	default:
		return fmt.Errorf("%w: bad signature", auth.ErrInvalidAppCheckToken)
	}
}

// serveAppCheck runs one request through the middleware and returns the
// response and the result the next handler saw ("unset", "true", "false"),
// or "" when the next handler didn't run.
func serveAppCheck(
	t *testing.T, checker auth.AppChecker, mode auth.AppCheckMode, token string,
) (*httptest.ResponseRecorder, string) {
	t.Helper()
	seen := ""
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = "unset"
		if ok := mw.AppCheckFromContext(r.Context()); ok != nil {
			seen = strconv.FormatBool(*ok)
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bills/hr-119-1/vote", nil)
	if token != "" {
		req.Header.Set(mw.AppCheckHeader, token)
	}
	rr := httptest.NewRecorder()
	mw.AppCheck(checker, mode, slog.New(slog.DiscardHandler))(next).ServeHTTP(rr, req)
	return rr, seen
}

func TestAppCheckModes(t *testing.T) {
	tests := []struct {
		mode       auth.AppCheckMode
		token      string
		wantStatus int
		wantSeen   string
		wantCode   string
	}{
		{auth.AppCheckOff, "", http.StatusOK, "unset", ""},
		{auth.AppCheckOff, "bad", http.StatusOK, "unset", ""},
		{auth.AppCheckAudit, "good", http.StatusOK, "true", ""},
		{auth.AppCheckAudit, "bad", http.StatusOK, "false", ""},
		{auth.AppCheckAudit, "", http.StatusOK, "false", ""},
		{auth.AppCheckAudit, "outage", http.StatusOK, "false", ""},
		{auth.AppCheckEnforce, "good", http.StatusOK, "true", ""},
		{auth.AppCheckEnforce, "", http.StatusUnauthorized, "", "app_check_required"},
		{auth.AppCheckEnforce, "bad", http.StatusUnauthorized, "", "invalid_app_check_token"},
		{auth.AppCheckEnforce, "outage", http.StatusServiceUnavailable, "", "app_check_unavailable"},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode)+"/"+tt.token, func(t *testing.T) {
			checker := &fakeAppCheck{}
			rr, seen := serveAppCheck(t, checker, tt.mode, tt.token)
			if rr.Code != tt.wantStatus || seen != tt.wantSeen {
				t.Errorf("status %d, next saw %q; want %d, %q", rr.Code, seen, tt.wantStatus, tt.wantSeen)
			}
			if tt.wantCode != "" && !strings.Contains(rr.Body.String(), `"code":"`+tt.wantCode+`"`) {
				t.Errorf("body = %s, want code %s", rr.Body, tt.wantCode)
			}
			if tt.mode == auth.AppCheckOff && checker.calls != 0 {
				t.Errorf("off mode called the verifier %d times", checker.calls)
			}
		})
	}
}

func TestAppCheckWithoutCheckerIsOff(t *testing.T) {
	rr, seen := serveAppCheck(t, nil, auth.AppCheckEnforce, "")
	if rr.Code != http.StatusOK || seen != "unset" {
		t.Errorf("nil checker: status %d, next saw %q; want 200 and no result", rr.Code, seen)
	}
}

// Each check in audit or enforce mode is counted once, with its result and
// the mode as the only attributes, whether or not the request goes on.
// obstest sets the global meter provider, so this test isn't parallel.
func TestAppCheckVerificationsMetric(t *testing.T) {
	tests := []struct {
		mode   auth.AppCheckMode
		token  string
		result string
	}{
		{auth.AppCheckAudit, "good", semconv.AppCheckResultOK},
		{auth.AppCheckAudit, "", semconv.AppCheckResultMissing},
		{auth.AppCheckAudit, "bad", semconv.AppCheckResultInvalid},
		{auth.AppCheckAudit, "outage", semconv.AppCheckResultError},
		{auth.AppCheckEnforce, "good", semconv.AppCheckResultOK},
		{auth.AppCheckEnforce, "", semconv.AppCheckResultMissing},
		{auth.AppCheckEnforce, "bad", semconv.AppCheckResultInvalid},
		{auth.AppCheckEnforce, "outage", semconv.AppCheckResultError},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode)+"/"+tt.result, func(t *testing.T) {
			tel := obstest.New(t) // before the middleware: it takes its counter when it's built
			serveAppCheck(t, &fakeAppCheck{}, tt.mode, tt.token)
			serveAppCheck(t, &fakeAppCheck{}, tt.mode, tt.token)

			points := verificationPoints(t, tel)
			if len(points) != 1 {
				t.Fatalf("got %d series, want 1: %v", len(points), points)
			}
			want := attribute.NewSet(
				semconv.AppCheckResultKey.String(tt.result),
				semconv.AppCheckModeKey.String(string(tt.mode)),
			)
			if got := points[0]; !got.Attributes.Equals(&want) || got.Value != 2 {
				t.Errorf("got %d with %v, want 2 with %v", got.Value, got.Attributes.ToSlice(), want.ToSlice())
			}
		})
	}
}

func TestAppCheckOffRecordsNothing(t *testing.T) {
	tel := obstest.New(t)
	serveAppCheck(t, &fakeAppCheck{}, auth.AppCheckOff, "good")
	serveAppCheck(t, nil, auth.AppCheckEnforce, "good")

	if points := verificationPoints(t, tel); len(points) != 0 {
		t.Errorf("off recorded %v, want nothing", points)
	}
}

// verificationPoints returns the justabill.app_check.verifications series
// recorded so far.
func verificationPoints(t *testing.T, tel *obstest.Telemetry) []metricdata.DataPoint[int64] {
	t.Helper()
	m, found := tel.Metric(t, semconv.AppCheckVerificationsName)
	if !found {
		return nil
	}
	sum, isSum := m.Data.(metricdata.Sum[int64])
	if !isSum || !sum.IsMonotonic {
		t.Fatalf("%s is %T, want a monotonic Int64 sum (a counter)", m.Name, m.Data)
	}
	if m.Unit != semconv.AppCheckVerificationsUnit {
		t.Errorf("unit %q, want %q", m.Unit, semconv.AppCheckVerificationsUnit)
	}
	return sum.DataPoints
}
