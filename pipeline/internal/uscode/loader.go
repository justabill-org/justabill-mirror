package uscode

import (
	"archive/zip"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
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
)

// titleFilePattern matches a title's file in the zip, usc42.xml, and not an appendix's
// (usc05A.xml, usc11a.xml).
var titleFilePattern = regexp.MustCompile(`^usc(\d{2})\.xml$`)

// Errors from a load.
var (
	ErrHTTPStatus = errors.New("uscode: unexpected HTTP status")
	ErrTooLarge   = errors.New("uscode: download exceeds the size cap")
	ErrWrongTitle = errors.New("uscode: title file holds another title")
	// ErrNotZip means the zip link served an HTML page other than the maintenance page.
	ErrNotZip = errors.New("uscode: the zip link served an HTML page")
	// ErrNotPosted means the download page lists a release point whose zip answers HTTP 404. The
	// Law Revision Counsel can list one before its files are up (Public Law 119-136 on 2026-10-28,
	// #1134), so [Loader.Run] reports it as unavailable and the hourly retry loads it once posted.
	// A link that changed for good looks the same; the US Code stale alert catches either after a
	// day without a load.
	ErrNotPosted = errors.New("uscode: the release point's zip isn't posted")

	// errHTTPNotFound is [ErrHTTPStatus] for HTTP 404, which a download tells apart.
	errHTTPNotFound = fmt.Errorf("%w: %d", ErrHTTPStatus, http.StatusNotFound)
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
	pageURLs  []string
	now       func() time.Time
	batchRows int
}

// Option configures a [Loader].
type Option func(*Loader)

// WithHTTPClient replaces the HTTP client, which by default times out each request after 30
// minutes.
func WithHTTPClient(c *http.Client) Option {
	return func(l *Loader) { l.client = c }
}

// WithPageURL replaces the pages the release point is found on, [DefaultPageURL] then
// [LegacyPageURL], with urls, tried in order.
func WithPageURL(urls ...string) Option {
	return func(l *Loader) { l.pageURLs = urls }
}

// WithBatchRows sets how many changed sections are written at a time (default 500).
func WithBatchRows(n int) Option {
	return func(l *Loader) { l.batchRows = max(n, 1) }
}

// WithClock replaces the wall clock, which stamps loaded release points.
func WithClock(now func() time.Time) Option {
	return func(l *Loader) { l.now = now }
}

