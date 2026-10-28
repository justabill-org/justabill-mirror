package uscode_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/uscode"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// checkUnavailable checks that err reports the site as unavailable, naming the cause.
func checkUnavailable(t *testing.T, err error, cause string) {
	t.Helper()
	if !errors.Is(err, obs.ErrUnavailable) || !errors.Is(err, uscode.ErrSiteUnavailable) {
		t.Fatalf("Run = %v, want ErrSiteUnavailable wrapped in obs.ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), cause) {
		t.Errorf("Run = %q, want it to name %q", err, cause)
	}
}

// checkFailed checks that err is a failure, not the site being unavailable.
func checkFailed(t *testing.T, err error, want error, text string) {
	t.Helper()
	if err == nil || errors.Is(err, obs.ErrUnavailable) || errors.Is(err, uscode.ErrSiteUnavailable) {
		t.Fatalf("Run = %v, want a failure", err)
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("Run = %v, want %v", err, want)
	}
	if !strings.Contains(err.Error(), text) {
		t.Errorf("Run = %q, want it to contain %q", err, text)
	}
}

// TestLoaderRunFindsThePage loads the release point from whichever download page links it: the
// site's temporary format moved it to /download.html (2026-10-12) and answers the old page with a
// "Page Not Found" page; its normal format has it at /download/download.shtml.
func TestLoaderRunFindsThePage(t *testing.T) {
	t.Parallel()
	notFound := readTestdata(t, "notfound.html")
	cases := []struct {
		name     string
		pagePath string
		notFound []byte
	}{
		{name: "temporary format", pagePath: pagePath, notFound: notFound},
		{name: "normal format, download.html a 404", pagePath: legacyPagePath},
		{name: "normal format, download.html not found", pagePath: legacyPagePath, notFound: notFound},
	}
	// Both pages' links resolve to the same zip.
	const wantZip = "/download/releasepoints/us/pl/119/111/xml_uscAll@119-111.zip"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			l, o := newTestLoader(t, store)
			o.set("119-111", releaseFiles(t, "x"))
			o.pagePath, o.notFound = tc.pagePath, tc.notFound
			if err := l.Run(t.Context()); err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
			if len(store.points) != 1 || store.points[0].ReleasePoint != "119-111" ||
				!strings.HasSuffix(store.points[0].SourceURL, wantZip) || o.downloads() != 1 {
				t.Errorf("recorded %+v after %d downloads, want 119-111 from %s", store.points, o.downloads(), wantZip)
			}
		})
	}
}

// TestLoaderRunOutage drives serve's load-uscode job through a nine-day maintenance outage and a
// pod restart: every run is unavailable, until the site is back and the next run loads.
func TestLoaderRunOutage(t *testing.T) {
	t.Parallel()
	maintenance := readTestdata(t, "maintenance.html")
	start := time.Date(2026, 10, 3, 16, 43, 0, 0, time.UTC)
	now := start // Run reads the clock on the test's goroutine.
	clock := uscode.WithClock(func() time.Time { return now })
	store := newFakeStore()
	l, o := newTestLoader(t, store, clock)
	o.set("119-111", releaseFiles(t, "x"))
	o.setMaintenance(maintenance)

	for _, at := range []time.Duration{0, 25 * time.Hour, 9 * 24 * time.Hour} {
		now = start.Add(at)
		checkUnavailable(t, l.Run(t.Context()), "(a maintenance page)")
	}
	// A new Loader, as after a pod restart, has no memory of the outage either way.
	restarted := uscode.NewLoader(store, slog.New(slog.DiscardHandler),
		uscode.WithPageURL(o.url+pagePath, o.url+legacyPagePath), uscode.WithHTTPClient(o.client), clock)
	now = start.Add(9*24*time.Hour + time.Hour)
	checkUnavailable(t, restarted.Run(t.Context()), "(a maintenance page)")

	o.setMaintenance(nil)
	now = start.Add(9*24*time.Hour + 2*time.Hour)
	if err := restarted.Run(t.Context()); err != nil {
		t.Fatalf("Run after the outage = %v, want nil", err)
	}
	if len(store.points) != 1 || !store.points[0].LoadedAt.Equal(now) {
		t.Fatalf("release points = %+v, want 119-111 loaded once, at %v", store.points, now)
	}
}

