package sync

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
)

// syncStateStore records the govinfo watermark writes. sync_retry is fakeRetries; any other
// PipelineStore method panics through its nil embedded interface.
type syncStateStore struct {
	fakeRetries

	since    time.Time
	stateErr error
	upserted []repository.SyncRun
	errors   []string
}

func (f *syncStateStore) GetSyncState(context.Context, string, int) (*repository.SyncStateRow, error) {
	return &repository.SyncStateRow{Step: "govinfo", LastSyncedAt: f.since}, f.stateErr
}

func (f *syncStateStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.upserted = append(f.upserted, run)
	return nil
}

func (f *syncStateStore) RecordSyncFailure(_ context.Context, run repository.SyncRun) error {
	f.errors = append(f.errors, run.Error)
	return nil
}

// govinfoPages serves a first page with two packages and a nextPage. The second page has
// one package, or fails with a 500 when failSecond is set.
func govinfoPages(failSecond bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offsetMark") == "*" {
			fmt.Fprintf(w, `{"packages": [{"packageId": "BILLS-118hr1ih"}, {"packageId": "BILLS-118hr2ih"}],
				"nextPage": "http://%s%s?offsetMark=two&pageSize=1000"}`, r.Host, r.URL.Path)
			return
		}
		if failSecond {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"packages": [{"packageId": "BILLS-118s3is"}]}`)
	}
}

func serviceWithGovInfo(t *testing.T, store repository.PipelineStore, handler http.HandlerFunc) *Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Service{
		store:   store,
		govinfo: govinfo.NewClientWithBaseURL(srv.Client(), srv.URL),
		logger:  slog.New(slog.DiscardHandler),
	}
}

// The packages are from the 118th Congress and the sync is for the 119th, so each one is
// counted and skipped without fetching its summary.
func TestSyncGovInfoChanges_ReadsEveryPage(t *testing.T) {
	store := &syncStateStore{since: time.Now().Add(-time.Hour)}
	s := serviceWithGovInfo(t, store, govinfoPages(false))

	if err := s.SyncGovInfoChanges(context.Background(), 119); err != nil {
		t.Fatal(err)
	}
	if len(store.upserted) != 1 || store.upserted[0].ItemsSynced != 3 {
		t.Errorf("sync state writes = %+v, want one with ItemsSynced 3", store.upserted)
	}
}

func TestSyncGovInfoChanges_PageFailureKeepsWatermark(t *testing.T) {
	store := &syncStateStore{since: time.Now().Add(-time.Hour)}
	s := serviceWithGovInfo(t, store, govinfoPages(true))

	err := s.SyncGovInfoChanges(context.Background(), 119)
	if err == nil {
		t.Fatal("want an error when page 2 fails")
	}
	if len(store.upserted) != 0 {
		t.Errorf("sync state moved: %+v", store.upserted)
	}
	if len(store.errors) != 1 || !strings.Contains(store.errors[0], "collection page 2") {
		t.Errorf("recorded errors = %q, want one naming page 2", store.errors)
	}
}

// warnCounter counts log records at warn level and above.
type warnCounter struct{ n atomic.Int32 }

func (c *warnCounter) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }
func (c *warnCounter) Handle(context.Context, slog.Record) error    { c.n.Add(1); return nil }
func (c *warnCounter) WithAttrs([]slog.Attr) slog.Handler           { return c }
func (c *warnCounter) WithGroup(string) slog.Handler                { return c }

// A run cancelled mid-loop (timeout or shutdown) stops at the next package instead of
// attempting every remaining one, and records the cancellation as a failure.
func TestSyncGovInfoChanges_StopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var summaries atomic.Int32
	store := &syncStateStore{since: time.Now().Add(-time.Hour)}
	s := serviceWithGovInfo(t, store, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/summary") {
			summaries.Add(1)
			cancel()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"packages": [{"packageId": "BILLS-119hr1ih"}, {"packageId": "BILLS-119hr2ih"},
			{"packageId": "BILLS-119hr3ih"}]}`)
	})
	warns := &warnCounter{}
	s.logger = slog.New(warns)

	if err := s.SyncGovInfoChanges(ctx, 119); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := summaries.Load(); got != 1 {
		t.Errorf("summary requests = %d, want 1", got)
	}
	if got := warns.n.Load(); got != 1 {
		t.Errorf("warnings = %d, want 1 (the first package only)", got)
	}
	if len(store.upserted) != 0 || len(store.errors) != 1 {
		t.Errorf("sync state: successes %+v, failures %q; want one failure", store.upserted, store.errors)
	}
}

