package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// crsStore is a PipelineStore holding CRS summaries in memory. bills are the bills with a bills
// row; sync_state is stepStore's.
type crsStore struct {
	stepStore

	crsMu     gosync.Mutex
	bills     map[string]bool
	versions  map[repository.CRSSummaryKey]repository.CRSSummaryRow
	upserts   [][]repository.CRSSummaryRow
	readErr   error
	upsertErr error
}

func newCRSStore(bills ...string) *crsStore {
	st := &crsStore{bills: map[string]bool{}, versions: map[repository.CRSSummaryKey]repository.CRSSummaryRow{}}
	for _, b := range bills {
		st.bills[b] = true
	}
	return st
}

func (f *crsStore) StoredCRSSummaries(_ context.Context, billIDs []string) (repository.StoredCRSSummaries, error) {
	f.crsMu.Lock()
	defer f.crsMu.Unlock()
	out := repository.StoredCRSSummaries{
		Versions: map[repository.CRSSummaryKey]repository.StoredCRSVersion{}, Bills: map[string]bool{},
	}
	if f.readErr != nil {
		return out, f.readErr
	}
	for key, row := range f.versions {
		if slices.Contains(billIDs, key.BillID) {
			out.Versions[key] = repository.StoredCRSVersion{
				ContentHash:  row.ContentHash,
				CRSUpdatedAt: row.CRSUpdatedAt,
			}
		}
	}
	for _, id := range billIDs {
		if f.bills[id] {
			out.Bills[id] = true
		}
	}
	return out, nil
}

func (f *crsStore) UpsertCRSSummaries(_ context.Context, rows []repository.CRSSummaryRow) error {
	f.crsMu.Lock()
	defer f.crsMu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserts = append(f.upserts, slices.Clone(rows))
	for _, r := range rows {
		f.versions[repository.CRSSummaryKey{BillID: r.BillID, VersionCode: r.VersionCode}] = r
	}
	return nil
}

func (f *crsStore) written() int {
	f.crsMu.Lock()
	defer f.crsMu.Unlock()
	n := 0
	for _, u := range f.upserts {
		n += len(u)
	}
	return n
}

// crsFeed serves /summaries/{congress} the way Congress.gov does: the items whose updateDate is
// in [fromDateTime, toDateTime], oldest first, 250 a page, with pagination.next while more are
// left. Items are raw JSON, so a test can serve malformed ones.
type crsFeed struct {
	t        *testing.T
	congress int
	items    []json.RawMessage
	// pageSizes, when set, is how many items each page returns (a short page), in request order.
	pageSizes []int
	// noNext leaves pagination.next out of every page.
	noNext bool
	// alwaysNext puts pagination.next on every page, even an empty one.
	alwaysNext bool
	failAt     int // 1-based request that answers 500; 0: none

	mu      gosync.Mutex
	queries []url.Values
}

func (f *crsFeed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, r.URL.Query())
	if r.URL.Path != fmt.Sprintf("/summaries/%d", f.congress) {
		http.NotFound(w, r)
		return
	}
	if f.failAt == len(f.queries) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	from, errFrom := time.Parse(time.RFC3339, q.Get("fromDateTime"))
	to, errTo := time.Parse(time.RFC3339, q.Get("toDateTime"))
	offset, errOff := strconv.Atoi(q.Get("offset"))
	if errFrom != nil || errTo != nil || errOff != nil || q.Get("sort") != "updateDate asc" || q.Get("limit") != "250" {
		f.t.Errorf("bad summaries query %v", q)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var window []json.RawMessage
	for _, item := range f.items {
		var s struct {
			UpdateDate string `json:"updateDate"`
		}
		_ = json.Unmarshal(item, &s)
		u, err := time.Parse(time.RFC3339, s.UpdateDate)
		if err != nil || (!u.Before(from) && !u.After(to)) {
			window = append(window, item)
		}
	}
	size := pageSize
	if n := len(f.queries) - 1; n < len(f.pageSizes) {
		size = f.pageSizes[n]
	}
	page := window[min(offset, len(window)):min(offset+size, len(window))]
	pg := map[string]any{"count": len(window) + 7} // count is never trusted
	if f.alwaysNext || (!f.noNext && offset+pageSize < len(window)) {
		pg["next"] = "https://api.congress.gov/v3/summaries/next"
	}
	body, _ := json.Marshal(map[string]any{"pagination": pg, "summaries": page})
	_, _ = w.Write(body)
}

