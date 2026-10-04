package uscode

import (
	"archive/zip"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs"
)

const (
	// userAgent names the loader to uscode.house.gov.
	userAgent = "justabill-pipeline/1.0 (+https://github.com/justabill-org/justabill)"
	// maxPageBytes caps the download page, about 84 KB in September 2026.
	maxPageBytes = 2 << 20
	// maxZipBytes caps the all-titles zip, about 109 MB in September 2026.
	maxZipBytes = 1 << 30
	// maxTitleBytes caps one unzipped title file; title 42, the largest, is about 114 MB.
	maxTitleBytes = 1 << 30
	// httpTimeout bounds each request, the zip's download included.
	httpTimeout = 30 * time.Minute
	// defaultBatchRows is how many changed sections go to the store at a time.
	defaultBatchRows = 500
	// DefaultOutageGrace is how long [Loader.Run] reports a maintenance page as unavailable
	// before it reports it as a failure.
	DefaultOutageGrace = 24 * time.Hour
)

// titleFilePattern matches a title's file in the zip, usc42.xml, and not an appendix's
// (usc05A.xml, usc11a.xml).
var titleFilePattern = regexp.MustCompile(`^usc(\d{2})\.xml$`)

// Errors from a load.
var (
	ErrHTTPStatus = errors.New("uscode: unexpected HTTP status")
	ErrTooLarge   = errors.New("uscode: download exceeds the size cap")
	ErrWrongTitle = errors.New("uscode: title file holds another title")
)

// Store is the part of [repository.PipelineStore] the loader writes through.
type Store interface {
	CurrentUSCReleasePoint(ctx context.Context) (*model.USCReleasePoint, error)
	USCSectionHashes(ctx context.Context, title int) (map[string]string, error)
	UpsertUSCSections(ctx context.Context, rows []repository.USCSectionRow) error
	RecordUSCReleasePoint(ctx context.Context, rp repository.USCReleasePointRow) error
}

// Result is what a load did.
type Result struct {
	ReleasePoint string
	// Skipped is true when the release point was loaded already, so nothing was downloaded.
	Skipped bool
	Titles  int
	// Sections counts every section in the release point, changed or not.
	Sections int
	// Written counts the sections whose content changed, which were written.
	Written int
	// Stale counts stored sections the release point no longer has. They are left in place.
	Stale int
}

// Loader loads the current US Code release point into the store.
type Loader struct {
	store     Store
	log       *slog.Logger
	client    *http.Client
	pageURL   string
	now       func() time.Time
	batchRows int
	grace     time.Duration

	mu sync.Mutex
	// downSince is when [Loader.Run] first found the site down in the current outage; zero
	// while it's up.
	downSince time.Time
}

// Option configures a [Loader].
type Option func(*Loader)

// WithHTTPClient replaces the HTTP client, which by default times out each request after 30
// minutes.
func WithHTTPClient(c *http.Client) Option {
	return func(l *Loader) { l.client = c }
}

// WithPageURL replaces [DefaultPageURL], the page the release point is found on.
func WithPageURL(u string) Option {
	return func(l *Loader) { l.pageURL = u }
}

// WithBatchRows sets how many changed sections are written at a time (default 500).
func WithBatchRows(n int) Option {
	return func(l *Loader) { l.batchRows = max(n, 1) }
}

// WithOutageGrace sets how long [Loader.Run] reports the site as unavailable before it reports
// a failure (default [DefaultOutageGrace]).
func WithOutageGrace(d time.Duration) Option {
	return func(l *Loader) { l.grace = d }
}

// WithClock replaces the wall clock, which times outages and stamps loaded release points.
func WithClock(now func() time.Time) Option {
	return func(l *Loader) { l.now = now }
}