// govinfoPackageStore is the store a GovInfo package's text goes through: one unfetched version,
// the bill_texts rows and the law references written. Any other PipelineStore method panics.
type govinfoPackageStore struct {
	repository.PipelineStore

	stored    []repository.BillTextRow
	lawRefs   map[string][]repository.BillLawRefRow // by version ID
	refsErr   error
	findErr   error // FindUnfetchedVersion
	insertErr error // InsertBillText
}

func (f *govinfoPackageStore) FindUnfetchedVersion(_ context.Context, billID, versionCode string) (string, error) {
	if f.findErr != nil {
		return "", f.findErr
	}
	return billID + "-" + versionCode, nil
}

func (f *govinfoPackageStore) InsertBillText(_ context.Context, t repository.BillTextRow) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.stored = append(f.stored, t)
	return nil
}

func (f *govinfoPackageStore) ReplaceBillLawRefs(
	_ context.Context, _, versionID string, rows []repository.BillLawRefRow,
) error {
	if f.refsErr != nil {
		return f.refsErr
	}
	if f.lawRefs == nil {
		f.lawRefs = map[string][]repository.BillLawRefRow{}
	}
	f.lawRefs[versionID] = rows
	return nil
}

// govinfoPackage serves BILLS-119hr1ih: a summary whose xmlLink is the text, the text, and the
// package's MODS.
func govinfoPackage(text string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/packages/BILLS-119hr1ih/summary":
			fmt.Fprintf(w, `{"packageId": "BILLS-119hr1ih",
				"download": {"xmlLink": "http://%s/packages/BILLS-119hr1ih/xml"}}`, r.Host)
		case "/packages/BILLS-119hr1ih/xml":
			fmt.Fprint(w, text)
		case "/packages/BILLS-119hr1ih/mods":
			fmt.Fprint(w, lawRefsMODS)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// A text sync-govinfo stores gets its row from newTextRow and its law references, the MODS
// supplement included, exactly as sync-texts writes them (#575).
func TestProcessGovInfoPackage_StoresTextAndLawRefs(t *testing.T) {
	store := &govinfoPackageStore{}
	s := serviceWithGovInfo(t, store, govinfoPackage(lawRefsXML))

	if err := s.processGovInfoPackage(t.Context(), "BILLS-119hr1ih", 119); err != nil {
		t.Fatal(err)
	}
	if len(store.stored) != 1 {
		t.Fatalf("stored %d texts, want 1", len(store.stored))
	}
	row := store.stored[0]
	if row.TextVersionID != "hr-119-1-ih" || row.Format != formatXML || len(row.ContentGz) == 0 ||
		row.Content != lawRefsXML {
		t.Errorf("stored row = %q %q, %d gzipped bytes, content %q; want newTextRow's",
			row.TextVersionID, row.Format, len(row.ContentGz), row.Content)
	}
	want := []string{"/us/usc/t15/s9401 cites", "/us/usc/t5/s8103 amends"}
	if got := sectionKinds(store.lawRefs["hr-119-1-ih"]); !reflect.DeepEqual(got, want) {
		t.Errorf("law references = %v, want %v", got, want)
	}
}

// A text too large for a cell is marked fetched with no text, and no law references, as in
// sync-texts.
func TestProcessGovInfoPackage_TooLargeText(t *testing.T) {
	store := &govinfoPackageStore{}
	noise := make([]byte, repository.MaxCellBytes+1)
	_, _ = rand.Read(noise) // gzip can't shrink it below a cell
	s := serviceWithGovInfo(t, store, govinfoPackage(string(noise)))

	if err := s.processGovInfoPackage(t.Context(), "BILLS-119hr1ih", 119); err != nil {
		t.Fatal(err)
	}
	if len(store.stored) != 1 || store.stored[0].ContentGz != nil || store.stored[0].Content != "" {
		t.Fatalf("stored = %d rows, want one with no text", len(store.stored))
	}
	if store.lawRefs != nil {
		t.Errorf("law references written for a text not stored: %v", store.lawRefs)
	}
}

// A failed law-reference write leaves the text stored and logs a warning.
func TestProcessGovInfoPackage_LawRefsFailureKeepsText(t *testing.T) {
	store := &govinfoPackageStore{refsErr: errFakeStore}
	s := serviceWithGovInfo(t, store, govinfoPackage(lawRefsXML))
	warns := &warnCounter{}
	s.logger = slog.New(warns)

	if err := s.processGovInfoPackage(t.Context(), "BILLS-119hr1ih", 119); err != nil {
		t.Fatalf("err = %v, want the text stored", err)
	}
	if len(store.stored) != 1 {
		t.Errorf("stored %d texts, want 1", len(store.stored))
	}
	if got := warns.n.Load(); got != 1 {
		t.Errorf("warnings = %d, want 1", got)
	}
}

func TestParseGovInfoBillID(t *testing.T) {
	tests := []struct {
		id       string
		ok       bool
		congress int
		billType string
		number   int
		version  string
	}{
		{id: "BILLS-119hr144ih", ok: true, congress: 119, billType: "hr", number: 144, version: "ih"},
		{id: "BILLS-118sjres7enr", ok: true, congress: 118, billType: "sjres", number: 7, version: "enr"},
		{id: "PLAW-119publ1"},
		{id: "BILLS-119hr144"},
		{id: "BILLS-99999999999999999999hr1ih"},
		{id: "BILLS-119hr99999999999999999999ih"},
	}
	for _, tt := range tests {
		congress, billType, number, version, ok := parseGovInfoBillID(tt.id)
		if ok != tt.ok || congress != tt.congress || billType != tt.billType || number != tt.number ||
			version != tt.version {
			t.Errorf("parseGovInfoBillID(%q) = %d, %q, %d, %q, %v; want %d, %q, %d, %q, %v", tt.id,
				congress, billType, number, version, ok, tt.congress, tt.billType, tt.number, tt.version, tt.ok)
		}
	}
}

func TestSyncGovInfoChanges_WithoutClientSkips(t *testing.T) {
	s := &Service{logger: slog.New(slog.DiscardHandler)} // a nil store panics if the step runs
	if err := s.SyncGovInfoChanges(t.Context(), 119); err != nil {
		t.Fatal(err)
	}
}

// Before the first success the poll looks back 24 hours; after it, from the watermark.
func TestSyncGovInfoChanges_PollsFromTheWatermark(t *testing.T) {
	watermark := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		since time.Time
		want  time.Time
	}{
		{name: "never synced", want: time.Now().Add(-defaultLookbackHours * time.Hour)},
		{name: "synced before", since: watermark, want: watermark},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var polled time.Time
			s := serviceWithGovInfo(t, &syncStateStore{since: tt.since}, func(w http.ResponseWriter, r *http.Request) {
				var err error
				if polled, err = time.Parse(
					time.RFC3339,
					strings.TrimPrefix(r.URL.Path, "/collections/BILLS/"),
				); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				fmt.Fprint(w, `{"packages": []}`)
			})
			if err := s.SyncGovInfoChanges(t.Context(), 119); err != nil {
				t.Fatal(err)
			}
			if d := polled.Sub(tt.want).Abs(); d > time.Minute {
				t.Errorf("polled from %v, want %v", polled, tt.want)
			}
		})
	}
}

