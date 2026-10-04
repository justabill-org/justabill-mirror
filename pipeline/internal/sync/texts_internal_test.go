package sync

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

// textsStore is a fake store for sync-texts: sections by version ID, canned sweep results, and
// the diffs written, changed and empty apart. Like the real store, a pair with a stored diff isn't
// missing. It is safe for concurrent use.
type textsStore struct {
	repository.PipelineStore

	mu        sync.Mutex
	inserts   map[string]int // InsertBillText calls by version ID
	unfetched []repository.TextVersionRef
	previous  *repository.PreviousVersionInfo
	sections  map[string]json.RawMessage
	deleted   repository.DeletedDiffs
	missing   []repository.DiffPair

	deleteErr  error
	missingErr error
	missingLim int
	diffs      []repository.DiffPair                 // changed diffs written
	empty      []repository.DiffPair                 // empty diffs written, flagged IsEmpty
	loads      map[string]int                        // LoadSections calls by version ID
	lawRefs    map[string][]repository.BillLawRefRow // by version ID
	refetch    repository.TextRefetch                // RefetchBillText's answer
	refetched  []repository.BillTextRow
	stored     []repository.BillTextRow // InsertBillText rows

	queryErr    error // QueryUnfetchedTextVersions
	insertErr   error // InsertBillText
	refetchErr  error // RefetchBillText
	previousErr error // FindPreviousVersion
	diffErr     error // InsertBillTextDiff
}

func (f *textsStore) QueryUnfetchedTextVersions(context.Context, int) ([]repository.TextVersionRef, error) {
	return f.unfetched, f.queryErr
}

func (f *textsStore) InsertBillText(_ context.Context, t repository.BillTextRow) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inserts == nil {
		f.inserts = map[string]int{}
	}
	f.inserts[t.TextVersionID]++
	f.stored = append(f.stored, t)
	f.sections[t.TextVersionID] = t.Sections
	return nil
}

func (f *textsStore) RefetchBillText(
	_ context.Context, t repository.BillTextRow,
) (repository.TextRefetch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refetched = append(f.refetched, t)
	return f.refetch, f.refetchErr
}

func (f *textsStore) ReplaceBillLawRefs(
	_ context.Context, _, versionID string, rows []repository.BillLawRefRow,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lawRefs == nil {
		f.lawRefs = map[string][]repository.BillLawRefRow{}
	}
	f.lawRefs[versionID] = rows
	return nil
}

// FindPreviousVersion returns the canned previous version if there is one, and otherwise the
// latest version before versionID in the unfetched queue that has a stored text.
func (f *textsStore) FindPreviousVersion(
	_ context.Context, billID, versionID string,
) (*repository.PreviousVersionInfo, error) {
	if f.previousErr != nil {
		return nil, f.previousErr
	}
	if f.previous != nil {
		return f.previous, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.unfetched, func(v repository.TextVersionRef) bool { return v.ID == versionID })
	for j := i - 1; j >= 0; j-- {
		v := f.unfetched[j]
		if s, ok := f.sections[v.ID]; ok && v.BillID == billID {
			return &repository.PreviousVersionInfo{VersionID: v.ID, Sections: s}, nil
		}
	}
	return nil, nil //nolint:nilnil // the fake mirrors the store: no previous version is nil, nil
}

func (f *textsStore) LoadSections(_ context.Context, versionID string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loads == nil {
		f.loads = map[string]int{}
	}
	f.loads[versionID]++
	s, ok := f.sections[versionID]
	if !ok {
		return nil, errors.New("no text for " + versionID)
	}
	return s, nil
}

