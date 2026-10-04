package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/api/internal/district"
	"github.com/justabill-org/justabill/api/internal/handler"
)

const (
	capitol     = "1100 Congress Ave, Austin, TX 78701"
	capitolBody = `{"address":"` + capitol + `"}`
)

// noDistricts is a district lookup that finds nothing, without calling the Census Bureau.
type noDistricts struct{}

func (noDistricts) FromAddress(context.Context, string, int) ([]district.Result, error) {
	return nil, nil
}

func (noDistricts) FromCoordinates(context.Context, float64, float64, int) ([]district.Result, error) {
	return nil, nil
}

// repsRouter is the API's router with lookup as the geocoder, logging to logs
// through the server's own logger.
func repsRouter(t *testing.T, lookup district.Lookup, logs *bytes.Buffer) http.Handler {
	t.Helper()
	log := newLogger(slog.NewJSONHandler(logs, nil))
	h := handler.New(nil,
		handler.WithDistrict(lookup),
		handler.WithCongresses(currentCongress{}),
		handler.WithMembers(texasMembers{}),
	)
	h.SetLogger(log)
	return buildRouter(h, log, nil, devEdge(t, 0))
}

// GET /reps is gone: the address only ever travels in a POST body.
func TestGetRepsIsNotAllowed(t *testing.T) {
	r := repsRouter(t, noDistricts{}, &bytes.Buffer{})
	rr := do(t, r, http.MethodGet, "/api/v1/reps?address=1100+Congress+Ave", "", "")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /reps = %d, want 405", rr.Code)
	}
	if post := do(t, r, http.MethodPost, "/api/v1/reps", "", capitolBody); post.Code != http.StatusOK {
		t.Errorf("POST /reps = %d, want 200: %s", post.Code, post.Body)
	}
}

// POST /reps allows 10 requests a minute per IP, well under the public 60.
func TestRepsRateLimit(t *testing.T) {
	r := repsRouter(t, noDistricts{}, &bytes.Buffer{})
	for i := range repsRateLimit {
		if rr := do(t, r, http.MethodPost, "/api/v1/reps", "", capitolBody); rr.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i+1, rr.Code)
		}
	}
	rr := do(t, r, http.MethodPost, "/api/v1/reps", "", capitolBody)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Errorf("request %d = %d (Retry-After %q), want 429 with Retry-After",
			repsRateLimit+1, rr.Code, rr.Header().Get("Retry-After"))
	}
	if other := do(t, r, http.MethodGet, "/api/v1/congresses", "", ""); other.Code == http.StatusTooManyRequests {
		t.Errorf("GET /congresses = 429: the /reps limit spilled over to other routes")
	}
}

// When the Census geocoder fails in any way, POST /reps is a 502 and neither
// the logs nor the response carry any part of the address. A transport failure
// is a *[url.Error], whose message would include the request URL and so the
// address if the lookup didn't strip it.
func TestRepsCensusFailureLogsNoAddress(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		url     string
	}{
		{name: "connection dropped", handler: func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}},
		{name: "server error echoing the query", handler: func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad address: "+r.URL.RawQuery, http.StatusInternalServerError)
		}},
		{name: "not JSON", handler: func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>" + r.URL.Query().Get("address") + "</html>"))
		}},
		{name: "connection refused", url: closed.URL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			geocoder := tt.url
			if tt.handler != nil {
				stub := httptest.NewServer(tt.handler)
				t.Cleanup(stub.Close)
				geocoder = stub.URL
			}
			var logs bytes.Buffer
			r := repsRouter(t, district.NewCensusLookupWithURL(http.DefaultClient, geocoder), &logs)

			rr := do(t, r, http.MethodPost, "/api/v1/reps", "", capitolBody)
			if rr.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want 502: %s", rr.Code, rr.Body)
			}
			if !strings.Contains(logs.String(), `"msg":"district lookup failed"`) {
				t.Errorf("want the failure logged, got %s", logs.String())
			}
			checkNoAddress(t, "log", logs.String())
			checkNoAddress(t, "response", rr.Body.String())
		})
	}
}

// checkNoAddress fails the test if out holds any part of the capitol's address,
// plain or query-encoded.
func checkNoAddress(t *testing.T, what, out string) {
	t.Helper()
	for _, part := range []string{"Congress Ave", "Congress+Ave", "Austin", "78701"} {
		if strings.Contains(out, part) {
			t.Errorf("%s contains %q from the address: %s", what, part, out)
		}
	}
}