// NewLoader returns a Loader that writes to store and logs to log.
func NewLoader(store Store, log *slog.Logger, opts ...Option) *Loader {
	l := &Loader{
		store:     store,
		log:       log,
		client:    &http.Client{Timeout: httpTimeout},
		pageURL:   DefaultPageURL,
		now:       time.Now,
		batchRows: defaultBatchRows,
		grace:     DefaultOutageGrace,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Load finds the current release point and, unless it is the one loaded last (or force is
// set), downloads the all-titles zip to a temporary file, parses every title, writes the
// sections whose content hash changed and then records the release point with its section
// count. The release point is recorded only after every title loaded, so an interrupted load
// runs again next time, and rewrites nothing that already landed.
func (l *Loader) Load(ctx context.Context, force bool) (Result, error) {
	rp, err := l.findReleasePoint(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{ReleasePoint: rp.ID}
	current, err := l.store.CurrentUSCReleasePoint(ctx)
	if err != nil {
		return res, fmt.Errorf("uscode: current release point: %w", err)
	}
	if current != nil && current.ReleasePoint == rp.ID && !force {
		res.Skipped = true
		l.log.InfoContext(ctx, "us code release point already loaded", "release_point", rp.ID)
		return res, nil
	}

	f, size, err := l.download(ctx, rp.ZipURL)
	if err != nil {
		return res, err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	if err = l.loadZip(ctx, f, size, rp.ID, &res); err != nil {
		return res, err
	}

	err = l.store.RecordUSCReleasePoint(ctx, repository.USCReleasePointRow{
		ReleasePoint:  rp.ID,
		PublishedDate: rp.LawDate,
		SourceURL:     rp.ZipURL,
		LoadedAt:      l.now().UTC(),
		SectionCount:  res.Sections,
	})
	if err != nil {
		return res, fmt.Errorf("uscode: record release point: %w", err)
	}
	l.log.InfoContext(ctx, "us code release point loaded", "release_point", rp.ID, "titles", res.Titles,
		"sections", res.Sections, "written", res.Written, "stale", res.Stale)
	return res, nil
}

// Run is serve's load-uscode job: [Loader.Load] without force. While uscode.house.gov serves its
// maintenance page, Run wraps [ErrSiteUnavailable] in [obs.ErrUnavailable], so the run is
// reported unavailable, not failed, and the failed-job alert stays quiet; once the outage has
// lasted the grace period (counted from the first such run this Loader saw), it returns the
// error alone, so the alert fires. Any other result ends the outage.
func (l *Loader) Run(ctx context.Context) error {
	_, err := l.Load(ctx, false)
	l.mu.Lock()
	defer l.mu.Unlock()
	if !errors.Is(err, ErrSiteUnavailable) {
		l.downSince = time.Time{}
		return err
	}
	now := l.now()
	if l.downSince.IsZero() {
		l.downSince = now
	}
	if now.Sub(l.downSince) < l.grace {
		return fmt.Errorf("%w: %w", obs.ErrUnavailable, err)
	}
	return fmt.Errorf("%w since %s", err, l.downSince.UTC().Format(time.RFC3339))
}

func (l *Loader) findReleasePoint(ctx context.Context) (ReleasePoint, error) {
	pageURL, err := url.Parse(l.pageURL)
	if err != nil {
		return ReleasePoint{}, fmt.Errorf("uscode: page URL: %w", err)
	}
	body, err := l.get(ctx, l.pageURL)
	if err != nil {
		return ReleasePoint{}, err
	}
	defer func() { _ = body.Close() }()
	page, err := io.ReadAll(io.LimitReader(body, maxPageBytes+1))
	if err != nil {
		return ReleasePoint{}, fmt.Errorf("uscode: read %s: %w", l.pageURL, err)
	}
	if len(page) > maxPageBytes {
		return ReleasePoint{}, fmt.Errorf("%w: %s", ErrTooLarge, l.pageURL)
	}
	return FindReleasePoint(page, pageURL)
}

// download streams the zip at zipURL to a temporary file in [os.TempDir] and returns it open,
// with its size. The caller closes and removes it.
func (l *Loader) download(ctx context.Context, zipURL string) (*os.File, int64, error) {
	body, err := l.get(ctx, zipURL)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = body.Close() }()
	f, err := os.CreateTemp("", "uscode-*.zip")
	if err != nil {
		return nil, 0, fmt.Errorf("uscode: temp file: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(body, maxZipBytes+1))
	if err == nil && n > maxZipBytes {
		err = fmt.Errorf("%w: %s", ErrTooLarge, zipURL)
	}
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, 0, fmt.Errorf("uscode: download %s: %w", zipURL, err)
	}
	l.log.InfoContext(ctx, "us code release point downloaded", "url", zipURL, "bytes", n)
	return f, n, nil
}

func (l *Loader) get(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("uscode: request %s: %w", rawURL, err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("uscode: get %s: %w", rawURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %d for %s", ErrHTTPStatus, resp.StatusCode, rawURL)
	}
	return resp.Body, nil
}

type titleFile struct {
	number int
	file   *zip.File
}

// loadZip loads each title file in the zip, in title order. Appendices are left out: their
// sections ("/us/usc/t5a/…") have no title number, and bills rarely cite them.
func (l *Loader) loadZip(ctx context.Context, r io.ReaderAt, size int64, rp string, res *Result) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("uscode: open zip: %w", err)
	}
	var titles []titleFile
	for _, f := range zr.File {
		m := titleFilePattern.FindStringSubmatch(f.Name)
		if m == nil {
			l.log.DebugContext(ctx, "us code zip entry skipped", "name", f.Name)
			continue
		}
		n, _ := strconv.Atoi(m[1]) // two digits, so it parses
		titles = append(titles, titleFile{number: n, file: f})
	}
	slices.SortFunc(titles, func(a, b titleFile) int { return cmp.Compare(a.number, b.number) })
	for _, t := range titles {
		if t.file.UncompressedSize64 > maxTitleBytes {
			return fmt.Errorf("%w: %s is %d bytes", ErrTooLarge, t.file.Name, t.file.UncompressedSize64)
		}
		if err = l.loadTitleFile(ctx, t, rp, res); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loader) loadTitleFile(ctx context.Context, t titleFile, rp string, res *Result) error {
	rc, err := t.file.Open()
	if err != nil {
		return fmt.Errorf("uscode: open %s: %w", t.file.Name, err)
	}
	defer func() { _ = rc.Close() }()
	if err = l.LoadTitle(ctx, io.LimitReader(rc, maxTitleBytes), t.number, rp, res); err != nil {
		return fmt.Errorf("uscode: %s: %w", t.file.Name, err)
	}
	return nil
}

// LoadTitle parses one title file from r and writes its sections whose content hash differs
// from the stored one, adding its counts to res. title is the title the file must hold.
func (l *Loader) LoadTitle(ctx context.Context, r io.Reader, title int, rp string, res *Result) error {
	hashes, err := l.store.USCSectionHashes(ctx, title)
	if err != nil {
		return fmt.Errorf("stored hashes: %w", err)
	}
	tl := titleLoad{l: l, title: title, rp: rp, hashes: hashes, seen: make(map[string]struct{}, len(hashes))}
	meta, err := ParseTitle(r, func(s Section) error { return tl.add(ctx, s) })
	if err == nil {
		err = tl.flush(ctx)
	}
	if err != nil {
		return err
	}
	if meta.Number != title {
		return fmt.Errorf("%w: docNumber %d, want %d", ErrWrongTitle, meta.Number, title)
	}
	stale := 0
	for id := range hashes {
		if _, ok := tl.seen[id]; !ok {
			stale++
		}
	}
	res.Titles++
	res.Sections += len(tl.seen)
	res.Written += tl.written
	res.Stale += stale
	l.log.DebugContext(ctx, "us code title loaded", "title", title, "sections", len(tl.seen),
		"written", tl.written, "stale", stale, "duplicates", tl.duplicates)
	return nil
}

// titleLoad is one title's load in progress.
type titleLoad struct {
	l          *Loader
	title      int
	rp         string
	hashes     map[string]string // stored content hash by section ID
	seen       map[string]struct{}
	batch      []repository.USCSectionRow
	written    int
	duplicates int
}

// add queues s to be written if its content changed, and writes a full batch.
func (tl *titleLoad) add(ctx context.Context, s Section) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Title != tl.title {
		return fmt.Errorf("%w: section %s in title %d's file", ErrWrongTitle, s.ID, tl.title)
	}
	if _, dup := tl.seen[s.ID]; dup {
		tl.duplicates++
		return nil
	}
	tl.seen[s.ID] = struct{}{}
	hash := s.Hash()
	if tl.hashes[s.ID] == hash {
		return nil
	}
	tl.batch = append(tl.batch, sectionRow(s, tl.rp, hash))
	if len(tl.batch) >= tl.l.batchRows {
		return tl.flush(ctx)
	}
	return nil
}

func (tl *titleLoad) flush(ctx context.Context) error {
	if len(tl.batch) == 0 {
		return nil
	}
	if err := tl.l.store.UpsertUSCSections(ctx, tl.batch); err != nil {
		return fmt.Errorf("write sections: %w", err)
	}
	tl.written += len(tl.batch)
	tl.batch = nil
	return nil
}

func sectionRow(s Section, rp, hash string) repository.USCSectionRow {
	row := repository.USCSectionRow{
		SectionID:     s.ID,
		TitleNumber:   s.Title,
		SectionNumber: s.Number,
		Text:          s.Text,
		Status:        s.Status,
		PositiveLaw:   s.PositiveLaw,
		ReleasePoint:  rp,
		ContentHash:   hash,
	}
	if s.Heading != "" {
		row.Heading = &s.Heading
	}
	return row
}