func TestSyncGovInfoChanges_StoreErrorsFailTheStep(t *testing.T) {
	tests := []struct {
		name    string
		store   *syncStateStore
		wantErr string
	}{
		{name: "watermark", store: &syncStateStore{stateErr: errFakeStore}, wantErr: "get govinfo last sync"},
		{name: "due retries", store: &syncStateStore{dueErr: errFakeStore},
			wantErr: errFakeStore.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := serviceWithGovInfo(t, tt.store, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTeapot) // never reached
			})
			err := s.SyncGovInfoChanges(t.Context(), 119)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
			if len(tt.store.errors) != 1 || len(tt.store.upserted) != 0 {
				t.Errorf("successes %+v, failures %q; want one failure", tt.store.upserted, tt.store.errors)
			}
		})
	}
}

// govinfoRunStore is the store of a whole sync-govinfo run: the watermark, sync_retry and one
// unfetched version for each package's text.
type govinfoRunStore struct {
	syncStateStore

	pkg govinfoPackageStore
}

func (f *govinfoRunStore) FindUnfetchedVersion(ctx context.Context, billID, versionCode string) (string, error) {
	return f.pkg.FindUnfetchedVersion(ctx, billID, versionCode)
}

func (f *govinfoRunStore) InsertBillText(ctx context.Context, t repository.BillTextRow) error {
	return f.pkg.InsertBillText(ctx, t)
}

