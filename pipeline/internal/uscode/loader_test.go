package uscode_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/uscode"
)

// fakeStore keeps sections and release points in memory and counts writes.
type fakeStore struct {
	mu        sync.Mutex
	sections  map[string]repository.USCSectionRow
	points    []repository.USCReleasePointRow
	upserts   int
	written   []string
	upsertErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{sections: map[string]repository.USCSectionRow{}}
}

func (s *fakeStore) CurrentUSCReleasePoint(context.Context) (*model.USCReleasePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.points) == 0 {
		return nil, nil //nolint:nilnil // matches spannerdb: no release point loaded yet
	}
	p := s.points[len(s.points)-1]
	return &model.USCReleasePoint{ReleasePoint: p.ReleasePoint, SourceURL: p.SourceURL}, nil
}

func (s *fakeStore) USCSectionHashes(_ context.Context, title int) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for id, r := range s.sections {
		if r.TitleNumber == title {
			out[id] = r.ContentHash
		}
	}
	return out, nil
}

func (s *fakeStore) UpsertUSCSections(_ context.Context, rows []repository.USCSectionRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.upserts++
	for _, r := range rows {
		s.sections[r.SectionID] = r
		s.written = append(s.written, r.SectionID)
	}
	return nil
}

func (s *fakeStore) RecordUSCReleasePoint(_ context.Context, rp repository.USCReleasePointRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.points = append(s.points, rp)
	return nil
}

// olrc serves a download page and the zip it links, and counts zip downloads.
type olrc struct {
	mu         sync.Mutex
	rp         string
	files      map[string]string
	zipGets    int
	pageStatus int
	// maintenance serves the site's maintenance page, with status 200, in place of the page.
	maintenance []byte
}

func (o *olrc) set(rp string, files map[string]string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rp, o.files = rp, files
}

func (o *olrc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	congress, law, _ := strings.Cut(o.rp, "-")
	zipPath := fmt.Sprintf("/download/releasepoints/us/pl/%s/%s/xml_uscAll@%s.zip", congress, law, o.rp)
	switch r.URL.Path {
	case "/download/download.shtml":
		if o.pageStatus != 0 {
			w.WriteHeader(o.pageStatus)
			return
		}
		if o.maintenance != nil {
			_, _ = w.Write(o.maintenance)
			return
		}
		_, _ = fmt.Fprintf(w, `<h3 class="releasepointinformation">Public Law %s (09/18/2026)</h3>`+
			`<a href="releasepoints/us/pl/%s/%s/xml_uscAll@%s.zip" title="All USC Titles in XML">[XML]</a>`,
			o.rp, congress, law, o.rp)
	case zipPath:
		o.zipGets++
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, body := range o.files {
			f, _ := zw.Create(name)
			_, _ = f.Write([]byte(body))
		}
		_ = zw.Close()
		_, _ = w.Write(buf.Bytes())
	default:
		http.NotFound(w, r)
	}
}

func (o *olrc) setMaintenance(page []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.maintenance = page
}

func (o *olrc) downloads() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.zipGets
}

const title1 = `<uscDoc xmlns="http://xml.house.gov/schemas/uslm/1.0"><meta><docNumber>1</docNumber>
<property role="is-positive-law">yes</property></meta><main><title identifier="/us/usc/t1">
<section identifier="/us/usc/t1/s1"><num>§ 1.</num><heading> Words</heading><content>%s</content></section>
<section identifier="/us/usc/t1/s2"><num>§ 2.</num><heading> County</heading><content>Parish.</content></section>
</title></main></uscDoc>`

func releaseFiles(t *testing.T, s1 string) map[string]string {
	t.Helper()
	sample, err := os.ReadFile("testdata/usc42-sample.xml")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"usc01.xml":  fmt.Sprintf(title1, s1),
		"usc42.xml":  string(sample),
		"usc05A.xml": `<uscDoc><meta><docNumber>5a</docNumber></meta></uscDoc>`,
		"readme.txt": "not a title",
	}
}

