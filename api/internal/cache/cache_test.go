package cache_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/obs/obstest"
)

const (
	testTTL   = time.Minute
	shortTTL  = 100 * time.Millisecond
	expiryMax = 2 * time.Second
)

// newTestCache connects to the Redis at REDIS_URL and skips the test when it isn't set.
func newTestCache(t *testing.T) *cache.Cache {
	t.Helper()

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping Redis integration test")
	}

	c, err := cache.New(redisURL)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	return c
}

// testKey returns a key unique to this test run, so tests can share a Redis instance.
func testKey(t *testing.T, name string) string {
	t.Helper()
	return fmt.Sprintf("test:%s:%d:%s", t.Name(), time.Now().UnixNano(), name)
}

func TestNew_InvalidURL(t *testing.T) {
	if _, err := cache.New("not-a-redis-url"); err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

// A password with a "/" (base64) breaks the URL, and the error mustn't carry the password into
// the logs (#599).
func TestNew_InvalidURLHidesPassword(t *testing.T) {
	const password = "s3cr3t/Pa55+word="
	_, err := cache.New("redis://:" + password + "@redis.app.svc.cluster.local:6379/0")
	if !errors.Is(err, cache.ErrBadURL) {
		t.Fatalf("New() error = %v, want ErrBadURL", err)
	}
	for _, part := range []string{password, "s3cr3t", "Pa55"} {
		if strings.Contains(err.Error(), part) {
			t.Fatalf("error %q contains the password part %q", err, part)
		}
	}
}

func TestCache_GetMissingKey(t *testing.T) {
	c := newTestCache(t)

	val, err := c.Get(t.Context(), testKey(t, "missing"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "" {
		t.Errorf("Get = %q, want empty string", val)
	}
}

func TestCache_SetGetDelete(t *testing.T) {
	c := newTestCache(t)
	ctx := t.Context()
	k1, k2 := testKey(t, "a"), testKey(t, "b")

	if err := c.Set(ctx, k1, `{"bill":"hr-1"}`, testTTL); err != nil {
		t.Fatalf("Set k1: %v", err)
	}
	if err := c.Set(ctx, k2, "two", testTTL); err != nil {
		t.Fatalf("Set k2: %v", err)
	}

	val, err := c.Get(ctx, k1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != `{"bill":"hr-1"}` {
		t.Errorf("Get = %q, want %q", val, `{"bill":"hr-1"}`)
	}

	if err = c.Delete(ctx, k1, k2); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, k := range []string{k1, k2} {
		if val, err = c.Get(ctx, k); err != nil || val != "" {
			t.Errorf("Get(%s) after Delete = %q, %v; want empty, nil", k, val, err)
		}
	}
}

func TestCache_SetExpires(t *testing.T) {
	c := newTestCache(t)
	ctx := t.Context()
	key := testKey(t, "ttl")

	if err := c.Set(ctx, key, "soon gone", shortTTL); err != nil {
		t.Fatalf("Set: %v", err)
	}

	deadline := time.Now().Add(expiryMax)
	for time.Now().Before(deadline) {
		val, err := c.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if val == "" {
			return
		}
		time.Sleep(shortTTL)
	}
	t.Fatalf("key %s did not expire within %s", key, expiryMax)
}

// Redis commands are CLIENT spans under the caller's span, without the
// command text: it would hold cache keys and cached responses.
func TestCache_Traces(t *testing.T) {
	tel := obstest.New(t)
	c := newTestCache(t) // after obstest.New: the hooks take the tracer provider when they're added

	ctx, parent := otel.Tracer("test").Start(t.Context(), "request")
	if err := c.Set(ctx, testKey(t, "traced"), "cached-body", testTTL); err != nil {
		t.Fatalf("Set: %v", err)
	}
	parent.End()

	var sets int
	for _, s := range tel.Ended() {
		if s.Name() != "set" {
			continue
		}
		sets++
		if s.SpanKind() != trace.SpanKindClient || s.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Errorf("span %q (%v), want a client span under the request", s.Name(), s.SpanKind())
		}
		for _, kv := range s.Attributes() {
			if kv.Key == "db.statement" || kv.Key == "db.query.text" ||
				strings.Contains(kv.Value.String(), "cached-body") {
				t.Errorf("recorded %s = %q", kv.Key, kv.Value.String())
			}
		}
	}
	if sets != 1 {
		names := []string{}
		for _, s := range tel.Ended() {
			names = append(names, s.Name())
		}
		t.Errorf("got %d set spans, want 1; spans: %v", sets, names)
	}
}

func TestCache_PingAfterClose(t *testing.T) {
	c := newTestCache(t)

	if err := c.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	_ = c.Close()
	if err := c.Ping(t.Context()); err == nil {
		t.Error("Ping after Close = nil, want an error")
	}
}