func (f *crsFeed) requests() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

// crsItem is a listed summary of hr-<congress>-<n>, version 00, updated at updated.
func crsItem(congressNum, n int, updated time.Time, text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"actionDate": "2025-02-10", "actionDesc": "Introduced in House",
		"bill":           map[string]any{"congress": congressNum, "type": "HR", "number": strconv.Itoa(n)},
		"currentChamber": "House", "lastSummaryUpdateDate": updated.Add(-time.Minute).Format(time.RFC3339),
		"text": text, "updateDate": updated.Format(time.RFC3339), "versionCode": "00",
	})
	return b
}

// crsItems returns n summaries of hr-<congress>-1 … hr-<congress>-n, updated a minute apart
// from start.
func crsItems(congressNum, n int, start time.Time) []json.RawMessage {
	items := make([]json.RawMessage, n)
	for i := range items {
		items[i] = crsItem(congressNum, i+1, start.Add(time.Duration(i)*time.Minute),
			fmt.Sprintf("<p>Summary %d.</p>", i+1))
	}
	return items
}

// crsTestNow is the fake clock: a run's toDateTime.
var crsTestNow = time.Date(2026, 9, 29, 6, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // test constant

// newCRSService returns a service whose Congress.gov client talks to feed, with the log in logs.
func newCRSService(t *testing.T, store repository.PipelineStore, feed *crsFeed) (*Service, *bytes.Buffer) {
	t.Helper()
	feed.t = t
	srv := httptest.NewServer(feed)
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	return &Service{
		store:  store,
		api:    congress.NewClientWithBaseURL(srv.Client(), srv.URL),
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		clock:  func() time.Time { return crsTestNow },
	}, &logs
}

// logLine returns the attributes of the first log line with message msg.
func logLine(t *testing.T, logs *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for line := range strings.SplitSeq(logs.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == msg {
			return rec
		}
	}
	t.Fatalf("no %q log line in:\n%s", msg, logs.String())
	return nil
}

func wantCounts(t *testing.T, rec map[string]any, want map[string]float64) {
	t.Helper()
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("log %s = %v, want %v", k, rec[k], v)
		}
	}
}

// The first run lists every summary from the congress's first day to the run's start: 5,916
// summaries (the 119th on 2026-09-28) in 24 pages, all stored, orphans counted.
func TestSyncCRSSummaries_FullLoad(t *testing.T) {
	const total = 5916
	feed := &crsFeed{congress: 119, items: crsItems(119, total, time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC))}
	store := newCRSStore()
	for i := 1; i <= total-16; i++ {
		store.bills[fmt.Sprintf("hr-119-%d", i)] = true
	}
	s, logs := newCRSService(t, store, feed)

	if err := s.SyncCRSSummaries(t.Context(), 119); err != nil {
		t.Fatalf("SyncCRSSummaries: %v", err)
	}
	reqs := feed.requests()
	if len(reqs) != 24 {
		t.Errorf("requests = %d, want 24", len(reqs))
	}
	if got, want := reqs[0].Get("fromDateTime"), "2025-01-03T00:00:00Z"; got != want {
		t.Errorf("first run fromDateTime = %s, want the congress's first day %s", got, want)
	}
	for i, q := range reqs {
		if q.Get("toDateTime") != "2026-09-29T06:00:00Z" || q.Get("offset") != strconv.Itoa(i*pageSize) {
			t.Errorf("request %d: toDateTime %s offset %s", i, q.Get("toDateTime"), q.Get("offset"))
		}
	}
	if store.written() != total {
		t.Errorf("stored %d summaries, want %d", store.written(), total)
	}
	wantCounts(t, logLine(t, logs, "crs summaries synced"), map[string]float64{
		"congress": 119, "fetched": total, "changed": total, "orphans": 16, "skipped": 0, "requests": 24,
	})
	if len(store.successes) != 1 || store.successes[0].Step != stepCRSSummaries ||
		store.successes[0].ItemsSynced != total {
		t.Errorf("sync_state successes = %+v, want one crs_summaries run of %d", store.successes, total)
	}
}

