package uscode_test

import (
	"archive/zip"
	"bytes"
	"cmp"
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
	mu      sync.Mutex
	rp      string
	files   map[string]string
	zipGets int
	// pagePath is where the download page is; legacyPagePath when empty.
	pagePath   string
	pageStatus int
	// page replaces the download page's body.
	page []byte
	// maintenance serves the site's maintenance page, with status 200, in place of every page.
	maintenance []byte
	zipStatus   int
	// zipPage is served as text/html, with status 200, in place of the zip.
	zipPage []byte
	// badZip serves bytes that aren't a zip; cutZip drops the connection partway through it.
	badZip, cutZip bool
	// notFound is served, with status 200, for any other path, as the site's "Page Not Found"
	// page; a 404 when nil.
	notFound []byte
	// url and client reach the server.
	url    string
	client *http.Client
}

const (
	pagePath       = "/download.html"
	legacyPagePath = "/download/download.shtml"
)

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
	switch {
	case o.maintenance != nil && !strings.HasSuffix(r.URL.Path, ".zip"):
		_, _ = w.Write(o.maintenance)
	case r.URL.Path == cmp.Or(o.pagePath, legacyPagePath):
		o.servePage(w, congress, law)
	case r.URL.Path == zipPath:
		o.serveZip(w)
	case o.notFound != nil:
		_, _ = w.Write(o.notFound)
	default:
		http.NotFound(w, r)
	}
}

func (o *olrc) servePage(w http.ResponseWriter, congress, law string) {
	switch {
	case o.pageStatus != 0:
		w.WriteHeader(o.pageStatus)
	case o.page != nil:
		_, _ = w.Write(o.page)
	default:
		// The page's links are relative to it, as on the site: the legacy page's to /download/.
		prefix := "download/releasepoints"
		if cmp.Or(o.pagePath, legacyPagePath) == legacyPagePath {
			prefix = "releasepoints"
		}
		_, _ = fmt.Fprintf(w, `<h3 class="releasepointinformation">Public Law %s (09/18/2026)</h3>`+
			`<a href="%s/us/pl/%s/%s/xml_uscAll@%s.zip" title="All USC Titles in XML">[XML]</a>`,
			o.rp, prefix, congress, law, o.rp)
	}
}

func (o *olrc) serveZip(w http.ResponseWriter) {
	o.zipGets++
	switch {
	case o.zipStatus != 0:
		w.WriteHeader(o.zipStatus)
		return
	case o.zipPage != nil:
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(o.zipPage)
		return
	case o.cutZip:
		hijackAndCut(w)
		return
	case o.badZip:
		w.Header().Set("Content-Type", "application/x-zip-compressed")
		_, _ = w.Write([]byte("not a zip"))
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range o.files {
		f, _ := zw.Create(name)
		_, _ = f.Write([]byte(body))
	}
	_ = zw.Close()
	w.Header().Set("Content-Type", "application/x-zip-compressed")
	_, _ = w.Write(buf.Bytes())
}

func (o *olrc) setZipStatus(status int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.zipStatus = status
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
	o.url, o.client = srv.URL, srv.Client()
	opts = append([]uscode.Option{
		uscode.WithPageURL(srv.URL+pagePath, srv.URL+legacyPagePath),
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
			setup: func(o *olrc, _ *fakeStore) { o.pageStatus = http.StatusNotFound },
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

// TestLoaderMissingZip loads a release point whose zip answers HTTP 404: [uscode.ErrNotPosted]
// (#1134), and nothing is recorded.
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
		uscode.WithPageURL(srv.URL+legacyPagePath), uscode.WithHTTPClient(srv.Client()))
	if _, err := l.Load(t.Context(), false); !errors.Is(err, uscode.ErrNotPosted) {
		t.Fatalf("err = %v, want ErrNotPosted", err)
	}
	if len(store.points) != 0 {
		t.Errorf("a load with no zip recorded %+v", store.points)
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