func (f *textsStore) InsertBillTextDiff(_ context.Context, d repository.BillTextDiffRow) error {
	if f.diffErr != nil {
		return f.diffErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := repository.DiffPair{BillID: d.BillID, FromVersionID: d.FromVersionID, ToVersionID: d.ToVersionID}
	if d.IsEmpty {
		f.empty = append(f.empty, p)
	} else {
		f.diffs = append(f.diffs, p)
	}
	return nil
}

func (f *textsStore) DeleteNonConsecutiveDiffs(context.Context) (repository.DeletedDiffs, error) {
	return f.deleted, f.deleteErr
}

func (f *textsStore) QueryMissingDiffPairs(_ context.Context, limit int) ([]repository.DiffPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.missingLim = limit
	var missing []repository.DiffPair
	for _, p := range f.missing {
		if !slices.Contains(f.diffs, p) && !slices.Contains(f.empty, p) {
			missing = append(missing, p)
		}
	}
	return missing, f.missingErr
}

const (
	sectionsOld = `[{"id":"sec1","header":"SHORT TITLE.","content":"This Act is the Old Act."}]`
	sectionsNew = `[{"id":"sec1","header":"SHORT TITLE.","content":"This Act is the New Act."}]`
)

func textsService(store *textsStore) (*Service, *bytes.Buffer) {
	var logs bytes.Buffer
	return &Service{
		store:  store,
		logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, &logs
}

func TestSweepDiffs_DeletesThenComputesMissingPairs(t *testing.T) {
	changed := repository.DiffPair{BillID: "hr-119-1", FromVersionID: "ih", ToVersionID: "rh"}
	same := repository.DiffPair{BillID: "hr-119-1", FromVersionID: "rh", ToVersionID: "eh"}
	broken := repository.DiffPair{BillID: "hr-119-2", FromVersionID: "ih2", ToVersionID: "gone"}
	store := &textsStore{
		sections: map[string]json.RawMessage{
			"ih": json.RawMessage(sectionsOld), "rh": json.RawMessage(sectionsNew),
			"eh": json.RawMessage(sectionsNew), "ih2": json.RawMessage(sectionsOld),
		},
		deleted: repository.DeletedDiffs{Diffs: 2, Summaries: 1},
		missing: []repository.DiffPair{changed, same, broken},
	}
	svc, logs := textsService(store)

	if err := svc.sweepDiffs(t.Context(), 0); err != nil {
		t.Fatalf("sweepDiffs: %v", err)
	}
	if want := []repository.DiffPair{changed}; !slices.Equal(store.diffs, want) {
		t.Errorf("changed diffs written = %+v, want only %+v", store.diffs, want)
	}
	if want := []repository.DiffPair{same}; !slices.Equal(store.empty, want) {
		t.Errorf("empty diffs written = %+v, want only %+v", store.empty, want)
	}
	for _, line := range []string{
		`level=WARN msg="diff sweep done" deleted_diffs=2 deleted_summaries=1 missing_pairs=3 computed=1 empty=1 failed=1`,
		`level=WARN msg="compute missing diff failed" bill_id=hr-119-2 from_version=ih2 to_version=gone`,
		`level=DEBUG msg="stored empty diff" bill_id=hr-119-1 from_version=rh to_version=eh`,
	} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("logs lack %q:\n%s", line, logs)
		}
	}
}

// TestSweepDiffs_RemembersEmptyPairs is #401's AC 1: the second run doesn't load an empty pair.
func TestSweepDiffs_RemembersEmptyPairs(t *testing.T) {
	same := repository.DiffPair{BillID: "hr-119-1", FromVersionID: "eh", ToVersionID: "rfs"}
	store := &textsStore{
		sections: map[string]json.RawMessage{
			"eh": json.RawMessage(sectionsNew), "rfs": json.RawMessage(sectionsNew),
		},
		missing: []repository.DiffPair{same},
	}
	svc, logs := textsService(store)

	if err := svc.sweepDiffs(t.Context(), 0); err != nil {
		t.Fatalf("first sweepDiffs: %v", err)
	}
	if !slices.Equal(store.empty, []repository.DiffPair{same}) || len(store.diffs) != 0 {
		t.Fatalf("first run wrote empty %+v and changed %+v, want one empty", store.empty, store.diffs)
	}
	if store.loads["eh"] != 1 || store.loads["rfs"] != 1 {
		t.Fatalf("first run loads = %v, want each version once", store.loads)
	}

	logs.Reset()
	if err := svc.sweepDiffs(t.Context(), 0); err != nil {
		t.Fatalf("second sweepDiffs: %v", err)
	}
	if store.loads["eh"] != 1 || store.loads["rfs"] != 1 || len(store.empty) != 1 {
		t.Errorf("second run loads = %v, empty = %+v; want no new loads or rows", store.loads, store.empty)
	}
	if want := `missing_pairs=0 computed=0 empty=0 failed=0`; !strings.Contains(logs.String(), want) {
		t.Errorf("logs lack %q:\n%s", want, logs)
	}
}