// A later run starts an hour before the last successful run started, and rewrites only the
// summaries whose HTML or lastSummaryUpdateDate changed.
func TestSyncCRSSummaries_IncrementalRewritesOnlyChanges(t *testing.T) {
	lastRun := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	items := crsItems(119, 4, lastRun.Add(-30*time.Minute))
	store := newCRSStore("hr-119-1", "hr-119-2", "hr-119-3", "hr-119-4")
	feed := &crsFeed{congress: 119, items: items}
	s, _ := newCRSService(t, store, feed)
	s.forceSync = true // the first load
	if err := s.SyncCRSSummaries(t.Context(), 119); err != nil {
		t.Fatalf("first run: %v", err)
	}
	s.forceSync = false
	store.state = &repository.SyncStateRow{LastSyncedAt: lastRun}
	store.upserts = nil

	// 2: new text. 3: CRS re-published it (lastSummaryUpdateDate moved). 1 and 4: listed again,
	// unchanged. 4's updateDate moved, which alone isn't a change.
	var edited map[string]any
	_ = json.Unmarshal(items[1], &edited)
	edited["text"] = "<p>Summary 2, corrected.</p>"
	items[1], _ = json.Marshal(edited)
	var republished map[string]any
	_ = json.Unmarshal(items[2], &republished)
	republished["lastSummaryUpdateDate"] = "2026-09-29T01:00:00Z"
	items[2], _ = json.Marshal(republished)
	var touched map[string]any
	_ = json.Unmarshal(items[3], &touched)
	touched["updateDate"] = "2026-09-29T02:00:00Z"
	items[3], _ = json.Marshal(touched)
	feed.items = items
	rv := &recordingRevalidator{}
	s.revalidator = rv
	before := len(feed.requests())

	if err := s.SyncCRSSummaries(t.Context(), 119); err != nil {
		t.Fatalf("second run: %v", err)
	}
	q := feed.requests()[before]
	if q.Get("fromDateTime") != "2026-09-28T23:00:00Z" || q.Get("toDateTime") != "2026-09-29T06:00:00Z" {
		t.Errorf("window = %s to %s, want 2026-09-28T23:00:00Z to 2026-09-29T06:00:00Z",
			q.Get("fromDateTime"), q.Get("toDateTime"))
	}
	var got []string
	for _, u := range store.upserts {
		for _, r := range u {
			got = append(got, r.BillID)
		}
	}
	if want := []string{"hr-119-2", "hr-119-3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("rewrote %v, want %v", got, want)
	}
	if row := store.versions[repository.CRSSummaryKey{BillID: "hr-119-2", VersionCode: "00"}]; row.Text !=
		"Summary 2, corrected." {
		t.Errorf("hr-119-2 text = %q", row.Text)
	}
	if want := [][]string{{"hr-119-2", "hr-119-3"}}; !reflect.DeepEqual(rv.calls(), want) {
		t.Errorf("revalidated %v, want %v once", rv.calls(), want)
	}
}