func (f *govinfoRunStore) ReplaceBillLawRefs(
	ctx context.Context, billID, versionID string, rows []repository.BillLawRefRow,
) error {
	return f.pkg.ReplaceBillLawRefs(ctx, billID, versionID, rows)
}

// A package due again in sync_retry goes first, a polled package already due isn't processed
// twice, a failure is recorded for retry and every success is cleared.
func TestSyncGovInfoChanges_RetriesFailedPackages(t *testing.T) {
	store := &govinfoRunStore{
		since: time.Now().Add(-time.Hour),
		due:   map[string][]string{repository.RetryStepGovInfo: {"BILLS-119hr9ih"}}}
	var summaries []string
	s := serviceWithGovInfo(t, store, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/collections/"):
			fmt.Fprint(w, `{"packages": [{"packageId": "BILLS-119hr1ih"}, {"packageId": "BILLS-119hr9ih"},
				{"packageId": "BILLS-119hr2ih"}]}`)
		case strings.HasSuffix(r.URL.Path, "/summary"):
			summaries = append(summaries, strings.Split(r.URL.Path, "/")[2])
			switch r.URL.Path {
			case "/packages/BILLS-119hr1ih/summary":
				fmt.Fprintf(w, `{"download": {"txtLink": "http://%s/packages/BILLS-119hr1ih/htm"}}`, r.Host)
			case "/packages/BILLS-119hr2ih/summary":
				fmt.Fprint(w, `{"packageId": "BILLS-119hr2ih"}`) // no download yet
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		case r.URL.Path == "/packages/BILLS-119hr1ih/htm":
			fmt.Fprint(w, "SEC. 1. SHORT TITLE.\nThis Act is the Act.\n")
		default:
			http.NotFound(w, r)
		}
	})

	if err := s.SyncGovInfoChanges(t.Context(), 119); err != nil {
		t.Fatal(err)
	}
	if want := []string{"BILLS-119hr9ih", "BILLS-119hr1ih", "BILLS-119hr2ih"}; !reflect.DeepEqual(summaries, want) {
		t.Errorf("summaries fetched = %v, want %v", summaries, want)
	}
	if len(store.pkg.stored) != 1 || store.pkg.stored[0].Format != formatText ||
		store.pkg.stored[0].TextVersionID != "hr-119-1-ih" {
		t.Errorf("stored = %+v, want hr-119-1-ih as plain text from the txtLink", store.pkg.stored)
	}
	if got := store.failedItems(); !reflect.DeepEqual(got, map[string]bool{"BILLS-119hr9ih": false}) {
		t.Errorf("retry failures = %v, want BILLS-119hr9ih", got)
	}
	if got := store.clearedSorted(repository.RetryStepGovInfo); !reflect.DeepEqual(got,
		[]string{"BILLS-119hr1ih", "BILLS-119hr2ih"}) {
		t.Errorf("retries cleared = %v, want hr1 and hr2", got)
	}
	if len(store.upserted) != 1 || store.upserted[0].ItemsSynced != 2 {
		t.Errorf("sync state writes = %+v, want one with ItemsSynced 2", store.upserted)
	}
}