func TestSweepDiffs_NothingToDoLogsAtInfo(t *testing.T) {
	store := &textsStore{}
	svc, logs := textsService(store)

	if err := svc.sweepDiffs(t.Context(), 25); err != nil {
		t.Fatalf("sweepDiffs: %v", err)
	}
	if store.missingLim != 25 {
		t.Errorf("missing-pairs limit = %d, want 25", store.missingLim)
	}
	want := `level=INFO msg="diff sweep done" deleted_diffs=0 deleted_summaries=0 missing_pairs=0 computed=0`
	if !strings.Contains(logs.String(), want) {
		t.Errorf("logs lack %q:\n%s", want, logs)
	}
}

func TestSweepDiffs_StoreErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, store := range map[string]*textsStore{
		"delete": {deleteErr: boom},
		"query":  {missingErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := textsService(store)
			if err := svc.sweepDiffs(t.Context(), 0); !errors.Is(err, boom) {
				t.Errorf("sweepDiffs = %v, want %v", err, boom)
			}
		})
	}
}

func TestSweepDiffs_StopsWhenCanceled(t *testing.T) {
	store := &textsStore{missing: []repository.DiffPair{{BillID: "b", FromVersionID: "x", ToVersionID: "y"}}}
	svc, _ := textsService(store)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := svc.sweepDiffs(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("sweepDiffs = %v, want context.Canceled", err)
	}
}

// TestSyncBillTexts_StoresEmptyFetchTimeDiff checks that a newly fetched version that changes no
// section stores the empty diff at once, so the same run's sweep doesn't diff the pair again.
func TestSyncBillTexts_StoresEmptyFetchTimeDiff(t *testing.T) {
	const text = "SEC. 1. SHORT TITLE.\nThis Act is the New Act.\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(text))
	}))
	t.Cleanup(srv.Close)
	formats, err := json.Marshal([]textFormat{{Type: formatText, URL: srv.URL + "/BILLS-119hr1rfs.htm"}})
	if err != nil {
		t.Fatal(err)
	}
	sections, err := billtext.ParsePlainText(text)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := json.Marshal(sections)
	if err != nil {
		t.Fatal(err)
	}
	pair := repository.DiffPair{BillID: "hr-119-1", FromVersionID: "eh", ToVersionID: "rfs"}
	store := &textsStore{
		unfetched: []repository.TextVersionRef{{ID: "rfs", BillID: "hr-119-1", VersionCode: "rfs", Formats: formats}},
		previous:  &repository.PreviousVersionInfo{VersionID: "eh", Sections: prev},
		sections:  map[string]json.RawMessage{"eh": prev},
		missing:   []repository.DiffPair{pair},
	}
	svc, logs := textsService(store)
	svc.http = srv.Client()

	if _, err = svc.syncBillTexts(t.Context(), 119, 0); err != nil {
		t.Fatalf("syncBillTexts: %v", err)
	}
	if !slices.Equal(store.empty, []repository.DiffPair{pair}) || len(store.diffs) != 0 {
		t.Errorf("wrote empty %+v and changed %+v, want the one empty pair", store.empty, store.diffs)
	}
	if store.loads["eh"] != 0 {
		t.Errorf("the sweep loaded eh %d times, want 0: the fetch-time diff stored the pair", store.loads["eh"])
	}
	if !strings.Contains(logs.String(), `missing_pairs=0`) {
		t.Errorf("the sweep found the pair missing:\n%s", logs)
	}
}