func TestSyncCRSSummaries_Window(t *testing.T) {
	for _, tt := range []struct {
		name     string
		congress int
		state    *repository.SyncStateRow
		force    bool
		want     string
	}{
		{"first run", 119, nil, false, "2025-01-03T00:00:00Z"},
		{"never succeeded", 119, &repository.SyncStateRow{ConsecutiveFailures: 2}, false, "2025-01-03T00:00:00Z"},
		{"after a run", 119, &repository.SyncStateRow{LastSyncedAt: time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC)},
			false, "2026-09-28T11:30:00Z"},
		{"force", 119, &repository.SyncStateRow{LastSyncedAt: time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC)},
			true, "2025-01-03T00:00:00Z"},
		{"within the first hour", 119, &repository.SyncStateRow{LastSyncedAt: time.Date(2025, 1, 3, 0, 20, 0, 0,
			time.UTC)}, false, "2025-01-03T00:00:00Z"},
		{"118th", 118, nil, false, "2023-01-03T00:00:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newCRSStore()
			store.state = tt.state
			feed := &crsFeed{congress: tt.congress}
			s, _ := newCRSService(t, store, feed)
			s.forceSync = tt.force
			if err := s.SyncCRSSummaries(t.Context(), tt.congress); err != nil {
				t.Fatalf("SyncCRSSummaries: %v", err)
			}
			if got := feed.requests()[0].Get("fromDateTime"); got != tt.want {
				t.Errorf("fromDateTime = %s, want %s", got, tt.want)
			}
		})
	}
}

// The 118th loads the same way, into 118th bill IDs.
func TestSyncCRSSummaries_PastCongress(t *testing.T) {
	store := newCRSStore("hr-118-2")
	feed := &crsFeed{congress: 118, items: crsItems(118, 3, time.Date(2023, 5, 1, 0, 0, 0, 0, time.UTC))}
	s, logs := newCRSService(t, store, feed)
	if err := s.SyncCRSSummaries(t.Context(), 118); err != nil {
		t.Fatalf("SyncCRSSummaries: %v", err)
	}
	for _, id := range []string{"hr-118-1", "hr-118-2", "hr-118-3"} {
		if _, ok := store.versions[repository.CRSSummaryKey{BillID: id, VersionCode: "00"}]; !ok {
			t.Errorf("%s not stored", id)
		}
	}
	wantCounts(t, logLine(t, logs, "crs summaries synced"), map[string]float64{
		"congress": 118, "fetched": 3, "changed": 3, "orphans": 2, "requests": 1,
	})
}

// Paging stops at a page without pagination.next, or at an empty page, whatever count says. A
// short page that still has next isn't the end (design 64: a short page can have a next one).
func TestSyncCRSSummaries_PagingStops(t *testing.T) {
	start := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name         string
		feed         *crsFeed
		wantRequests int
		wantStored   int
	}{
		{"full page without next", &crsFeed{items: crsItems(119, 600, start), noNext: true}, 1, 250},
		{"short last page", &crsFeed{items: crsItems(119, 260, start)}, 2, 260},
		{"empty page with next", &crsFeed{items: crsItems(119, 250, start), alwaysNext: true}, 2, 250},
		// Offsets are list positions, as in fetchAll: the page after a short one is at 250.
		{"short page with next", &crsFeed{items: crsItems(119, 300, start), pageSizes: []int{200}}, 2, 250},
		{"nothing new", &crsFeed{}, 1, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			feed := tt.feed
			feed.congress = 119
			store := newCRSStore()
			s, _ := newCRSService(t, store, feed)
			if err := s.SyncCRSSummaries(t.Context(), 119); err != nil {
				t.Fatalf("SyncCRSSummaries: %v", err)
			}
			if got := len(feed.requests()); got != tt.wantRequests {
				t.Errorf("requests = %d, want %d", got, tt.wantRequests)
			}
			if store.written() != tt.wantStored {
				t.Errorf("stored %d, want %d", store.written(), tt.wantStored)
			}
		})
	}
}