func newTestLoader(t *testing.T, store *fakeStore, opts ...uscode.Option) (*uscode.Loader, *olrc) {
	t.Helper()
	o := &olrc{}
	srv := httptest.NewServer(o)
	t.Cleanup(srv.Close)
	opts = append([]uscode.Option{
		uscode.WithPageURL(srv.URL + "/download/download.shtml"),
		uscode.WithHTTPClient(srv.Client()),
	}, opts...)
	return uscode.NewLoader(store, slog.New(slog.DiscardHandler), opts...), o
}

func TestLoaderLoadsReleasePoints(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	l, o := newTestLoader(t, store)
	o.set("119-111", releaseFiles(t, "Singular."))
	ctx := t.Context()

	// First load: every section is new.
	res, err := l.Load(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	want := uscode.Result{ReleasePoint: "119-111", Titles: 2, Sections: 6, Written: 6}
	if res != want {
		t.Errorf("first load = %+v, want %+v", res, want)
	}
	checkFirstLoad(t, store)

	// Same release point: nothing downloaded, nothing written.
	before := store.upserts
	res, err = l.Load(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || o.downloads() != 1 || store.upserts != before || len(store.points) != 1 {
		t.Errorf("unchanged release point: %+v, %d downloads, %d upserts, %d points",
			res, o.downloads(), store.upserts-before, len(store.points))
	}

	// A new release point that changes one section: only that one is written.
	o.set("119-112", releaseFiles(t, "Plural."))
	store.written = nil
	res, err = l.Load(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	want = uscode.Result{ReleasePoint: "119-112", Titles: 2, Sections: 6, Written: 1}
	if res != want || len(store.written) != 1 || store.written[0] != "/us/usc/t1/s1" {
		t.Errorf("changed release point = %+v, wrote %v; want %+v writing /us/usc/t1/s1", res, store.written, want)
	}
	if got := store.sections["/us/usc/t1/s1"]; got.Text != "Plural." || got.ReleasePoint != "119-112" {
		t.Errorf("changed section = %+v", got)
	}
	if got := store.sections["/us/usc/t1/s2"]; got.ReleasePoint != "119-111" {
		t.Errorf("unchanged section moved to release point %s", got.ReleasePoint)
	}
	if last := store.points[len(store.points)-1]; last.ReleasePoint != "119-112" || last.SectionCount != 6 {
		t.Errorf("recorded %+v", last)
	}
}

// checkFirstLoad checks the rows a first load of 119-111 wrote.
func checkFirstLoad(t *testing.T, store *fakeStore) {
	t.Helper()
	if len(store.points) != 1 {
		t.Fatalf("recorded %d release points, want 1", len(store.points))
	}
	p := store.points[0]
	if p.ReleasePoint != "119-111" || p.SectionCount != 6 || p.LoadedAt.IsZero() ||
		!strings.HasSuffix(p.SourceURL, "/download/releasepoints/us/pl/119/111/xml_uscAll@119-111.zip") ||
		p.PublishedDate == nil || !p.PublishedDate.Equal(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("recorded %+v", p)
	}
	s1 := store.sections["/us/usc/t1/s1"]
	if s1.Heading == nil || *s1.Heading != "Words" || s1.Text != "Singular." || !s1.PositiveLaw ||
		s1.Status != uscode.StatusCurrent || s1.ReleasePoint != "119-111" || s1.TitleNumber != 1 ||
		s1.SectionNumber != "1" || len(s1.ContentHash) != 64 {
		t.Errorf("section 1 row = %+v", s1)
	}
	if _, ok := store.sections["/us/usc/t42/s217a-1"]; !ok {
		t.Errorf("sections %v lack /us/usc/t42/s217a-1", slices.Sorted(maps.Keys(store.sections)))
	}
}

func TestLoaderForceAndStale(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.sections["/us/usc/t1/s99"] = repository.USCSectionRow{SectionID: "/us/usc/t1/s99", TitleNumber: 1}
	store.points = []repository.USCReleasePointRow{{ReleasePoint: "119-111"}}
	l, o := newTestLoader(t, store, uscode.WithBatchRows(1))
	o.set("119-111", releaseFiles(t, "Singular."))

	res, err := l.Load(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || res.Written != 6 || res.Stale != 1 || o.downloads() != 1 {
		t.Errorf("forced load = %+v after %d downloads", res, o.downloads())
	}
	if store.upserts != 6 {
		t.Errorf("%d upserts with one row per batch, want 6", store.upserts)
	}
	if _, ok := store.sections["/us/usc/t1/s99"]; !ok {
		t.Error("stale section was deleted; the loader leaves it")
	}

	// Forced again with nothing changed: downloaded and parsed, nothing written.
	res, err = l.Load(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 0 || res.Sections != 6 || o.downloads() != 2 {
		t.Errorf("second forced load = %+v after %d downloads", res, o.downloads())
	}
}

func TestLoaderErrors(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("spanner down")
	cases := []struct {
		name  string
		setup func(*olrc, *fakeStore)
		want  error
	}{
		{
			name:  "page status",
			setup: func(o *olrc, _ *fakeStore) { o.pageStatus = http.StatusServiceUnavailable },
			want:  uscode.ErrHTTPStatus,
		},
		{
			name: "title file holds another title",
			setup: func(o *olrc, _ *fakeStore) {
				files := releaseFiles(t, "x")
				files["usc02.xml"] = files["usc01.xml"]
				delete(files, "usc01.xml")
				o.files = files
			},
			want: uscode.ErrWrongTitle,
		},
		{
			name: "empty title file",
			setup: func(o *olrc, _ *fakeStore) {
				o.files = map[string]string{"usc03.xml": `<uscDoc><meta><docNumber>4</docNumber></meta></uscDoc>`}
			},
			want: uscode.ErrWrongTitle,
		},
		{
			name:  "store error",
			setup: func(_ *olrc, s *fakeStore) { s.upsertErr = storeErr },
			want:  storeErr,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			l, o := newTestLoader(t, store)
			o.set("119-111", releaseFiles(t, "x"))
			tc.setup(o, store)
			_, err := l.Load(t.Context(), false)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(store.points) != 0 {
				t.Errorf("a failed load recorded %+v", store.points)
			}
		})
	}
}

func TestLoaderMissingZip(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	o := &olrc{}
	o.set("119-111", nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".zip") {
			http.NotFound(w, r)
			return
		}
		o.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	l := uscode.NewLoader(store, slog.New(slog.DiscardHandler),
		uscode.WithPageURL(srv.URL+"/download/download.shtml"), uscode.WithHTTPClient(srv.Client()))
	if _, err := l.Load(t.Context(), false); !errors.Is(err, uscode.ErrHTTPStatus) {
		t.Fatalf("err = %v, want ErrHTTPStatus", err)
	}
}

func TestLoaderCanceled(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	l, o := newTestLoader(t, store)
	o.set("119-111", releaseFiles(t, "x"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := l.Load(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// runWant is what a [uscode.Loader.Run] should return.
type runWant int

const (
	runOK runWant = iota
	runUnavailable
	runFailed
)

// checkRun checks Run's error at a step of an outage that started at 2026-10-03T16:43:00Z.
func checkRun(t *testing.T, at time.Duration, err error, want runWant) {
	t.Helper()
	switch want {
	case runOK:
		if err != nil {
			t.Fatalf("at +%v: Run = %v, want nil", at, err)
		}
	case runUnavailable:
		if !errors.Is(err, obs.ErrUnavailable) || !errors.Is(err, uscode.ErrSiteUnavailable) {
			t.Fatalf("at +%v: Run = %v, want ErrSiteUnavailable wrapped in obs.ErrUnavailable", at, err)
		}
	case runFailed:
		if errors.Is(err, obs.ErrUnavailable) || !errors.Is(err, uscode.ErrSiteUnavailable) {
			t.Fatalf("at +%v: Run = %v, want ErrSiteUnavailable alone", at, err)
		}
		if !strings.Contains(err.Error(), "since 2026-10-03T16:43:00Z") {
			t.Errorf("at +%v: Run = %q, want the outage's start", at, err)
		}
	}
}

// TestLoaderRunOutage drives serve's load-uscode job through a maintenance outage: unavailable
// for the grace period from the first run that found the site down, then a failure, until a run
// gets through.
func TestLoaderRunOutage(t *testing.T) {
	t.Parallel()
	maintenance, err := os.ReadFile("testdata/maintenance.html")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 3, 16, 43, 0, 0, time.UTC)
	now := start // Run reads the clock on the test's goroutine.
	store := newFakeStore()
	l, o := newTestLoader(t, store, uscode.WithClock(func() time.Time { return now }))
	o.set("119-111", releaseFiles(t, "x"))

	steps := []struct {
		at   time.Duration
		down bool
		want runWant
	}{
		{at: 0, down: true, want: runUnavailable},
		{at: time.Hour, down: true, want: runUnavailable},
		{at: 23*time.Hour + 59*time.Minute, down: true, want: runUnavailable},
		{at: 24 * time.Hour, down: true, want: runFailed},
		{at: 25 * time.Hour, down: true, want: runFailed},
		{at: 26 * time.Hour, down: false, want: runOK},
		// A new outage gets a new grace period.
		{at: 50 * time.Hour, down: true, want: runUnavailable},
		{at: 73 * time.Hour, down: true, want: runUnavailable},
	}
	for _, step := range steps {
		now = start.Add(step.at)
		page := maintenance
		if !step.down {
			page = nil
		}
		o.setMaintenance(page)
		checkRun(t, step.at, l.Run(t.Context()), step.want)
	}
	if len(store.points) != 1 || store.points[0].ReleasePoint != "119-111" {
		t.Fatalf("release points = %+v, want 119-111 loaded once, when the site was up", store.points)
	}
	if got := store.points[0].LoadedAt; !got.Equal(start.Add(26 * time.Hour)) {
		t.Errorf("LoadedAt = %v, want the clock's time", got)
	}
}

// TestLoaderRunOtherErrorsFail checks that only a maintenance page counts as unavailable: a
// failed page request fails at once, and ends an outage.
func TestLoaderRunOtherErrorsFail(t *testing.T) {
	t.Parallel()
	maintenance, err := os.ReadFile("testdata/maintenance.html")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 3, 16, 43, 0, 0, time.UTC)
	now := start // Run reads the clock on the test's goroutine.
	l, o := newTestLoader(t, newFakeStore(), uscode.WithOutageGrace(time.Hour),
		uscode.WithClock(func() time.Time { return now }))
	o.set("119-111", releaseFiles(t, "x"))

	o.setMaintenance(maintenance)
	if err = l.Run(t.Context()); !errors.Is(err, obs.ErrUnavailable) {
		t.Fatalf("Run = %v, want unavailable", err)
	}
	o.setMaintenance(nil)
	o.mu.Lock()
	o.pageStatus = http.StatusBadGateway
	o.mu.Unlock()
	now = start.Add(30 * time.Minute)
	err = l.Run(t.Context())
	if errors.Is(err, obs.ErrUnavailable) || !errors.Is(err, uscode.ErrHTTPStatus) {
		t.Fatalf("Run = %v, want ErrHTTPStatus alone", err)
	}

	// Two hours after the first maintenance page, past the grace, but in a new outage.
	o.mu.Lock()
	o.pageStatus = 0
	o.mu.Unlock()
	o.setMaintenance(maintenance)
	now = start.Add(2 * time.Hour)
	if err = l.Run(t.Context()); !errors.Is(err, obs.ErrUnavailable) {
		t.Fatalf("Run = %v, want unavailable: the failed request ended the earlier outage", err)
	}
}