// NewLoader returns a Loader that writes to store and logs to log.
func NewLoader(store Store, log *slog.Logger, opts ...Option) *Loader {
	l := &Loader{
		store:     store,
		log:       log,
		client:    &http.Client{Timeout: httpTimeout},
		pageURLs:  []string{DefaultPageURL, LegacyPageURL},
		now:       time.Now,
		batchRows: defaultBatchRows,
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

// Run is serve's load-uscode job: [Loader.Load] without force. While uscode.house.gov is down
// (its maintenance page, HTTP 5xx, or no answer: [ErrSiteUnavailable]), or while the release
// point it lists has no zip yet ([ErrNotPosted]), Run wraps the error in [obs.ErrUnavailable], for
// as long as that lasts, so the run is reported unavailable, not failed: the job retries, and the
// Pipeline stale alert tracks it. Anything else, such as a changed page or a zip that doesn't
// parse, fails.
func (l *Loader) Run(ctx context.Context) error {
	_, err := l.Load(ctx, false)
	if errors.Is(err, ErrSiteUnavailable) || errors.Is(err, ErrNotPosted) {
		return fmt.Errorf("%w: %w", obs.ErrUnavailable, err)
	}
	return err
}

// findReleasePoint reads each download page in turn and returns the first release point found.
// If none has one, the error joins each page's.
func (l *Loader) findReleasePoint(ctx context.Context) (ReleasePoint, error) {
	errs := make([]error, 0, len(l.pageURLs))
	for _, pageURL := range l.pageURLs {
		rp, err := l.readPage(ctx, pageURL)
		if err == nil {
			return rp, nil
		}
		if ctx.Err() != nil {
			return ReleasePoint{}, err
		}
		errs = append(errs, err)
	}
	return ReleasePoint{}, errors.Join(errs...)
}

func (l *Loader) readPage(ctx context.Context, rawURL string) (ReleasePoint, error) {
	pageURL, err := url.Parse(rawURL)
	if err != nil {
		return ReleasePoint{}, fmt.Errorf("uscode: page URL: %w", err)
	}
	body, err := l.get(ctx, rawURL)
	if err != nil {
		return ReleasePoint{}, err
	}
	defer func() { _ = body.Close() }()
	page, err := io.ReadAll(io.LimitReader(body, maxPageBytes+1))
	if err != nil {
		return ReleasePoint{}, l.readError(ctx, "read", rawURL, err)
	}
	if len(page) > maxPageBytes {
		return ReleasePoint{}, fmt.Errorf("%w: %s", ErrTooLarge, rawURL)
	}
	return FindReleasePoint(page, pageURL)
}

// download streams the zip at zipURL to a temporary file in [os.TempDir] and returns it open,
// with its size. The caller closes and removes it. A zip that answers HTTP 404 is [ErrNotPosted].
func (l *Loader) download(ctx context.Context, zipURL string) (*os.File, int64, error) {
	resp, err := l.do(ctx, zipURL)
	if errors.Is(err, errHTTPNotFound) {
		return nil, 0, fmt.Errorf("%w: %s answers HTTP 404 (listed before its files are up, or the link changed)",
			ErrNotPosted, zipURL)
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mediaType == "text/html" {
		return nil, 0, l.htmlInPlaceOfZip(ctx, resp.Body, zipURL)
	}
	f, err := os.CreateTemp("", "uscode-*.zip")
	if err != nil {
		return nil, 0, fmt.Errorf("uscode: temp file: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxZipBytes+1))
	if err == nil && n > maxZipBytes {
		err = fmt.Errorf("%w: %s", ErrTooLarge, zipURL)
	} else if err != nil {
		err = l.readError(ctx, "download", zipURL, err)
	}
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, 0, err
	}
	l.log.InfoContext(ctx, "us code release point downloaded", "url", zipURL, "bytes", n)
	return f, n, nil
}

// htmlInPlaceOfZip is the error for an HTML page served at zipURL: the site being unavailable if
// it's the maintenance page, else [ErrNotZip].
func (l *Loader) htmlInPlaceOfZip(ctx context.Context, body io.Reader, zipURL string) error {
	page, err := io.ReadAll(io.LimitReader(body, maxPageBytes))
	if err != nil {
		return l.readError(ctx, "download", zipURL, err)
	}
	if maintenanceTitlePattern.Match(page) {
		return fmt.Errorf("%w (a maintenance page for %s)", ErrSiteUnavailable, zipURL)
	}
	return fmt.Errorf("%w: %s", ErrNotZip, zipURL)
}

// get requests rawURL and returns the body of a 200 answer.
func (l *Loader) get(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	resp, err := l.do(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// do requests rawURL and returns a 200 answer. An answer of HTTP 5xx, or none, is
// [ErrSiteUnavailable], unless ctx ended; any other status is [ErrHTTPStatus] (a 404 as
// errHTTPNotFound).
func (l *Loader) do(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("uscode: request %s: %w", rawURL, err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := l.client.Do(req)
	if err != nil {
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err // its text repeats the method and the URL
		}
		return nil, l.readError(ctx, "get", rawURL, err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return resp, nil
	case resp.StatusCode >= http.StatusInternalServerError:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w (HTTP %d for %s)", ErrSiteUnavailable, resp.StatusCode, rawURL)
	case resp.StatusCode == http.StatusNotFound:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w for %s", errHTTPNotFound, rawURL)
	default:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %d for %s", ErrHTTPStatus, resp.StatusCode, rawURL)
	}
}

// readError is the error for a request to rawURL that failed in op ("get", "read" or "download")
// before it had all of its answer: the site being unavailable, unless ctx ended, as when the job's
// deadline passes.
func (*Loader) readError(ctx context.Context, op, rawURL string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("uscode: %s %s: %w", op, rawURL, err)
	}
	return fmt.Errorf("%w (%s %s: %w)", ErrSiteUnavailable, op, rawURL, err)
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