func TestSyncBillTexts_FetchesDiffsAndSweeps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("SEC. 1. SHORT TITLE.\nThis Act is the New Act.\n"))
	}))
	t.Cleanup(srv.Close)
	formats, err := json.Marshal([]textFormat{{Type: formatText, URL: srv.URL + "/BILLS-119hr1rh.htm"}})
	if err != nil {
		t.Fatal(err)
	}
	store := &textsStore{
		unfetched: []repository.TextVersionRef{{ID: "rh", BillID: "hr-119-1", VersionCode: "rh", Formats: formats}},
		previous:  &repository.PreviousVersionInfo{VersionID: "ih", Sections: json.RawMessage(sectionsOld)},
		sections:  map[string]json.RawMessage{},
		deleted:   repository.DeletedDiffs{Diffs: 1},
	}
	svc, logs := textsService(store)
	svc.http = srv.Client()

	total, err := svc.syncBillTexts(t.Context(), 119, 0)
	if err != nil || total != 1 {
		t.Fatalf("syncBillTexts = %d, %v; want 1, nil", total, err)
	}
	want := []repository.DiffPair{{BillID: "hr-119-1", FromVersionID: "ih", ToVersionID: "rh"}}
	if !slices.Equal(store.diffs, want) {
		t.Errorf("diffs written = %+v, want %+v", store.diffs, want)
	}
	if !strings.Contains(logs.String(), `msg="diff sweep done" deleted_diffs=1`) {
		t.Errorf("the sweep didn't run after the fetch:\n%s", logs)
	}
	// A plain-text version has no XML to parse, and there's no GovInfo client for MODS: its
	// references are written, and empty.
	if rows, ok := store.lawRefs["rh"]; !ok || len(rows) != 0 {
		t.Errorf("law refs for rh = %v (written %v), want an empty write", rows, ok)
	}
}

