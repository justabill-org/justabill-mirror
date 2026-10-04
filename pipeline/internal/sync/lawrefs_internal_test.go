package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
)

const (
	lawRefsXML = `<bill><legis-body><section id="S2"><text>Section 8103(a) of title 5, United States Code, is
		amended by striking “a”.</text></section></legis-body></bill>`
	lawRefsMODS = `<mods xmlns="http://www.loc.gov/mods/v3"><extension>
		<USCode title="5"><section number="8103" detail="(a)"/></USCode>
		<USCode title="15"><section number="9401" detail="(3)"/></USCode></extension></mods>`
)

// lawRefsStore serves stored texts to BackfillLawRefs and records its writes and checkpoints.
// Any other PipelineStore method panics through the nil embedded interface.
type lawRefsStore struct {
	repository.PipelineStore

	sources     []repository.LawRefSource
	state       *repository.SyncStateRow
	checkpoints []string
	listAfters  []string
	written     map[string][]repository.BillLawRefRow // by version ID
	failVersion string
	failures    int
}

func (f *lawRefsStore) GetSyncState(context.Context, string, int) (*repository.SyncStateRow, error) {
	return f.state, nil
}

func (f *lawRefsStore) SaveSyncCheckpoint(_ context.Context, step string, congress int, offset *string, n int) error {
	f.state = &repository.SyncStateRow{Step: step, Congress: congress, LastOffset: offset, ItemsSynced: n}
	f.checkpoints = append(f.checkpoints, *offset)
	return nil
}

func (f *lawRefsStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.state = &repository.SyncStateRow{Step: run.Step, Congress: run.Congress, ItemsSynced: run.ItemsSynced}
	f.checkpoints = append(f.checkpoints, "done")
	return nil
}

func (f *lawRefsStore) RecordSyncFailure(context.Context, repository.SyncRun) error {
	f.failures++
	return nil
}

func (f *lawRefsStore) ListLawRefSources(
	_ context.Context, _ int, afterBill, afterVersion string, limit int,
) ([]repository.LawRefSource, error) {
	f.listAfters = append(f.listAfters, afterBill+"/"+afterVersion)
	i := 0
	for i < len(f.sources) && (f.sources[i].BillID < afterBill ||
		f.sources[i].BillID == afterBill && f.sources[i].VersionID <= afterVersion) {
		i++
	}
	return f.sources[i:min(i+limit, len(f.sources))], nil
}

func (f *lawRefsStore) ReplaceBillLawRefs(
	_ context.Context, _, versionID string, rows []repository.BillLawRefRow,
) error {
	if versionID == f.failVersion {
		return errFakeStore
	}
	if f.written == nil {
		f.written = map[string][]repository.BillLawRefRow{}
	}
	f.written[versionID] = rows
	return nil
}

func lawRefSource(bill, version string) repository.LawRefSource {
	formats, _ := json.Marshal([]textFormat{
		{Type: formatText, URL: "https://www.congress.gov/119/bills/" + version + "/BILLS-119" + version + ".htm"},
		{Type: formatXML, URL: "https://www.congress.gov/119/bills/" + version + "/BILLS-119" + version + ".xml"},
	})
	return repository.LawRefSource{
		BillID: bill, VersionID: version, VersionCode: "ih", Formats: formats, Format: formatXML, Content: lawRefsXML,
	}
}

// modsServer serves lawRefsMODS for every package but BILLS-119hr5ih, and records the paths.
func modsServer(t *testing.T) (*govinfo.Client, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.Contains(r.URL.Path, "hr5ih") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(lawRefsMODS))
	}))
	t.Cleanup(srv.Close)
	return govinfo.NewClientWithBaseURL(srv.Client(), srv.URL), &paths
}

func sectionKinds(rows []repository.BillLawRefRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.SectionID+" "+r.RefKind)
	}
	return out
}

func TestBillsPackageID(t *testing.T) {
	for name, tc := range map[string]struct{ formats, want string }{
		"xml":        {`[{"type":"Formatted XML","url":"https://www.congress.gov/119/bills/hr1/BILLS-119hr1ih.xml"}]`, "BILLS-119hr1ih"},
		"text first": {`[{"type":"Formatted Text","url":"https://x.test/BILLS-119s5rs.htm"}]`, "BILLS-119s5rs"},
		"public law": {`[{"type":"Formatted Text","url":"https://x.test/PLAW-119publ1.htm"}]`, ""},
		"none":       {`[]`, ""},
		"bad json":   {`{`, ""},
	} {
		if got := billsPackageID(json.RawMessage(tc.formats)); got != tc.want {
			t.Errorf("%s: billsPackageID = %q, want %q", name, got, tc.want)
		}
	}
}