// A summary that can't be read is logged and skipped; the rest of the page is stored and the run
// succeeds.
func TestSyncCRSSummaries_MalformedSkipped(t *testing.T) {
	at := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	good := crsItem(119, 1, at, "<p>Fine.</p>")
	mutate := func(n int, key string, value any) json.RawMessage {
		var m map[string]any
		_ = json.Unmarshal(crsItem(119, n, at, "<p>Summary.</p>"), &m)
		if key == "bill.type" {
			m["bill"].(map[string]any)["type"] = value
		} else {
			m[key] = value
		}
		b, _ := json.Marshal(m)
		return b
	}
	feed := &crsFeed{congress: 119, items: []json.RawMessage{
		good,
		json.RawMessage(`{"bill":{"congress":"119"},"versionCode":"00"}`), // doesn't decode
		mutate(3, "bill.type", "PL"),
		mutate(4, "actionDate", "July 4"),
		mutate(5, "lastSummaryUpdateDate", ""),
		mutate(6, "updateDate", "yesterday"),
		mutate(7, "text", "<p> &nbsp; </p>"),
		mutate(8, "versionCode", ""),
		mutate(9, "versionCode", "123456789"),
	}}
	store := newCRSStore("hr-119-1")
	s, logs := newCRSService(t, store, feed)
	if err := s.SyncCRSSummaries(t.Context(), 119); err != nil {
		t.Fatalf("SyncCRSSummaries: %v", err)
	}
	if store.written() != 1 {
		t.Errorf("stored %d, want only the good summary", store.written())
	}
	wantCounts(t, logLine(t, logs, "crs summaries synced"), map[string]float64{
		"fetched": 1, "changed": 1, "skipped": 8,
	})
	if n := strings.Count(logs.String(), "skipping malformed crs summary"); n != 8 {
		t.Errorf("logged %d skipped summaries, want 8", n)
	}
	if len(store.successes) != 1 {
		t.Errorf("successes = %d, want 1", len(store.successes))
	}
}

func TestCRSSummaryRow_Validation(t *testing.T) {
	base := congress.CRSSummary{
		ActionDate: "2025-02-10", ActionDesc: " Introduced in Senate ", CurrentChamber: "Senate",
		LastSummaryUpdateDate: "2025-03-01T10:00:00Z", UpdateDate: "2025-03-01T10:05:00Z",
		Text: "<p>Text.</p>", VersionCode: "00",
	}
	base.Bill.Congress, base.Bill.Type, base.Bill.Number = 119, "SJRES", "12"
	row, err := crsSummaryRow(base, 119)
	if err != nil {
		t.Fatalf("crsSummaryRow: %v", err)
	}
	if row.BillID != "sjres-119-12" || row.ActionDesc != "Introduced in Senate" || row.Chamber == nil ||
		*row.Chamber != "Senate" || row.Text != "Text." {
		t.Errorf("row = %+v", row)
	}
	for name, change := range map[string]func(*congress.CRSSummary){
		"other congress": func(c *congress.CRSSummary) { c.Bill.Congress = 118 },
		"bad number":     func(c *congress.CRSSummary) { c.Bill.Number = "12a" },
		"zero number":    func(c *congress.CRSSummary) { c.Bill.Number = "0" },
	} {
		c := base
		change(&c)
		if _, err = crsSummaryRow(c, 119); !errors.Is(err, errMalformedCRSSummary) {
			t.Errorf("%s: error = %v, want malformed", name, err)
		}
	}
}

