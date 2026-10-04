package apicache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// recordingDel records each DEL's keys and answers as if every key existed, or with err.
type recordingDel struct {
	calls [][]string
	err   error
}

func (r *recordingDel) del(_ context.Context, keys []string) (int64, error) {
	r.calls = append(r.calls, slices.Clone(keys))
	if r.err != nil {
		return 0, r.err
	}
	return int64(len(keys)), nil
}

// logTo returns a logger writing JSON lines to buf.
func logTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestNew_EmptyURLIsOff(t *testing.T) {
	for _, u := range []string{"", "  "} {
		c, err := New(u)
		if err != nil || c != nil {
			t.Errorf("New(%q) = %v, %v; want nil, nil", u, c, err)
		}
	}
	var c *Clearer
	c.Clear(t.Context(), []string{"hr-119-1"}) // nil clearer: no panic
}

func TestNew_BadURLKeepsThePasswordOut(t *testing.T) {
	_, err := New("redis://:hunter2@redis:notaport")
	if err == nil {
		t.Fatal("New succeeded, want an error for the bad port")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q holds the password", err)
	}
	if !strings.HasPrefix(err.Error(), "REDIS_URL: ") {
		t.Errorf("error %q doesn't name REDIS_URL", err)
	}

	if _, err = New("http://redis:6379"); err == nil {
		t.Error("New accepted an http URL")
	}

	// A base64 password's "/" makes the start of the password read as the port (#599).
	_, err = New("redis://:s3cr3t/Pa55+word=@redis:6379/0")
	if err == nil {
		t.Fatal("New succeeded, want an error for the password that breaks the URL")
	}
	for _, part := range []string{"s3cr3t", "Pa55"} {
		if strings.Contains(err.Error(), part) {
			t.Errorf("error %q holds the password part %q", err, part)
		}
	}
}

// config is a viper-style lookup over fixed values.
type config map[string]string

func (c config) get(key string) string { return c[key] }

// urlFile writes content to a file in a temporary directory and returns its path.
func urlFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFromConfig_ReadsRedisURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  config
		on   bool
	}{
		{name: "unset is off", cfg: config{}},
		{name: "REDIS_URL", cfg: config{"redis_url": "redis://localhost:6379/0"}, on: true},
		// The init container writes no trailing newline, but a hand-made file may have one.
		{name: "REDIS_URL_FILE", cfg: config{"redis_url_file": urlFile(t, "redis://:pw@localhost:6379/0\n")}, on: true},
		{name: "empty file is off", cfg: config{"redis_url_file": urlFile(t, "\n")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := FromConfig(tt.cfg.get)
			if err != nil || (c != nil) != tt.on {
				t.Errorf("FromConfig = %v, %v; want on: %v", c, err, tt.on)
			}
		})
	}
}

func TestFromConfig_Errors(t *testing.T) {
	const withPassword = "redis://:hunter2@localhost:6379/0"
	tests := []struct {
		name    string
		cfg     config
		wantErr string
	}{
		{"both forms", config{"redis_url": withPassword, "redis_url_file": urlFile(t, withPassword)},
			"set REDIS_URL or REDIS_URL_FILE, not both"},
		{"missing file", config{"redis_url_file": filepath.Join(t.TempDir(), "nope")}, "REDIS_URL_FILE"},
		{"bad URL in the file", config{"redis_url_file": urlFile(t, "redis://:hunter2@redis:notaport")}, "REDIS_URL: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromConfig(tt.cfg.get)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("error %q holds the password", err)
			}
		})
	}
}

func TestKeys(t *testing.T) {
	got := Keys([]string{"s-119-5", "hr-119-1", "", "s-119-5"})
	want := []string{
		"bills:detail:s-119-5", "bills:law-changes:s-119-5", "bills:detail:hr-119-1", "bills:law-changes:hr-119-1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Keys = %v, want %v", got, want)
	}
}

