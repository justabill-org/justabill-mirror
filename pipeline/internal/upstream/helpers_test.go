package upstream_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

const testKey = "test-key-123"

// logBuf collects JSON log lines from the client.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// count returns how many lines have the given message.
func (l *logBuf) count(msg string) int {
	return strings.Count(l.String(), `"msg":"`+msg+`"`)
}

func newLogger() (*slog.Logger, *logBuf) {
	lb := &logBuf{}
	return slog.New(slog.NewJSONHandler(lb, &slog.HandlerOptions{Level: slog.LevelDebug})), lb
}

// sleeps records the waits the client asks for and returns at once.
type sleeps struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *sleeps) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waits = append(s.waits, d)
	return ctx.Err()
}

func (s *sleeps) got() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

// server is an httptest server whose handler also gets the 1-based hit number.
type server struct {
	*httptest.Server

	hits atomic.Int32
}

func newServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, hit int)) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, int(s.hits.Add(1)))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) host() string {
	u, err := url.Parse(s.URL)
	if err != nil {
		panic(err)
	}
	return u.Host
}

func (s *server) count() int { return int(s.hits.Load()) }

func newBudget(t *testing.T, rps float64, burst, reservePct int) *upstream.Budget {
	t.Helper()
	b, err := upstream.NewBudget("test", rps, burst, reservePct)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fastBudget effectively doesn't pace, so tests measure retries rather than the rate.
func fastBudget(t *testing.T) *upstream.Budget {
	t.Helper()
	return newBudget(t, 1000, 100, 0)
}

func host(b *upstream.Budget) upstream.Host {
	return upstream.Host{Budget: b, AttemptTimeout: 2 * time.Second, MaxBodyBytes: 1 << 20}
}

func newClient(
	t *testing.T, log *slog.Logger, hosts map[string]upstream.Host, hooks upstream.Hooks,
) *http.Client {
	t.Helper()
	c, err := upstream.NewClientWithHooks(log, hosts, hooks)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// get sends a GET and returns the body text.
func get(ctx context.Context, c *http.Client, target string, header http.Header) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	maps.Copy(req.Header, header)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// statusThen answers hits 1..len(statuses) with those statuses and later hits with 200 "ok".
func statusThen(statuses ...int) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, _ *http.Request, hit int) {
		if hit <= len(statuses) {
			w.WriteHeader(statuses[hit-1])
			return
		}
		_, _ = io.WriteString(w, "ok")
	}
}