// H.R. 1's Public Law summary, as Congress.gov lists it: the longest CRS summary of the 119th.
func TestSyncCRSSummaries_HR1PublicLaw(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("testdata", "crs", "119-hr1-public-law.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		Summaries []json.RawMessage `json:"summaries"`
	}
	if err = json.Unmarshal(page, &recorded); err != nil {
		t.Fatal(err)
	}
	var html struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(recorded.Summaries[0], &html)
	store := newCRSStore("hr-119-1")
	feed := &crsFeed{congress: 119, items: recorded.Summaries}
	s, _ := newCRSService(t, store, feed)
	if err = s.SyncCRSSummaries(t.Context(), 119); err != nil {
		t.Fatalf("SyncCRSSummaries: %v", err)
	}

	row, ok := store.versions[repository.CRSSummaryKey{BillID: "hr-119-1", VersionCode: "49"}]
	if !ok {
		t.Fatalf("H.R. 1 version 49 not stored: %v", store.versions)
	}
	sum := sha256.Sum256([]byte(html.Text))
	if row.ActionDesc != "Public Law" || row.ActionDate.Format(time.DateOnly) != "2025-07-04" || row.Chamber != nil ||
		!row.CRSUpdatedAt.Equal(time.Date(2025, 10, 6, 21, 40, 39, 0, time.UTC)) ||
		!row.SourceUpdatedAt.Equal(time.Date(2025, 10, 6, 21, 49, 14, 0, time.UTC)) ||
		row.TextHTML != html.Text || row.ContentHash != hex.EncodeToString(sum[:]) {
		t.Errorf("row = %+v (text omitted)", withoutText(row))
	}
	if !strings.HasPrefix(row.Text, "This act reduces taxes, reduces or increases spending") ||
		strings.ContainsAny(row.Text, "<> ") || strings.Contains(row.Text, "&nbsp;") {
		t.Errorf("text starts %q and must have no tags or entities", row.Text[:200])
	}
	if !strings.Contains(row.Text, "\n\n") || !strings.Contains(row.Text, "\n"+"• ") {
		t.Error("text has no paragraphs or list lines")
	}
}

func withoutText(r repository.CRSSummaryRow) repository.CRSSummaryRow {
	r.Text, r.TextHTML = "", ""
	return r
}

func TestSyncCRSSummaries_FailuresFailTheRun(t *testing.T) {
	items := crsItems(119, 300, time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC))
	for _, tt := range []struct {
		name  string
		setup func(*crsStore, *crsFeed)
	}{
		{"list error", func(_ *crsStore, f *crsFeed) { f.failAt = 2 }},
		{"read error", func(st *crsStore, _ *crsFeed) { st.readErr = errors.New("spanner down") }},
		{"write error", func(st *crsStore, _ *crsFeed) { st.upsertErr = errors.New("spanner down") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newCRSStore()
			feed := &crsFeed{congress: 119, items: items}
			tt.setup(store, feed)
			s, _ := newCRSService(t, store, feed)
			if err := s.SyncCRSSummaries(t.Context(), 119); err == nil {
				t.Fatal("SyncCRSSummaries: want an error")
			}
			if len(store.successes) != 0 || len(store.failures) != 1 {
				t.Errorf("sync_state: %d successes, %d failures; want the failure recorded",
					len(store.successes), len(store.failures))
			}
		})
	}
}

// failingStateStore can't read sync_state.
type failingStateStore struct{ crsStore }

func (*failingStateStore) GetSyncState(context.Context, string, int) (*repository.SyncStateRow, error) {
	return nil, errors.New("spanner down")
}

func TestSyncCRSSummaries_StateReadErrorAndCancel(t *testing.T) {
	store := &failingStateStore{crsStore: *newCRSStore()}
	feed := &crsFeed{congress: 119}
	s, _ := newCRSService(t, store, feed)
	if err := s.SyncCRSSummaries(t.Context(), 119); err == nil || len(feed.requests()) != 0 {
		t.Errorf("SyncCRSSummaries = %v after %d requests; want an error and no request", err, len(feed.requests()))
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	feed = &crsFeed{congress: 119}
	s, _ = newCRSService(t, newCRSStore(), feed)
	if err := s.SyncCRSSummaries(ctx, 119); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled SyncCRSSummaries = %v, want context.Canceled", err)
	}
}