func TestClear_DeletesEachBillsKey(t *testing.T) {
	var logs bytes.Buffer
	rec := &recordingDel{}
	c := newClearer(rec.del, WithLogger(logTo(&logs)))

	c.Clear(t.Context(), []string{"hr-119-1", "s-119-5"})

	want := [][]string{{
		"bills:detail:hr-119-1", "bills:law-changes:hr-119-1", "bills:detail:s-119-5", "bills:law-changes:s-119-5",
	}}
	if !slices.EqualFunc(rec.calls, want, slices.Equal[[]string]) {
		t.Errorf("DELs = %v, want %v", rec.calls, want)
	}
	if !strings.Contains(logs.String(), `"msg":"api cache cleared","bills":2,"deleted":4`) {
		t.Errorf("logs = %s, want the cleared line", logs.String())
	}
}

func TestClear_NothingToDelete(t *testing.T) {
	rec := &recordingDel{}
	c := newClearer(rec.del)
	c.Clear(t.Context(), nil)
	c.Clear(t.Context(), []string{""})
	if len(rec.calls) != 0 {
		t.Errorf("DELs = %v, want none", rec.calls)
	}
}

// More than revalidate.MaxTags bills still clears every key, in DELs of at most MaxKeysPerDel.
func TestClear_BatchesLargeRuns(t *testing.T) {
	ids := make([]string, MaxKeysPerDel+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("hr-119-%d", i+1)
	}
	rec := &recordingDel{}
	newClearer(rec.del).Clear(t.Context(), ids)

	sizes := make([]int, 0, len(rec.calls))
	var all []string
	for _, keys := range rec.calls {
		sizes = append(sizes, len(keys))
		all = append(all, keys...)
	}
	if want := []int{MaxKeysPerDel, MaxKeysPerDel, keysPerBill}; !slices.Equal(sizes, want) {
		t.Errorf("DEL sizes = %v, want %v", sizes, want)
	}
	if !slices.Equal(all, Keys(ids)) {
		t.Error("the DELs didn't cover every bill's key once")
	}
}

func TestClear_FailureIsLoggedNotReturned(t *testing.T) {
	var logs bytes.Buffer
	rec := &recordingDel{err: errors.New("connection refused")}
	ids := make([]string, MaxKeysPerDel+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("s-119-%d", i+1)
	}
	newClearer(rec.del, WithLogger(logTo(&logs))).Clear(t.Context(), ids)

	if len(rec.calls) != 1 {
		t.Errorf("DELs = %d, want it to stop after the first failure", len(rec.calls))
	}
	line := logs.String()
	for _, want := range []string{`"level":"WARN"`, `"msg":"api cache clear failed"`, `"bills":501`, "connection refused"} {
		if !strings.Contains(line, want) {
			t.Errorf("logs = %s, want %s", line, want)
		}
	}
}

// The real client against a Redis that isn't there: Clear gives up within its timeout and warns.
func TestClear_UnreachableRedis(t *testing.T) {
	var logs bytes.Buffer
	c, err := New("redis://127.0.0.1:1", WithLogger(logTo(&logs)), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	c.Clear(context.WithoutCancel(t.Context()), []string{"hr-119-1"})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Clear took %v, want it bounded by the timeout", elapsed)
	}
	if !strings.Contains(logs.String(), "api cache clear failed") {
		t.Errorf("logs = %s, want the failed line", logs.String())
	}
}

// Against a real Redis when REDIS_URL is set (task infra:up; CI's pipeline job has none).
func TestClear_RealRedis(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping Redis integration test")
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := t.Context()
	if err = rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis at REDIS_URL unreachable: %v", err)
	}

	prefix := fmt.Sprintf("hr-999-%d", time.Now().UnixNano()%100000)
	ids := []string{prefix + "1", prefix + "2"}
	other := "bills:detail:" + prefix + "3"
	for _, key := range append(Keys(ids), other) {
		if err = rdb.Set(ctx, key, "{}", time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = rdb.Del(context.WithoutCancel(ctx), other).Err() })

	c, err := New(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	c.Clear(ctx, ids)

	n, err := rdb.Exists(ctx, Keys(ids)...).Result()
	if err != nil || n != 0 {
		t.Errorf("EXISTS on the cleared keys = %d, %v; want 0", n, err)
	}
	if n, err = rdb.Exists(ctx, other).Result(); err != nil || n != 1 {
		t.Errorf("EXISTS on another bill's key = %d, %v; want it left alone", n, err)
	}
}
