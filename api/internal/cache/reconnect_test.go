package cache_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/cache"
)

const testBackoff = time.Minute

// closedAddr returns a local address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// A cache made while Redis is down is still a cache, and while a recent command failed it
// answers ErrUnavailable without dialing (#457).
func TestNew_RedisDown(t *testing.T) {
	c, err := cache.New("redis://" + closedAddr(t))
	if err != nil {
		t.Fatalf("New with Redis down = %v, want a cache", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	clk := &clock{t: time.Unix(1_000, 0)}
	c.SetClock(clk.now, testBackoff)
	ctx := t.Context()

	if _, err = c.Get(ctx, "k"); err == nil || errors.Is(err, cache.ErrUnavailable) {
		t.Fatalf("first Get = %v, want the dial error", err)
	}
	if _, err = c.Get(ctx, "k"); !errors.Is(err, cache.ErrUnavailable) {
		t.Errorf("Get during back-off = %v, want ErrUnavailable", err)
	}
	if err = c.Set(ctx, "k", "v", testTTL); !errors.Is(err, cache.ErrUnavailable) {
		t.Errorf("Set during back-off = %v, want ErrUnavailable", err)
	}
	if err = c.Delete(ctx, "k"); !errors.Is(err, cache.ErrUnavailable) {
		t.Errorf("Delete during back-off = %v, want ErrUnavailable", err)
	}
	if err = c.Ping(ctx); err == nil || errors.Is(err, cache.ErrUnavailable) {
		t.Errorf("Ping during back-off = %v, want the dial error", err)
	}

	clk.advance(testBackoff)
	if _, err = c.Get(ctx, "k"); err == nil || errors.Is(err, cache.ErrUnavailable) {
		t.Errorf("Get after back-off = %v, want a new dial error", err)
	}
}

// A request whose context ended says nothing about Redis, so it doesn't start a back-off.
func TestCache_CanceledContextNoBackoff(t *testing.T) {
	c, err := cache.New("redis://" + closedAddr(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = c.Get(canceled, "k"); err == nil {
		t.Fatal("Get with a canceled context = nil, want an error")
	}
	if _, err = c.Get(t.Context(), "k"); errors.Is(err, cache.ErrUnavailable) {
		t.Errorf("Get after a canceled request = %v, want a dial attempt", err)
	}
}

// proxy forwards connections on a fixed address to target once started.
func proxy(t *testing.T, addr, target string) {
	t.Helper()
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })
	wg.Go(func() {
		for {
			in, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			out, dialErr := new(net.Dialer).DialContext(t.Context(), "tcp", target)
			if dialErr != nil {
				_ = in.Close()
				continue
			}
			t.Cleanup(func() { _ = in.Close(); _ = out.Close() })
			wg.Go(func() { _, _ = io.Copy(out, in); _ = out.Close() })
			wg.Go(func() { _, _ = io.Copy(in, out); _ = in.Close() })
		}
	})
}

// Given Redis unreachable when the cache is made and reachable later, the same cache serves
// hits without a restart (#457).
func TestCache_RedisComesUp(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping Redis integration test")
	}
	u, err := url.Parse(redisURL)
	if err != nil {
		t.Fatalf("parse REDIS_URL: %v", err)
	}
	target := u.Host
	addr := closedAddr(t)
	u.Host = addr

	c, err := cache.New(u.String())
	if err != nil {
		t.Fatalf("New with Redis down = %v, want a cache", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	clk := &clock{t: time.Unix(1_000, 0)}
	c.SetClock(clk.now, testBackoff)
	ctx := t.Context()
	if err = c.Ping(ctx); err == nil {
		t.Fatal("Ping before Redis is up = nil, want an error")
	}

	proxy(t, addr, target)
	// The readiness probe's Ping sees Redis first and ends the back-off.
	if err = c.Ping(ctx); err != nil {
		t.Fatalf("Ping after Redis is up: %v", err)
	}
	key := testKey(t, "up")
	if err = c.Set(ctx, key, "hit", testTTL); err != nil {
		t.Fatalf("Set after Redis is up: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, key) })
	if val, getErr := c.Get(ctx, key); getErr != nil || val != "hit" {
		t.Errorf("Get after Redis is up = %q, %v; want hit", val, getErr)
	}
}