// processGovInfoPackage skips a package with nothing to store, and returns an error, for a retry,
// when a fetch or a write fails.
func TestProcessGovInfoPackage_SkipsAndFailures(t *testing.T) {
	const summary = `{"download": {"xmlLink": "http://{host}/packages/BILLS-119hr1ih/xml"}}`
	tests := []struct {
		name      string
		packageID string
		summary   string // the summary's body, with {host} for the server; "" answers 500
		textFails bool
		store     *govinfoPackageStore
		wantErr   string // "" for a skip
	}{
		{name: "not a bill package", packageID: "GAOREPORTS-GAO-26-1"},
		{name: "another congress", packageID: "BILLS-118hr1ih"},
		{name: "summary fails", packageID: "BILLS-119hr1ih", wantErr: "fetch package summary"},
		{name: "no download", packageID: "BILLS-119hr1ih", summary: `{"packageId": "BILLS-119hr1ih"}`},
		{name: "no text link", packageID: "BILLS-119hr1ih", summary: `{"download": {"pdfLink": "x"}}`},
		{
			name: "no unfetched version", packageID: "BILLS-119hr1ih", summary: summary,
			store: &govinfoPackageStore{findErr: errors.New("no unfetched version found")},
		},
		{
			name: "version lookup fails", packageID: "BILLS-119hr1ih", summary: summary,
			store: &govinfoPackageStore{findErr: errFakeStore}, wantErr: "find unfetched version",
		},
		{
			name:      "text fails",
			packageID: "BILLS-119hr1ih",
			summary:   summary,
			textFails: true,
			wantErr:   "fetch govinfo text",
		},
		{
			name: "insert fails", packageID: "BILLS-119hr1ih", summary: summary,
			store: &govinfoPackageStore{insertErr: errFakeStore}, wantErr: "insert govinfo bill text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			if store == nil {
				store = &govinfoPackageStore{}
			}
			var requests []string
			s := serviceWithGovInfo(t, store, func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.URL.Path)
				servePackage(w, r, tt.summary, tt.textFails)
			})

			err := s.processGovInfoPackage(t.Context(), tt.packageID, 119)
			checkErr(t, err, tt.wantErr)
			if len(store.stored) != 0 {
				t.Errorf("stored %d texts, want none", len(store.stored))
			}
			if !strings.HasPrefix(tt.packageID, "BILLS-119") && len(requests) != 0 {
				t.Errorf("requests = %v for a package not of this congress's bills, want none", requests)
			}
		})
	}
}

// servePackage answers for BILLS-119hr1ih: summary, with {host} replaced, or a 500 when it's "";
// the XML text, or a 500 when textFails.
func servePackage(w http.ResponseWriter, r *http.Request, summary string, textFails bool) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/summary") && summary != "":
		fmt.Fprint(w, strings.ReplaceAll(summary, "{host}", r.Host))
	case strings.HasSuffix(r.URL.Path, "/xml") && !textFails:
		fmt.Fprint(w, lawRefsXML)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// checkErr fails the test unless err is nil when want is "", or contains want.
func checkErr(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want one containing %q", err, want)
	}
}