// TestLoaderRunOutcomes checks which answers from uscode.house.gov make a run unavailable (the
// site is down: it retries) and which make it fail (the site changed, or the bug is ours).
func TestLoaderRunOutcomes(t *testing.T) {
	t.Parallel()
	maintenance := readTestdata(t, "maintenance.html")
	notFound := readTestdata(t, "notfound.html")
	cases := []struct {
		name  string
		setup func(*olrc)
		// cause is in an unavailable run's error; empty for a failure.
		cause string
		want  error
		text  string
	}{
		{name: "page 500", setup: func(o *olrc) { o.pageStatus = 500 }, cause: "(HTTP 500 for "},
		{name: "page 502", setup: func(o *olrc) { o.pageStatus = 502 }, cause: "(HTTP 502 for "},
		{name: "page 503", setup: func(o *olrc) { o.pageStatus = 503 }, cause: "(HTTP 503 for "},
		{name: "page 504", setup: func(o *olrc) { o.pageStatus = 504 }, cause: "(HTTP 504 for "},
		{name: "zip 500", setup: func(o *olrc) { o.zipStatus = 500 }, cause: "(HTTP 500 for "},
		{
			name: "maintenance page in the zip's place", setup: func(o *olrc) { o.zipPage = maintenance },
			cause: "(a maintenance page for ",
		},
		{
			name: "every page not found", setup: func(o *olrc) { o.page, o.notFound = notFound, notFound },
			want: uscode.ErrNoReleasePoint, text: "no all-titles XML link",
		},
		{
			name: "page 403", setup: func(o *olrc) { o.pageStatus = http.StatusForbidden },
			want: uscode.ErrHTTPStatus, text: "403",
		},
		{
			name: "every page 404", setup: func(o *olrc) { o.pageStatus = http.StatusNotFound },
			want: uscode.ErrHTTPStatus, text: "404",
		},
		{
			name: "zip 403", setup: func(o *olrc) { o.zipStatus = http.StatusForbidden },
			want: uscode.ErrHTTPStatus, text: "403",
		},
		{
			name: "another page in the zip's place", setup: func(o *olrc) { o.zipPage = notFound },
			want: uscode.ErrNotZip, text: "an HTML page",
		},
		{
			name: "zip link leaves the host",
			setup: func(o *olrc) {
				o.page = []byte(`<a href="https://uscode.example/download/xml_uscAll@119-111.zip">`)
			},
			want: uscode.ErrNoReleasePoint, text: "leaves",
		},
		{
			name: "zip doesn't parse", setup: func(o *olrc) { o.badZip = true },
			text: "uscode: open zip",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			l, o := newTestLoader(t, store)
			o.set("119-111", releaseFiles(t, "x"))
			tc.setup(o)
			err := l.Run(t.Context())
			if tc.cause != "" {
				checkUnavailable(t, err, tc.cause)
			} else {
				checkFailed(t, err, tc.want, tc.text)
			}
			if len(store.points) != 0 {
				t.Errorf("a run that didn't load recorded %+v", store.points)
			}
		})
	}
}

// TestLoaderRunZipNotPosted is #1134: the download page lists a release point whose zip answers
// HTTP 404 (Public Law 119-136 on 2026-10-28). Each run is unavailable, with nothing recorded,
// until the zip is posted and the next run loads it.
func TestLoaderRunZipNotPosted(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	l, o := newTestLoader(t, store)
	o.set("119-111", releaseFiles(t, "x"))
	o.setZipStatus(http.StatusNotFound)

	for range 2 {
		err := l.Run(t.Context())
		if !errors.Is(err, obs.ErrUnavailable) || !errors.Is(err, uscode.ErrNotPosted) {
			t.Fatalf("Run = %v, want ErrNotPosted wrapped in obs.ErrUnavailable", err)
		}
		if errors.Is(err, uscode.ErrSiteUnavailable) {
			t.Errorf("Run = %v, want it not to say the site is down", err)
		}
		if !strings.Contains(err.Error(), "xml_uscAll@119-111.zip answers HTTP 404") {
			t.Errorf("Run = %q, want it to name the zip and its 404", err)
		}
		if len(store.points) != 0 {
			t.Fatalf("a run with no zip recorded %+v", store.points)
		}
	}

	o.setZipStatus(0)
	if err := l.Run(t.Context()); err != nil {
		t.Fatalf("Run once the zip is posted = %v, want nil", err)
	}
	if len(store.points) != 1 || store.points[0].ReleasePoint != "119-111" || o.downloads() != 3 {
		t.Errorf("recorded %+v after %d downloads, want 119-111 after 3", store.points, o.downloads())
	}
}

// TestLoaderRunTransportErrors checks that a request that never gets an answer is the site being
// unavailable, naming the request and the error, unless the job's own deadline passed.
func TestLoaderRunTransportErrors(t *testing.T) {
	t.Parallel()
	t.Run("connection refused", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		l := uscode.NewLoader(newFakeStore(), slog.New(slog.DiscardHandler), uscode.WithPageURL(srv.URL+pagePath))
		checkUnavailable(t, l.Run(t.Context()), "(get "+srv.URL+pagePath+": ")
	})
	t.Run("client timeout", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) })
		client := srv.Client()
		client.Timeout = 50 * time.Millisecond
		l := uscode.NewLoader(newFakeStore(), slog.New(slog.DiscardHandler), uscode.WithPageURL(srv.URL+pagePath),
			uscode.WithHTTPClient(client))
		checkUnavailable(t, l.Run(t.Context()), "Client.Timeout exceeded")
	})
	t.Run("reset mid-download", func(t *testing.T) {
		t.Parallel()
		l, o := newTestLoader(t, newFakeStore())
		o.set("119-111", releaseFiles(t, "x"))
		o.cutZip = true
		checkUnavailable(t, l.Run(t.Context()), "(download "+o.url+"/download/releasepoints/")
	})
	t.Run("the job's deadline", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) })
		l := uscode.NewLoader(newFakeStore(), slog.New(slog.DiscardHandler), uscode.WithPageURL(srv.URL+pagePath),
			uscode.WithHTTPClient(srv.Client()))
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		err := l.Run(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, obs.ErrUnavailable) {
			t.Fatalf("Run = %v, want the deadline, not unavailable", err)
		}
	})
}

// hijackAndCut sends a response that promises more bytes than it sends, then drops the connection.
func hijackAndCut(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "1000000")
	w.Header().Set("Content-Type", "application/x-zip-compressed")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("PK\x03\x04 truncated"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	if tc, isTCP := conn.(*net.TCPConn); isTCP {
		_ = tc.SetLinger(0)
	}
	_ = conn.Close()
}
