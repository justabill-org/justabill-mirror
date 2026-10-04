package revalidate_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/revalidate"
)

// call is one request the fake web route received.
type call struct {
	auth, contentType string
	tags              []string
}

// webRoute is a fake /api/revalidate. It answers each call with the next status in statuses
// (200 once they run out); a status of -1 drops the connection without an answer.
type webRoute struct {
	mu       gosync.Mutex
	statuses []int
	calls    []call
}

func (w *webRoute) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Tags []string `json:"tags"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	w.mu.Lock()
	w.calls = append(w.calls, call{
		auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"), tags: body.Tags,
	})
	status := http.StatusOK
	if len(w.statuses) > 0 {
		status, w.statuses = w.statuses[0], w.statuses[1:]
	}
	w.mu.Unlock()
	if status == -1 {
		conn, _, err := rw.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	rw.WriteHeader(status)
	_, _ = fmt.Fprintf(rw, `{"status":%d}`, status)
}

func (w *webRoute) received() []call {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls)
}

// newClient returns a client for route with a 1 ms retry delay, and its log.
func newClient(t *testing.T, route http.Handler, opts ...revalidate.Option) (*revalidate.Client, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(route)
	t.Cleanup(srv.Close)
	var log bytes.Buffer
	opts = append([]revalidate.Option{
		revalidate.WithLogger(slog.New(slog.NewTextHandler(&log, nil))),
		revalidate.WithHTTPClient(srv.Client()),
		revalidate.WithRetryDelay(time.Millisecond),
	}, opts...)
	return revalidate.New(srv.URL+"/api/revalidate", "s3cret", opts...), &log
}

func TestNew_OffWithoutURLOrSecret(t *testing.T) {
	for _, tt := range []struct{ url, secret string }{{"", "s"}, {"https://x.test/api/revalidate", ""}, {"", ""}} {
		if c := revalidate.New(tt.url, tt.secret); c != nil {
			t.Errorf("New(%q, %q) = %v, want nil", tt.url, tt.secret, c)
		}
	}
	var c *revalidate.Client
	c.Flush(t.Context(), []string{"hr-119-1"}) // a nil client is a no-op
}

func TestFlush_SendsTheBillTags(t *testing.T) {
	route := &webRoute{}
	c, log := newClient(t, route)

	c.Flush(t.Context(), []string{"hr-119-1"})

	got := route.received()
	if len(got) != 1 {
		t.Fatalf("calls = %+v, want one", got)
	}
	if got[0].auth != "Bearer s3cret" || got[0].contentType != "application/json" {
		t.Errorf("headers: Authorization %q, Content-Type %q", got[0].auth, got[0].contentType)
	}
	if !slices.Equal(got[0].tags, []string{"bill:hr-119-1"}) {
		t.Errorf("tags = %v, want [bill:hr-119-1]", got[0].tags)
	}
	if !strings.Contains(log.String(), `msg="web revalidation sent" bills=1 tags=1`) {
		t.Errorf("log:\n%s\nwant a sent line with the bill count", log)
	}
	if strings.Contains(log.String(), "s3cret") {
		t.Errorf("the secret was logged:\n%s", log)
	}
}

func TestFlush_NothingToSend(t *testing.T) {
	route := &webRoute{}
	c, log := newClient(t, route)

	c.Flush(t.Context(), nil)
	c.Flush(t.Context(), []string{"PN25-37", "hr-119-1; drop"})

	if got := route.received(); len(got) != 0 {
		t.Errorf("calls = %+v, want none", got)
	}
	if !strings.Contains(log.String(), "no valid bill IDs") {
		t.Errorf("log:\n%s\nwant the skipped IDs logged", log)
	}
}

func TestFlush_Retries(t *testing.T) {
	tests := []struct {
		name      string
		statuses  []int
		wantCalls int
		wantLog   string
	}{
		{"a 5xx is retried once", []int{http.StatusBadGateway}, 2, "web revalidation sent"},
		{"a dropped connection is retried once", []int{-1}, 2, "web revalidation sent"},
		{"two 5xx give up", []int{http.StatusInternalServerError, http.StatusServiceUnavailable}, 2,
			`level=WARN msg="web revalidation failed" bills=1 tags=1 status=503`},
		{"a 4xx isn't retried", []int{http.StatusUnauthorized}, 1,
			`level=WARN msg="web revalidation failed" bills=1 tags=1 status=401`},
		{"a 404 (route off) isn't retried", []int{http.StatusNotFound}, 1, "status=404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := &webRoute{statuses: tt.statuses}
			c, log := newClient(t, route)

			c.Flush(t.Context(), []string{"s-119-5"})

			if got := route.received(); len(got) != tt.wantCalls {
				t.Errorf("calls = %d, want %d", len(got), tt.wantCalls)
			}
			if !strings.Contains(log.String(), tt.wantLog) {
				t.Errorf("log:\n%s\nwant %q", log, tt.wantLog)
			}
		})
	}
}

func TestFlush_WaitsTheRetryDelay(t *testing.T) {
	route := &webRoute{statuses: []int{http.StatusBadGateway}}
	c, _ := newClient(t, route, revalidate.WithRetryDelay(50*time.Millisecond))

	start := time.Now()
	c.Flush(t.Context(), []string{"s-119-5"})

	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("Flush returned after %v, want the retry delay first", elapsed)
	}
	if got := route.received(); len(got) != 2 {
		t.Errorf("calls = %d, want 2", len(got))
	}
}

func TestFlush_GivesUpAtTheTimeout(t *testing.T) {
	release := make(chan struct{})
	hang := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release })
	c, log := newClient(t, hang, revalidate.WithTimeout(50*time.Millisecond),
		revalidate.WithRetryDelay(time.Hour))
	t.Cleanup(func() { close(release) }) // before the server's Close, which waits for the handler

	start := time.Now()
	c.Flush(t.Context(), []string{"hr-119-1"})

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Flush took %v, want it bounded by the 50 ms timeout", elapsed)
	}
	if !strings.Contains(log.String(), "web revalidation failed") ||
		!strings.Contains(log.String(), "deadline exceeded") {
		t.Errorf("log:\n%s\nwant a failed line naming the deadline", log)
	}
}

func TestTags(t *testing.T) {
	many := make([]string, 0, revalidate.MaxTags+1)
	for n := range revalidate.MaxTags + 1 {
		many = append(many, fmt.Sprintf("hr-119-%d", n+1))
	}
	tests := []struct {
		name string
		ids  []string
		want []string
	}{
		{"sorted and distinct", []string{"s-119-2", "hr-119-1", "s-119-2"}, []string{"bill:hr-119-1", "bill:s-119-2"}},
		{"IDs the route rejects are dropped", []string{"hjres-119-35", "HR-119-1", "hr-119-", "pn-119-1234567"},
			[]string{"bill:hjres-119-35"}},
		{"exactly the limit", many[:revalidate.MaxTags], nil},
		{"over the limit sends bills", many, []string{revalidate.AllBillsTag}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := revalidate.Tags(tt.ids)
			if tt.want == nil {
				if len(got) != revalidate.MaxTags || got[0] == revalidate.AllBillsTag {
					t.Errorf("got %d tags starting %q, want %d bill tags", len(got), got[0], revalidate.MaxTags)
				}
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Tags(%v) = %v, want %v", tt.ids, got, tt.want)
			}
		})
	}
}

func TestFlush_OverTheLimitSendsBills(t *testing.T) {
	route := &webRoute{}
	c, log := newClient(t, route)
	ids := make([]string, 0, revalidate.MaxTags+1)
	for n := range revalidate.MaxTags + 1 {
		ids = append(ids, fmt.Sprintf("s-119-%d", n+1))
	}

	c.Flush(t.Context(), ids)

	got := route.received()
	if len(got) != 1 || !slices.Equal(got[0].tags, []string{"bills"}) {
		t.Fatalf("calls = %+v, want one with [bills]", got)
	}
	if !strings.Contains(log.String(), "bills=101 tags=1") {
		t.Errorf("log:\n%s\nwant the bill count", log)
	}
}

// config is a viper-like getter over a map.
type config map[string]string

func (c config) get(key string) string { return c[key] }

// secretFile writes secret to a file in a temporary directory and returns its path.
func secretFile(t *testing.T, secret string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFromConfig(t *testing.T) {
	route := &webRoute{}
	srv := httptest.NewServer(route)
	t.Cleanup(srv.Close)

	tests := []struct {
		name string
		cfg  config
		auth string // the Authorization header sent; empty means the hook is off
	}{
		{name: "unset is off", cfg: config{}},
		{name: "URL only is off", cfg: config{"web_revalidate_url": srv.URL}},
		{name: "secret only is off", cfg: config{"web_revalidate_secret": "s"}},
		{name: "URL and secret", cfg: config{"web_revalidate_url": srv.URL, "web_revalidate_secret": " s "},
			auth: "Bearer s"},
		{name: "secret file", auth: "Bearer from-file", cfg: config{
			"web_revalidate_url": srv.URL, "web_revalidate_secret_file": secretFile(t, "from-file\n"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := revalidate.FromConfig(tt.cfg.get, revalidate.WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatal(err)
			}
			if (c != nil) != (tt.auth != "") {
				t.Fatalf("client = %v, want on: %v", c, tt.auth != "")
			}
			before := len(route.received())
			c.Flush(t.Context(), []string{"hr-119-1"})
			got := route.received()[before:]
			if tt.auth != "" && (len(got) != 1 || got[0].auth != tt.auth) {
				t.Errorf("calls = %+v, want one with %q", got, tt.auth)
			}
		})
	}
}

func TestFromConfig_Errors(t *testing.T) {
	file := secretFile(t, "from-file")
	tests := []struct {
		name    string
		cfg     config
		wantErr string
	}{
		{"both secret forms", config{"web_revalidate_url": "https://x.test/api/revalidate",
			"web_revalidate_secret": "s", "web_revalidate_secret_file": file}, "not both"},
		{"missing secret file", config{"web_revalidate_url": "https://x.test/api/revalidate",
			"web_revalidate_secret_file": filepath.Join(t.TempDir(), "nope")}, "WEB_REVALIDATE_SECRET_FILE"},
		{"relative URL", config{"web_revalidate_url": "/api/revalidate", "web_revalidate_secret": "s"},
			"WEB_REVALIDATE_URL"},
		{"other scheme", config{"web_revalidate_url": "ftp://x.test/r", "web_revalidate_secret": "s"},
			"WEB_REVALIDATE_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := revalidate.FromConfig(tt.cfg.get); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want one naming %q", err, tt.wantErr)
			}
		})
	}
}

// The response body is read (bounded) and quoted in the failure, so the route's reason shows.
func TestFlush_LogsTheRouteReason(t *testing.T) {
	route := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"tag not allowed"}` + strings.Repeat("x", 4096)))
	})
	c, log := newClient(t, route)

	c.Flush(t.Context(), []string{"hr-119-1"})

	if !strings.Contains(log.String(), "tag not allowed") {
		t.Errorf("log:\n%s\nwant the route's error", log)
	}
	if strings.Count(log.String(), "x") > 1024 {
		t.Errorf("log quotes %d bytes of body, want it bounded", log.Len())
	}
}