func TestSyncBillTexts_StoresLawRefsOfXMLText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<bill><legis-body><section id="S2"><text>Section 102(f) of the Family and
			Medical Leave Act of 1993 (<external-xref legal-doc="usc" parsable-cite="usc/29/2612">29 U.S.C.
			2612(f)</external-xref>) is repealed.</text></section></legis-body></bill>`))
	}))
	t.Cleanup(srv.Close)
	formats, err := json.Marshal([]textFormat{{Type: formatXML, URL: srv.URL + "/BILLS-119hr3404ih.xml"}})
	if err != nil {
		t.Fatal(err)
	}
	store := &textsStore{
		unfetched: []repository.TextVersionRef{{ID: "ih", BillID: "hr-119-3404", VersionCode: "ih", Formats: formats}},
		sections:  map[string]json.RawMessage{},
	}
	svc, _ := textsService(store)
	svc.http = srv.Client()

	if _, err = svc.syncBillTexts(t.Context(), 119, 0); err != nil {
		t.Fatalf("syncBillTexts: %v", err)
	}
	rows := store.lawRefs["ih"]
	if len(rows) != 1 || rows[0].SectionID != "/us/usc/t29/s2612" || rows[0].RefKind != "repeals" ||
		rows[0].SubsectionPath == nil || *rows[0].SubsectionPath != "(f)" {
		t.Errorf("law refs = %+v, want 29 U.S.C. 2612(f) repealed", rows)
	}
}

func TestSyncBillTexts_ReturnsSweepError(t *testing.T) {
	store := &textsStore{deleteErr: errors.New("boom")}
	svc, _ := textsService(store)

	if _, err := svc.syncBillTexts(t.Context(), 119, 0); err == nil {
		t.Error("syncBillTexts = nil, want the sweep's error")
	}
}

// refetchCase is one TestSyncBillTexts_Refetch case: what RefetchBillText answers, and what
// sync-texts should then do.
type refetchCase struct {
	name    string
	refetch repository.TextRefetch
	lawRefs bool
	marked  []string
	logLine string
}

func TestSyncBillTexts_Refetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("SEC. 1. SHORT TITLE.\nThis Act is the Corrected Act.\n"))
	}))
	t.Cleanup(srv.Close)
	for _, tt := range []refetchCase{
		{
			name:    "unchanged text leaves the rest alone",
			marked:  []string{},
			logLine: `level=INFO msg="refetched text unchanged" text_version_id=rh bill_id=hr-119-1`,
		},
		{
			name: "changed text rewrites law refs and marks the bill",
			refetch: repository.TextRefetch{
				Changed: true, Deleted: repository.DeletedDiffs{Diffs: 2, Summaries: 1},
			},
			lawRefs: true,
			marked:  []string{"hr-119-1"},
			logLine: `level=WARN msg="refetched text changed" text_version_id=rh bill_id=hr-119-1 ` +
				`version_code=rh deleted_diffs=2 deleted_summaries=1`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) { testRefetch(t, srv, tt) })
	}
}

func testRefetch(t *testing.T, srv *httptest.Server, tt refetchCase) {
	t.Helper()
	formats, err := json.Marshal([]textFormat{{Type: formatText, URL: srv.URL + "/BILLS-119hr1rh.htm"}})
	if err != nil {
		t.Fatal(err)
	}
	store := &textsStore{
		unfetched: []repository.TextVersionRef{
			{ID: "rh", BillID: "hr-119-1", VersionCode: "rh", Formats: formats, Refetch: true},
		},
		previous: &repository.PreviousVersionInfo{VersionID: "ih", Sections: json.RawMessage(sectionsOld)},
		sections: map[string]json.RawMessage{},
		refetch:  tt.refetch,
	}
	svc, logs := textsService(store)
	svc.http = srv.Client()
	ctx, cs := withChangeSet(t.Context())

	if total, syncErr := svc.syncBillTexts(ctx, 119, 0); syncErr != nil || total != 1 {
		t.Fatalf("syncBillTexts = %d, %v; want 1, nil", total, syncErr)
	}
	if len(store.refetched) != 1 || store.refetched[0].TextVersionID != "rh" ||
		!strings.Contains(store.refetched[0].Content, "Corrected Act") {
		t.Fatalf("refetched = %+v, want rh's new text", store.refetched)
	}
	if _, inserted := store.sections["rh"]; inserted {
		t.Error("a refetch went through InsertBillText")
	}
	if len(store.diffs) != 0 {
		t.Errorf("diffs written = %+v, want none: the sweep recomputes a changed text's diffs", store.diffs)
	}
	if _, ok := store.lawRefs["rh"]; ok != tt.lawRefs {
		t.Errorf("law refs written = %v, want %v", ok, tt.lawRefs)
	}
	if got := cs.billIDs(); !slices.Equal(got, tt.marked) {
		t.Errorf("marked bills = %v, want %v", got, tt.marked)
	}
	if !strings.Contains(logs.String(), tt.logLine) {
		t.Errorf("logs lack %q:\n%s", tt.logLine, logs)
	}
}

// gatedTextServer serves a version's text by the code at the end of its path (ih: the old
// text, rh: the new one; fail: HTTP 500). Each request waits until full requests are in flight
// at once (or a second passes), so a sync that runs fewer at once is slow and one that runs more
// is seen. It records the most requests it saw in flight. In-flight requests can reach full
// again after the gate opens, so it closes isFull only once (#514).
type gatedTextServer struct {
	full     int
	isFull   chan struct{}
	opened   sync.Once
	mu       sync.Mutex
	inFlight int
	most     int
}

func (g *gatedTextServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.inFlight++
	g.most = max(g.most, g.inFlight)
	if g.inFlight == g.full {
		g.opened.Do(func() { close(g.isFull) })
	}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
	}()
	select {
	case <-g.isFull:
	case <-time.After(time.Second):
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "fail.htm"):
		w.WriteHeader(http.StatusInternalServerError)
	case strings.HasSuffix(r.URL.Path, "ih.htm"):
		_, _ = w.Write([]byte("SEC. 1. SHORT TITLE.\nThis Act is the Old Act.\n"))
	default:
		_, _ = w.Write([]byte("SEC. 1. SHORT TITLE.\nThis Act is the New Act.\n"))
	}
}

// unfetchedText is an unfetched plain-text version of bill served by srv.
func unfetchedText(t *testing.T, srvURL, bill, code string) repository.TextVersionRef {
	t.Helper()
	formats, err := json.Marshal([]textFormat{{Type: formatText, URL: srvURL + "/" + bill + code + ".htm"}})
	if err != nil {
		t.Fatal(err)
	}
	return repository.TextVersionRef{ID: bill + "-" + code, BillID: bill, VersionCode: code, Formats: formats}
}

func TestSyncBillTexts_BoundedConcurrencyInBillOrder(t *testing.T) {
	gate := &gatedTextServer{full: defaultTextWorkers, isFull: make(chan struct{})}
	srv := httptest.NewServer(gate)
	t.Cleanup(srv.Close)

	const bills = 7
	store := &textsStore{sections: map[string]json.RawMessage{}}
	var wantDiffs []repository.DiffPair
	for i := range bills {
		bill := "hr-119-" + strconv.Itoa(i+1)
		ih, rh := unfetchedText(t, srv.URL, bill, "ih"), unfetchedText(t, srv.URL, bill, "rh")
		store.unfetched = append(store.unfetched, ih, rh)
		wantDiffs = append(wantDiffs, repository.DiffPair{BillID: bill, FromVersionID: ih.ID, ToVersionID: rh.ID})
	}
	failed := unfetchedText(t, srv.URL, "s-119-1", "fail")
	store.unfetched = append(store.unfetched, failed)
	svc, logs := textsService(store)
	svc.http = srv.Client()

	total, err := svc.syncBillTexts(t.Context(), 119, 0)
	if err != nil || total != 2*bills {
		t.Fatalf("syncBillTexts = %d, %v; want %d, nil", total, err, 2*bills)
	}
	gate.mu.Lock()
	most := gate.most
	gate.mu.Unlock()
	if most != defaultTextWorkers {
		t.Errorf("most downloads at once = %d, want %d", most, defaultTextWorkers)
	}
	for _, v := range store.unfetched[:2*bills] {
		if n := store.inserts[v.ID]; n != 1 {
			t.Errorf("%s stored %d times, want once", v.ID, n)
		}
	}
	if n, ok := store.inserts[failed.ID]; ok {
		t.Errorf("the failed version was stored %d times", n)
	}
	// Each bill's later version is diffed against its earlier one, which was stored first.
	sortPairs := func(a, b repository.DiffPair) int { return strings.Compare(a.BillID, b.BillID) }
	slices.SortFunc(store.diffs, sortPairs)
	if !slices.Equal(store.diffs, wantDiffs) {
		t.Errorf("diffs written = %+v, want %+v", store.diffs, wantDiffs)
	}
	want := `level=WARN msg="fetch text failed" text_version_id=s-119-1-fail`
	if !strings.Contains(logs.String(), want) {
		t.Errorf("logs lack %q:\n%s", want, logs)
	}
}

// When a request ends and another starts, in-flight requests can reach full again; the gate
// must stay open rather than close isFull twice (a panic that left mu locked, #514).
func TestGatedTextServer_FullMoreThanOnce(t *testing.T) {
	gate := &gatedTextServer{full: 1, isFull: make(chan struct{})}
	for i := range 3 {
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/hr-119-1ih.htm", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want %d", i+1, rec.Code, http.StatusOK)
		}
	}
	if gate.inFlight != 0 || gate.most != 1 {
		t.Errorf("inFlight = %d, most = %d; want 0, 1", gate.inFlight, gate.most)
	}
}

func TestSyncBillTexts_StopsWhenCanceled(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(srv.Close)
	store := &textsStore{
		unfetched: []repository.TextVersionRef{unfetchedText(t, srv.URL, "hr-119-1", "ih")},
		sections:  map[string]json.RawMessage{},
	}
	svc, _ := textsService(store)
	svc.http = srv.Client()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if total, err := svc.syncBillTexts(ctx, 119, 0); total != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("syncBillTexts = %d, %v; want 0, context.Canceled", total, err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d downloads after the cancel, want none", n)
	}
}

func TestGroupByBill(t *testing.T) {
	versions := []repository.TextVersionRef{
		{ID: "a1", BillID: "a"}, {ID: "a2", BillID: "a"}, {ID: "b1", BillID: "b"}, {ID: "c1", BillID: "c"},
	}
	var got [][]string
	for _, bill := range groupByBill(versions) {
		var ids []string
		for _, v := range bill {
			ids = append(ids, v.ID)
		}
		got = append(got, ids)
	}
	if want := [][]string{{"a1", "a2"}, {"b1"}, {"c1"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("groupByBill = %v, want %v", got, want)
	}
	if got := groupByBill(nil); len(got) != 0 {
		t.Errorf("groupByBill(nil) = %+v, want none", got)
	}
}

// textsStepStore is a textsStore that records sync_state writes, for SyncBillTexts.
type textsStepStore struct {
	*textsStore

	successes []repository.SyncRun
	failures  []repository.SyncRun
}

func (f *textsStepStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.successes = append(f.successes, run)
	return nil
}

func (f *textsStepStore) RecordSyncFailure(_ context.Context, run repository.SyncRun) error {
	f.failures = append(f.failures, run)
	return nil
}

// The limit is a batch size, not a cut of a listing: a limited run that worked records success.
// A failed queue query fails the step.
func TestSyncBillTexts_RecordsTheStep(t *testing.T) {
	tests := []struct {
		name    string
		store   *textsStore
		wantErr string
	}{
		{name: "an empty queue succeeds", store: &textsStore{}},
		{name: "a failed queue query fails", store: &textsStore{queryErr: errFakeStore},
			wantErr: "query unfetched text versions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &textsStepStore{textsStore: tt.store}
			svc := &Service{store: store, logger: slog.New(slog.DiscardHandler)}

			err := svc.SyncBillTexts(t.Context(), 119, 10)
			if tt.wantErr == "" {
				if err != nil || len(store.successes) != 1 || store.successes[0].Step != stepTexts {
					t.Errorf(
						"SyncBillTexts = %v, successes %+v; want nil and one %s run",
						err,
						store.successes,
						stepTexts,
					)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("SyncBillTexts = %v, want an error containing %q", err, tt.wantErr)
			}
			if len(store.failures) != 1 || len(store.successes) != 0 {
				t.Errorf("successes %+v, failures %+v; want one failure", store.successes, store.failures)
			}
		})
	}
}

// A version whose text can't be fetched or stored is logged and stays queued for the next run;
// it doesn't fail the step.
func TestSyncBillTexts_VersionFailuresStayQueued(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "fail.htm") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("SEC. 1. SHORT TITLE.\nThis Act is the New Act.\n"))
	}))
	t.Cleanup(srv.Close)
	ok := unfetchedText(t, srv.URL, "hr-119-1", "ih")
	tests := []struct {
		name    string
		version repository.TextVersionRef
		store   *textsStore
		wantErr string
	}{
		{
			name:    "formats that aren't JSON",
			version: repository.TextVersionRef{ID: "v", BillID: "hr-119-1", Formats: json.RawMessage(`"pdf"`)},
			wantErr: "parsing formats JSONB",
		},
		{
			name: "no XML or text format",
			version: repository.TextVersionRef{ID: "v", BillID: "hr-119-1",
				Formats: json.RawMessage(`[{"type":"PDF","url":"https://example.test/a.pdf"}]`)},
			wantErr: "no supported text format found in 1 formats",
		},
		{
			name:    "a failed download",
			version: unfetchedText(t, srv.URL, "hr-119-1", "fail"),
			wantErr: "fetch text content",
		},
		{
			name:    "a failed insert",
			version: ok,
			store:   &textsStore{insertErr: errFakeStore},
			wantErr: "insert bill text",
		},
		{
			name:    "a failed refetch",
			version: repository.TextVersionRef{ID: ok.ID, BillID: ok.BillID, Formats: ok.Formats, Refetch: true},
			store:   &textsStore{refetchErr: errFakeStore},
			wantErr: "store refetched bill text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := cmp.Or(tt.store, &textsStore{})
			store.sections = map[string]json.RawMessage{}
			store.unfetched = []repository.TextVersionRef{tt.version}
			svc, logs := textsService(store)
			svc.http = srv.Client()

			if total, err := svc.syncBillTexts(t.Context(), 119, 0); err != nil || total != 0 {
				t.Fatalf("syncBillTexts = %d, %v; want 0, nil", total, err)
			}
			if len(store.stored) != 0 || len(store.lawRefs) != 0 {
				t.Errorf("stored %d texts and law refs %v, want none", len(store.stored), store.lawRefs)
			}
			checkLogs(t, logs, `level=WARN msg="fetch text failed"`, tt.wantErr)
		})
	}
}

// A text that's stored but can't be diffed counts as synced: the failure is logged, and the
// diff sweep computes the missing pair later.
func TestSyncBillTexts_DiffFailuresKeepTheText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("SEC. 1. SHORT TITLE.\nThis Act is the New Act.\n"))
	}))
	t.Cleanup(srv.Close)
	tests := []struct {
		name    string
		store   *textsStore
		wantErr string
	}{
		{name: "previous version lookup", store: &textsStore{previousErr: errFakeStore},
			wantErr: "find previous version"},
		{name: "previous sections unreadable", store: &textsStore{
			previous: &repository.PreviousVersionInfo{VersionID: "ih", Sections: json.RawMessage(`{`)},
		}, wantErr: "unmarshal old sections"},
		{name: "diff insert", store: &textsStore{
			previous: &repository.PreviousVersionInfo{VersionID: "ih", Sections: json.RawMessage(sectionsOld)},
			diffErr:  errFakeStore,
		}, wantErr: "insert bill text diff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			store.sections = map[string]json.RawMessage{}
			store.unfetched = []repository.TextVersionRef{unfetchedText(t, srv.URL, "hr-119-1", "rh")}
			svc, logs := textsService(store)
			svc.http = srv.Client()

			if total, err := svc.syncBillTexts(t.Context(), 119, 0); err != nil || total != 1 {
				t.Fatalf("syncBillTexts = %d, %v; want 1, nil", total, err)
			}
			if len(store.stored) != 1 || len(store.diffs) != 0 {
				t.Errorf("stored %d texts and diffs %+v; want the text and no diff", len(store.stored), store.diffs)
			}
			checkLogs(t, logs, `level=WARN msg="compute diff failed"`, tt.wantErr)
		})
	}
}

// The sweep counts a pair whose sections can't be loaded, at either end, as failed and goes on.
func TestSweepDiffs_UnloadableSectionsFail(t *testing.T) {
	store := &textsStore{
		sections: map[string]json.RawMessage{"ih": json.RawMessage(sectionsOld), "bad": json.RawMessage(`[`)},
		missing: []repository.DiffPair{
			{BillID: "hr-119-1", FromVersionID: "gone", ToVersionID: "ih"},
			{BillID: "hr-119-2", FromVersionID: "ih", ToVersionID: "gone"},
			{BillID: "hr-119-3", FromVersionID: "ih", ToVersionID: "bad"},
		},
	}
	svc, logs := textsService(store)

	if err := svc.sweepDiffs(t.Context(), 0); err != nil {
		t.Fatalf("sweepDiffs: %v", err)
	}
	for _, want := range []string{
		"load from sections", "load to sections", "unmarshal new sections",
		`msg="diff sweep done" deleted_diffs=0 deleted_summaries=0 missing_pairs=3 computed=0 empty=0 failed=3`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
}

// checkLogs fails the test for each of wants that logs lacks.
func checkLogs(t *testing.T, logs *bytes.Buffer, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
}
