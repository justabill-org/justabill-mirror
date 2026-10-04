package main

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"testing"
)

// A Redis that doesn't answer at startup still gets a cache, which caches once Redis is up
// (#457); only a bad URL leaves the API without one.
func TestOpenCache(t *testing.T) {
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	down := "redis://" + ln.Addr().String()
	_ = ln.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	c := openCache(t.Context(), down, logger)
	if c == nil {
		t.Fatal("openCache with Redis down = nil, want a cache")
	}
	_ = c.Close()
	if !strings.Contains(logs.String(), "serving uncached until it answers") {
		t.Errorf("logs = %q, want the unavailable warning", logs.String())
	}

	if c = openCache(t.Context(), "not-a-redis-url", logger); c != nil {
		t.Error("openCache with a bad URL = a cache, want nil")
	}
}

// An empty REDIS_URL means no cache, on purpose: no client, so nothing dials Redis, and no
// warning either (#834).
func TestOpenCacheOff(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	if c := openCache(t.Context(), "", logger); c != nil {
		_ = c.Close()
		t.Fatal("openCache with no URL = a cache, want nil")
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("logs = %q, want no warning", logs.String())
	}
}
