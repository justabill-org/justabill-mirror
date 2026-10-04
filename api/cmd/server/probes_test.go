package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/api/internal/health"
)

// okPinger is a Spanner that always answers.
type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

// countingHandler counts the log records it handles.
type countingHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (c *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (c *countingHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, r.Message)
	return nil
}

func (c *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return c }

func (c *countingHandler) WithGroup(string) slog.Handler { return c }

func (c *countingHandler) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

// TestProbesSkipTheMiddleware checks /healthz, /readyz and /health answer
// ahead of the router: never rate limited, no CORS headers, not in the access
// log.
func TestProbesSkipTheMiddleware(t *testing.T) {
	logs := &countingHandler{}
	log := slog.New(logs)
	h := handler.New(nil)
	h.SetLogger(log)
	probes := health.New(slog.New(slog.DiscardHandler), okPinger{})
	srv := probes.Wrap(buildRouter(h, log, nil, devEdge(t, 0)))

	send := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "198.51.100.1:5000"
		req.Header.Set("Origin", "http://localhost:3000")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}

	for _, path := range []string{"/healthz", "/readyz", "/health"} {
		for i := range publicRateLimit + 5 {
			rr := send(path)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s request %d = %d, want 200", path, i+1, rr.Code)
			}
			if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Fatalf("%s has Access-Control-Allow-Origin %q, want none", path, got)
			}
		}
	}
	if n := logs.count(); n != 0 {
		t.Errorf("probes wrote %d log records (%v), want none", n, logs.msgs)
	}

	// The same client's API requests still go through the stack.
	if rr := send("/api/v1/not-a-route"); rr.Code != http.StatusNotFound ||
		rr.Header().Get("Access-Control-Allow-Origin") == "" || logs.count() == 0 {
		t.Errorf("API request = %d, CORS %q, %d log records; want a logged 404 with CORS",
			rr.Code, rr.Header().Get("Access-Control-Allow-Origin"), logs.count())
	}
}

func TestShutdownDrain(t *testing.T) {
	tests := []struct {
		value   string
		set     bool
		want    time.Duration
		wantErr bool
	}{
		{set: false, want: 0},
		{value: " ", set: true, want: 0},
		{value: "0s", set: true, want: 0},
		{value: "15s", set: true, want: 15 * time.Second},
		{value: "15", set: true, wantErr: true},
		{value: "-1s", set: true, wantErr: true},
		{value: "later", set: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q set=%v", tt.value, tt.set), func(t *testing.T) {
			t.Setenv("SHUTDOWN_DRAIN", tt.value)
			if !tt.set {
				_ = os.Unsetenv("SHUTDOWN_DRAIN")
			}
			viper.Reset()
			viper.AutomaticEnv()
			t.Cleanup(viper.Reset)

			got, err := serverTimings()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "SHUTDOWN_DRAIN") {
					t.Fatalf("serverTimings() = %v, %v; want an error naming SHUTDOWN_DRAIN", got, err)
				}
				return
			}
			if err != nil || got.drain != tt.want || got.idle != defaultIdleTimeout {
				t.Fatalf("serverTimings() = %+v, %v; want drain %v", got, err, tt.want)
			}
		})
	}
}

// TestServeDrainsBeforeShutdown is the SIGTERM sequence: once ctx is done,
// /readyz answers 503 for the drain while the server keeps serving, then the
// server shuts down.
func TestServeDrainsBeforeShutdown(t *testing.T) {
	const drain = 600 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	probes := health.New(slog.New(slog.DiscardHandler), okPinger{})
	srv := newServer("", probes.Wrap(http.NotFoundHandler()), defaultIdleTimeout)
	ctx, signal := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln, probes, drain, slog.New(slog.DiscardHandler)) }()

	url := "http://" + ln.Addr().String() + "/readyz"
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	readyz := func() int {
		resp, getErr := client.Get(url)
		if getErr != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if code := readyz(); code != http.StatusOK {
		t.Fatalf("/readyz before the signal = %d, want 200", code)
	}
	signaled := time.Now()
	signal()
	// serve sees ctx.Done() on its own goroutine, so poll until it drains: a
	// single request right after signal() can win the race and get 200.
	draining := readyz()
	for draining == http.StatusOK && time.Since(signaled) < drain/2 {
		time.Sleep(5 * time.Millisecond)
		draining = readyz()
	}
	if draining != http.StatusServiceUnavailable {
		t.Errorf("/readyz while draining = %d, want 503", draining)
	}
	if err = <-done; err != nil {
		t.Fatalf("serve = %v", err)
	}
	if took := time.Since(signaled); took < drain {
		t.Errorf("shut down %v after the signal, want at least the %v drain", took, drain)
	}
	if code := readyz(); code != 0 {
		t.Errorf("/readyz after shutdown = %d, want no answer", code)
	}
}