func TestStoreLawRefs(t *testing.T) {
	client, paths := modsServer(t)
	plain := lawRefSource("hr-119-3", "hr3ih")
	plain.Format, plain.Content = formatText, "SEC. 1. Section 8103 of title 5 is amended."
	badXML := lawRefSource("hr-119-4", "hr4ih")
	badXML.Content = "<bill><legis-body"
	broken := lawRefSource("hr-119-5", "hr5ih")

	for _, tc := range []struct {
		name    string
		src     repository.LawRefSource
		govinfo *govinfo.Client
		want    []string
		counts  lawRefCounts
	}{
		{
			name: "XML plus MODS", src: lawRefSource("hr-119-1", "hr1ih"), govinfo: client,
			want:   []string{"/us/usc/t15/s9401 cites", "/us/usc/t5/s8103 amends"},
			counts: lawRefCounts{versions: 1, refs: 2, fromMODS: 1},
		},
		{
			name: "XML without a GovInfo client", src: lawRefSource("hr-119-2", "hr2ih"),
			want:   []string{"/us/usc/t5/s8103 amends"},
			counts: lawRefCounts{versions: 1, refs: 1},
		},
		{
			name: "plain text gets MODS only", src: plain, govinfo: client,
			want:   []string{"/us/usc/t15/s9401 cites", "/us/usc/t5/s8103 cites"},
			counts: lawRefCounts{versions: 1, refs: 2, fromMODS: 2},
		},
		{
			name: "unreadable XML gets MODS only", src: badXML, govinfo: client,
			want:   []string{"/us/usc/t15/s9401 cites", "/us/usc/t5/s8103 cites"},
			counts: lawRefCounts{versions: 1, refs: 2, fromMODS: 2, badXML: 1},
		},
		{
			name: "failed MODS keeps the XML's", src: broken, govinfo: client,
			want:   []string{"/us/usc/t5/s8103 amends"},
			counts: lawRefCounts{versions: 1, refs: 1, modsFailed: 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &lawRefsStore{}
			svc := newBackfillService(store)
			svc.govinfo = tc.govinfo
			var c lawRefCounts
			if err := svc.storeLawRefs(t.Context(), tc.src, &c); err != nil {
				t.Fatalf("storeLawRefs: %v", err)
			}
			if got := sectionKinds(store.written[tc.src.VersionID]); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rows = %q, want %q", got, tc.want)
			}
			if c != tc.counts {
				t.Errorf("counts = %+v, want %+v", c, tc.counts)
			}
		})
	}
	if want := "/packages/BILLS-119hr1ih/mods"; len(*paths) == 0 || (*paths)[0] != want {
		t.Errorf("MODS requests = %q, want the first to be %s", *paths, want)
	}
}

func TestStoreLawRefsRowFields(t *testing.T) {
	store := &lawRefsStore{}
	svc := newBackfillService(store)
	if err := svc.storeLawRefs(t.Context(), lawRefSource("hr-119-1", "hr1ih"), &lawRefCounts{}); err != nil {
		t.Fatal(err)
	}
	rows := store.written["hr1ih"]
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if *r.CiteText != "Section 8103(a) of title 5, United States Code" || *r.SubsectionPath != "(a)" ||
		*r.BillSectionRef != "S2" || !strings.HasPrefix(*r.Instruction, "Section 8103(a) of title 5") {
		t.Errorf("row = cite %q, path %q, section %q, instruction %q",
			*r.CiteText, *r.SubsectionPath, *r.BillSectionRef, *r.Instruction)
	}
	if optional("") != nil {
		t.Error(`optional("") isn't nil`)
	}
}

func TestBackfillLawRefsPagesAndCheckpoints(t *testing.T) {
	store := &lawRefsStore{sources: []repository.LawRefSource{
		lawRefSource("hr-119-1", "a"), lawRefSource("hr-119-1", "b"),
		lawRefSource("hr-119-2", "c"), lawRefSource("s-119-1", "d"), lawRefSource("s-119-1", "e"),
	}}
	svc := newBackfillService(store)

	if err := svc.BackfillLawRefs(t.Context(), 119, 2); err != nil {
		t.Fatalf("BackfillLawRefs: %v", err)
	}
	if len(store.written) != 5 {
		t.Errorf("wrote %d versions, want 5", len(store.written))
	}
	wantCheckpoints := []string{"hr-119-1 b", "s-119-1 d", "done"}
	if !reflect.DeepEqual(store.checkpoints, wantCheckpoints) {
		t.Errorf("checkpoints = %q, want %q", store.checkpoints, wantCheckpoints)
	}
	if store.state.ItemsSynced != 5 {
		t.Errorf("items synced = %d, want 5", store.state.ItemsSynced)
	}
}

func TestBackfillLawRefsResumes(t *testing.T) {
	offset := "hr-119-1 b"
	store := &lawRefsStore{
		sources: []repository.LawRefSource{
			lawRefSource("hr-119-1", "a"), lawRefSource("hr-119-1", "b"), lawRefSource("hr-119-2", "c"),
		},
		state: &repository.SyncStateRow{Step: stepLawRefs, Congress: 119, LastOffset: &offset, ItemsSynced: 2},
	}
	svc := newBackfillService(store)

	if err := svc.BackfillLawRefs(t.Context(), 119, 0); err != nil {
		t.Fatalf("BackfillLawRefs: %v", err)
	}
	if _, ok := store.written["c"]; !ok || len(store.written) != 1 {
		t.Errorf("wrote %v, want only version c", store.written)
	}
	if store.listAfters[0] != "hr-119-1/b" || store.state.ItemsSynced != 3 {
		t.Errorf("first list after %q, items %d; want hr-119-1/b and 3", store.listAfters[0], store.state.ItemsSynced)
	}
}

func TestBackfillLawRefsStopsOnWriteError(t *testing.T) {
	store := &lawRefsStore{
		sources:     []repository.LawRefSource{lawRefSource("hr-119-1", "a"), lawRefSource("hr-119-2", "b")},
		failVersion: "b",
	}
	svc := newBackfillService(store)

	err := svc.BackfillLawRefs(t.Context(), 119, 10)
	if !errors.Is(err, errFakeStore) || store.failures != 1 {
		t.Errorf("BackfillLawRefs = %v with %d failures recorded, want the store error recorded", err, store.failures)
	}
}

func TestBackfillLawRefsStopsWhenCanceled(t *testing.T) {
	store := &lawRefsStore{sources: []repository.LawRefSource{lawRefSource("hr-119-1", "a")}}
	svc := newBackfillService(store)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := svc.BackfillLawRefs(ctx, 119, 10); !errors.Is(err, context.Canceled) || len(store.written) != 0 {
		t.Errorf("BackfillLawRefs = %v after writing %d, want context.Canceled and nothing", err, len(store.written))
	}
}
