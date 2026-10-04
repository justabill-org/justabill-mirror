package upstream_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/justabill-org/justabill/pipeline/internal/apikey"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

// fetchedAt is the client's clock in the archive tests: 23:30 EDT on 2026-10-02, so the
// objects' date is 2026-10-03, the UTC date.
func fetchedAt() time.Time {
	return time.Date(2026, 10, 2, 23, 30, 0, 0, time.FixedZone("EDT", -4*60*60))
}

// fakeStore records objects, or answers every Put with err. A non-nil gate holds each Put
// until it's closed.
type fakeStore struct {
	mu   sync.Mutex
	objs []upstream.ArchiveObject
	err  error
	gate chan struct{}
}

func (s *fakeStore) Put(ctx context.Context, obj upstream.ArchiveObject) error {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs = append(s.objs, obj)
	return nil
}

func (s *fakeStore) objects() []upstream.ArchiveObject {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]upstream.ArchiveObject(nil), s.objs...)
}

// archivingClient is a client for srv, keyed with testKey, that archives to store.
func archivingClient(
	t *testing.T, log *slog.Logger, srv *server, store upstream.ArchiveStore,
) (*http.Client, *upstream.Archive) {
	t.Helper()
	a := upstream.NewArchive(log, store)
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	keyed := host(fastBudget(t))
	keyed.APIKey = testKey
	c, err := upstream.NewClientWithHooks(log, map[string]upstream.Host{srv.host(): keyed},
		upstream.Hooks{Sleep: (&sleeps{}).sleep, Now: fetchedAt},
		upstream.WithArchive(a))
	if err != nil {
		t.Fatal(err)
	}
	return c, a
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestArchiveWritesResponseWithoutKey(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"bills":[]}`)
	})
	store := &fakeStore{}
	c, a := archivingClient(t, slog.New(slog.DiscardHandler), srv, store)

	callerKey := http.Header{apikey.Header: {"caller-key"}}
	body, err := get(t.Context(), c, srv.URL+"/v3/bill/119/hr?offset=250&api_key=query-key&format=json", callerKey)
	if err != nil || body != `{"bills":[]}` {
		t.Fatalf("got %q, %v", body, err)
	}
	if err = a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	objs := store.objects()
	if len(objs) != 1 {
		t.Fatalf("archived %d objects, want 1", len(objs))
	}
	obj := objs[0]
	if want := srv.host() + "/2026-10-03/v3/bill/119/hr?format=json&offset=250"; obj.Name != want {
		t.Errorf("name = %q, want %q", obj.Name, want)
	}
	if got := gunzip(t, obj.Body); got != `{"bills":[]}` {
		t.Errorf("archived body = %q", got)
	}
	if obj.ContentType != "application/json" {
		t.Errorf("content type = %q", obj.ContentType)
	}
	if obj.Metadata["status"] != "200" || obj.Metadata["method"] != http.MethodGet ||
		obj.Metadata["fetched_at"] != "2026-10-03T03:30:00Z" {
		t.Errorf("metadata = %v", obj.Metadata)
	}
	for _, secret := range []string{testKey, "caller-key", "query-key", "api_key"} {
		if strings.Contains(obj.Name, secret) {
			t.Errorf("name %q contains %q", obj.Name, secret)
		}
		for k, v := range obj.Metadata {
			if strings.Contains(k, secret) || strings.Contains(v, secret) {
				t.Errorf("metadata %s=%q contains %q", k, v, secret)
			}
		}
	}
	if s := a.Stats(); s != (upstream.ArchiveStats{Written: 1}) {
		t.Errorf("stats = %+v", s)
	}
}

// Only the final 2xx of a request is archived: not the failed attempts before it, error
// statuses, or HEAD requests.
func TestArchiveKeepsOnlySuccessfulBodies(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request, hit int) {
		switch {
		case r.URL.Path == "/missing":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/flaky" && hit == 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = io.WriteString(w, "ok")
		}
	})
	store := &fakeStore{}
	c, a := archivingClient(t, slog.New(slog.DiscardHandler), srv, store)

	if _, err := get(t.Context(), c, srv.URL+"/flaky", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := get(t.Context(), c, srv.URL+"/missing", nil); err == nil {
		t.Fatal("404 succeeded")
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodHead, srv.URL+"/head", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if err = a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	objs := store.objects()
	if len(objs) != 1 || !strings.HasSuffix(objs[0].Name, "/2026-10-03/flaky") || gunzip(t, objs[0].Body) != "ok" {
		t.Errorf("archived %+v, want only /flaky's 200", objs)
	}
}

func TestArchiveWriteFailureDoesNotFailRequest(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) { _, _ = io.WriteString(w, "ok") })
	log, lb := newLogger()
	c, a := archivingClient(t, log, srv, &fakeStore{err: errors.New("gcs: 503 backend error")})

	for range 3 {
		if body, err := get(t.Context(), c, srv.URL+"/v3/member?api_key=query-key", nil); err != nil || body != "ok" {
			t.Fatalf("got %q, %v; want ok despite the archive", body, err)
		}
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s := a.Stats(); s != (upstream.ArchiveStats{Failed: 3}) {
		t.Errorf("stats = %+v, want 3 failed", s)
	}
	if n := lb.count("upstream_archive_error"); n != 3 {
		t.Errorf("logged %d upstream_archive_error lines, want 3", n)
	}
	logs := lb.String()
	if !strings.Contains(logs, `"reason":"write_failed"`) || !strings.Contains(logs, "503 backend error") {
		t.Errorf("error log lacks reason or cause: %s", logs)
	}
	if !strings.Contains(logs, `"msg":"upstream_archive_summary"`) || !strings.Contains(logs, `"failed":3`) {
		t.Errorf("no summary with the failure count: %s", logs)
	}
	if strings.Contains(logs, "query-key") || strings.Contains(logs, testKey) {
		t.Errorf("a key reached the log: %s", logs)
	}
}

// The pipeline may create objects, not overwrite them: a second fetch of a URL on the same
// day finds the first one's object, which isn't a failure.
func TestArchiveDuplicateIsNotAFailure(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) { _, _ = io.WriteString(w, "ok") })
	log, lb := newLogger()
	c, a := archivingClient(t, log, srv, &fakeStore{err: upstream.ErrArchiveExists})

	if _, err := get(t.Context(), c, srv.URL+"/v3/bill", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s := a.Stats(); s != (upstream.ArchiveStats{Duplicates: 1}) {
		t.Errorf("stats = %+v, want 1 duplicate", s)
	}
	if n := lb.count("upstream_archive_error"); n != 0 {
		t.Errorf("logged %d errors for a duplicate", n)
	}
}

// A stalled bucket never holds a request: once the queue is full, responses are dropped
// from the archive and counted, and Close gives up at its deadline.
func TestArchiveStalledStoreNeverBlocksRequests(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) { _, _ = io.WriteString(w, "ok") })
	store := &fakeStore{gate: make(chan struct{})}
	c, a := archivingClient(t, slog.New(slog.DiscardHandler), srv, store)

	const requests = 300 // more than the queue and its workers hold
	for i := range requests {
		if _, err := get(t.Context(), c, srv.URL+"/v3/bill?offset="+strconv.Itoa(i), nil); err != nil {
			t.Fatal(err)
		}
	}
	s := a.Stats()
	if s.Dropped == 0 || s.Written != 0 {
		t.Errorf("stats = %+v, want drops and nothing written yet", s)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close = %v, want the deadline", err)
	}
	close(store.gate)
	if err := a.Close(t.Context()); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	s = a.Stats()
	if s.Written+s.Dropped != requests {
		t.Errorf("stats = %+v, want written + dropped = %d", s, requests)
	}

	// After Close, responses still succeed and are counted as dropped.
	if _, err := get(t.Context(), c, srv.URL+"/late", nil); err != nil {
		t.Fatal(err)
	}
	if got := a.Stats().Dropped; got != s.Dropped+1 {
		t.Errorf("dropped = %d after a late response, want %d", got, s.Dropped+1)
	}
}

// CloseWithin gives up on writes still queued after its wait and logs it, since the caller is
// exiting anyway; with nothing queued it closes quietly.
func TestPipelineCloseWithin(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) { _, _ = io.WriteString(w, "ok") })
	log, lb := newLogger()
	store := &fakeStore{gate: make(chan struct{})}
	defer close(store.gate)
	c, a := archivingClient(t, log, srv, store)
	if _, err := get(t.Context(), c, srv.URL+"/v3/bill", nil); err != nil {
		t.Fatal(err)
	}

	(&upstream.Pipeline{Archive: a}).CloseWithin(t.Context(), log, 20*time.Millisecond)
	if n := lb.count("upstream_close"); n != 1 {
		t.Errorf("logged %d upstream_close lines for a stalled write, want 1: %s", n, lb.String())
	}

	(&upstream.Pipeline{}).CloseWithin(t.Context(), log, time.Second)
	if n := lb.count("upstream_close"); n != 1 {
		t.Errorf("logged %d upstream_close lines, want none more without an archive", n)
	}
}

func TestArchivePOSTNameHasBodyHash(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) { _, _ = io.WriteString(w, `{}`) })
	store := &fakeStore{}
	c, a := archivingClient(t, slog.New(slog.DiscardHandler), srv, store)

	for _, q := range []string{`{"query":"a"}`, `{"query":"b"}`} {
		req, err := http.NewRequestWithContext(upstream.WithIdempotent(t.Context()), http.MethodPost,
			srv.URL+"/search", strings.NewReader(q))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	objs := store.objects()
	if len(objs) != 2 || objs[0].Name == objs[1].Name {
		t.Fatalf("objects = %+v, want two distinct names", objs)
	}
	for _, o := range objs {
		if !strings.Contains(o.Name, "/2026-10-03/search.POST-") ||
			!strings.HasPrefix(o.Metadata["request_body"], `{"query":`) {
			t.Errorf("object %q, metadata %v", o.Name, o.Metadata)
		}
	}
}

// errReader fails every Read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

// A POST whose body can't be read again is still archived, named as a POST with an empty body
// and without request_body metadata.
func TestArchiveUnreadableRequestBody(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) { _, _ = io.WriteString(w, `{}`) })
	store := &fakeStore{}
	c, a := archivingClient(t, slog.New(slog.DiscardHandler), srv, store)

	for _, getBody := range []func() (io.ReadCloser, error){
		func() (io.ReadCloser, error) { return nil, errors.New("no body") },
		func() (io.ReadCloser, error) { return io.NopCloser(errReader{}), nil },
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/search",
			strings.NewReader(`{"query":"a"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.GetBody = getBody
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	objs := store.objects()
	if len(objs) != 2 || objs[0].Name != objs[1].Name {
		t.Fatalf("objects = %+v, want two with the same name", objs)
	}
	for _, o := range objs {
		if _, ok := o.Metadata["request_body"]; ok || !strings.Contains(o.Name, "/search.POST-") {
			t.Errorf("object %q, metadata %v", o.Name, o.Metadata)
		}
	}
}

func TestArchiveName(t *testing.T) {
	long := "/v3/" + strings.Repeat("é", 700)
	for _, tc := range []struct {
		name, method, url, body, want string
	}{
		{"sorted query", http.MethodGet, "https://api.congress.gov/v3/bill?offset=0&format=json",
			"", "api.congress.gov/2026-10-03/v3/bill?format=json&offset=0"},
		{"api_key in any case", http.MethodGet, "https://API.congress.gov/v3/bill?API_KEY=x&Api_Key=y&limit=2",
			"", "api.congress.gov/2026-10-03/v3/bill?limit=2"},
		{"no query", http.MethodGet, "https://clerk.house.gov/evs/2026/roll001.xml",
			"", "clerk.house.gov/2026-10-03/evs/2026/roll001.xml"},
		{"only a key", http.MethodGet, "https://api.govinfo.gov/packages/X/mods?api_key=x",
			"", "api.govinfo.gov/2026-10-03/packages/X/mods"},
		{"no path", http.MethodGet, "https://www.senate.gov", "", "www.senate.gov/2026-10-03/"},
		{"user info and fragment", http.MethodGet, "https://u:p@www.congress.gov/a#frag",
			"", "www.congress.gov/2026-10-03/a"},
		{"post", http.MethodPost, "https://api.govinfo.gov/search", "{}",
			"api.govinfo.gov/2026-10-03/search.POST-44136fa355b3678a"},
	} {
		u, err := url.Parse(tc.url)
		if err != nil {
			t.Fatal(err)
		}
		if got := upstream.ArchiveName(u, tc.method, []byte(tc.body), fetchedAt()); got != tc.want {
			t.Errorf("%s: name = %q, want %q", tc.name, got, tc.want)
		}
	}

	u := &url.URL{Scheme: "https", Host: "www.congress.gov", Path: long}
	got := upstream.ArchiveName(u, http.MethodGet, nil, fetchedAt())
	if len(got) > 1024 || !utf8.ValidString(got) || !strings.Contains(got, "~") {
		t.Errorf("long name: %d bytes, valid UTF-8 %v: %q", len(got), utf8.ValidString(got), got)
	}
	u.Path += "x"
	if again := upstream.ArchiveName(u, http.MethodGet, nil, fetchedAt()); again == got {
		t.Error("two long names that differ only past the cut are the same")
	}
}
