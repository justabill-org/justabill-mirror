package fedreg_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/fedreg"
)

// newServer serves handler on an httptest server closed when the test ends, and returns a Client
// pointed at it.
func newServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *fedreg.Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, fedreg.NewClientWithBaseURL(srv.Client(), srv.URL)
}

func TestDocuments_PublishedOn(t *testing.T) {
	body, err := os.ReadFile("../sync/testdata/fedreg/day-2024-12-30.json")
	if err != nil {
		t.Fatal(err)
	}
	var got *http.Request
	_, client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write(body)
	})

	docs, err := client.Documents(t.Context(), fedreg.Query{
		PublishedOn: time.Date(2024, 12, 30, 0, 0, 0, 0, time.UTC),
		Types:       []string{fedreg.TypeRule, fedreg.TypeNotice, fedreg.TypeProposedRule},
	})
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}

	checkDayRequest(t, got)

	if len(docs) != 5 {
		t.Fatalf("got %d documents, want 5", len(docs))
	}
	i := slices.IndexFunc(docs, func(d fedreg.Document) bool { return d.DocumentNumber == "2024-29699" })
	if i < 0 {
		t.Fatal("document 2024-29699 not found")
	}
	checkOverdraftDoc(t, docs[i])
}

// checkDayRequest checks the request for one day's rules, notices and proposed rules.
func checkDayRequest(t *testing.T, got *http.Request) {
	t.Helper()
	if got.URL.Path != "/documents.json" {
		t.Errorf("path = %q, want /documents.json", got.URL.Path)
	}
	if a := got.Header.Get("Accept"); a != "application/json" {
		t.Errorf("Accept = %q, want application/json", a)
	}
	q := got.URL.Query()
	for key, want := range map[string]string{
		"conditions[publication_date][is]": "2024-12-30",
		"per_page":                         "1000",
		"order":                            "newest",
	} {
		if v := q.Get(key); v != want {
			t.Errorf("%s = %q, want %q", key, v, want)
		}
	}
	if types := q["conditions[type][]"]; !slices.Equal(types, []string{"RULE", "NOTICE", "PRORULE"}) {
		t.Errorf("conditions[type][] = %v, want [RULE NOTICE PRORULE]", types)
	}
	for _, f := range []string{"document_number", "abstract", "regulations_dot_gov_info", "docket_ids"} {
		if !slices.Contains(q["fields[]"], f) {
			t.Errorf("fields[] = %v, missing %q", q["fields[]"], f)
		}
	}
	for _, key := range []string{"conditions[term]", "conditions[publication_date][lte]", "conditions[publication_date][year]"} {
		if q.Has(key) {
			t.Errorf("%s = %q, want it absent", key, q.Get(key))
		}
	}
}

// checkOverdraftDoc checks 2024-29699 as day-2024-12-30.json records it.
func checkOverdraftDoc(t *testing.T, d fedreg.Document) {
	t.Helper()
	if d.Volume != 89 || d.StartPage != 106768 {
		t.Errorf("volume, start page = %d, %d; want 89, 106768", d.Volume, d.StartPage)
	}
	if d.Type != fedreg.DocRule {
		t.Errorf("type = %q, want %q", d.Type, fedreg.DocRule)
	}
	if len(d.Agencies) != 1 || d.Agencies[0].Name != "Consumer Financial Protection Bureau" {
		t.Errorf("agencies = %+v, want Consumer Financial Protection Bureau", d.Agencies)
	}
	if d.DocketID() != "CFPB-2024-0002" {
		t.Errorf("DocketID() = %q, want CFPB-2024-0002", d.DocketID())
	}
	if d.CorrectionOf != "" {
		t.Errorf("CorrectionOf = %q, want empty", d.CorrectionOf)
	}
}

func TestDocuments_Term(t *testing.T) {
	var got url.Values
	_, client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		fmt.Fprint(w, `{"count":1,"results":[{"document_number":"2024-1"}]}`)
	})

	docs, err := client.Documents(t.Context(), fedreg.Query{
		Term:        "Overdraft Lending",
		PublishedBy: time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC),
		Year:        2024,
		PerPage:     20,
	})
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if len(docs) != 1 {
		t.Errorf("got %d documents, want 1", len(docs))
	}
	for key, want := range map[string]string{
		"conditions[term]":                   `"Overdraft Lending"`,
		"conditions[publication_date][lte]":  "2025-02-01",
		"conditions[publication_date][year]": "2024",
		"per_page":                           "20",
	} {
		if v := got.Get(key); v != want {
			t.Errorf("%s = %q, want %q", key, v, want)
		}
	}
	for _, key := range []string{"conditions[publication_date][is]", "conditions[type][]"} {
		if got.Has(key) {
			t.Errorf("%s = %q, want it absent", key, got[key])
		}
	}
}

func TestDocuments_PerPageClamped(t *testing.T) {
	for _, perPage := range []int{-1, 5000} {
		var got string
		_, client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Query().Get("per_page")
			fmt.Fprint(w, `{"count":0}`)
		})
		if _, err := client.Documents(t.Context(), fedreg.Query{PerPage: perPage}); err != nil {
			t.Fatalf("PerPage %d: %v", perPage, err)
		}
		if got != "1000" {
			t.Errorf("PerPage %d: per_page = %q, want 1000", perPage, got)
		}
	}
}

func TestDocuments_NoResults(t *testing.T) {
	_, client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"count":0,"description":"Documents matching nothing"}`)
	})
	docs, err := client.Documents(t.Context(), fedreg.Query{Term: "nothing"})
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("got %d documents, want none", len(docs))
	}
}

// pagedServer serves numbered pages of one document each; every page up to last links to the
// next on nextOrigin (the server itself when empty). It records the pages asked for.
func pagedServer(t *testing.T, last int, nextOrigin string) (*fedreg.Client, *[]string) {
	t.Helper()
	var asked []string
	var srv *httptest.Server
	srv, client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := r.URL.Query().Get("page")
		if n == "" {
			n = "1"
		}
		asked = append(asked, n)
		var num int
		if _, err := fmt.Sscan(n, &num); err != nil {
			http.Error(w, "bad page", http.StatusBadRequest)
			return
		}
		p := map[string]any{"count": last, "results": []map[string]any{{"document_number": "doc-" + n}}}
		if num < last {
			origin := nextOrigin
			if origin == "" {
				origin = srv.URL
			}
			p["next_page_url"] = fmt.Sprintf("%s/documents.json?page=%d&per_page=1", origin, num+1)
		}
		_ = json.NewEncoder(w).Encode(p)
	})
	return client, &asked
}

func docNumbers(docs []fedreg.Document) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.DocumentNumber)
	}
	return out
}

func TestDocuments_Paging(t *testing.T) {
	client, asked := pagedServer(t, 2, "")
	docs, err := client.Documents(t.Context(), fedreg.Query{Term: "x"})
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if got := docNumbers(docs); !slices.Equal(got, []string{"doc-1", "doc-2"}) {
		t.Errorf("documents = %v, want [doc-1 doc-2]", got)
	}
	if !slices.Equal(*asked, []string{"1", "2"}) {
		t.Errorf("pages asked = %v, want [1 2]", *asked)
	}
}

func TestDocuments_NextPageOtherOrigin(t *testing.T) {
	client, asked := pagedServer(t, 2, "https://evil.example")
	docs, err := client.Documents(t.Context(), fedreg.Query{Term: "x"})
	if err == nil || !strings.Contains(err.Error(), "unexpected origin") {
		t.Errorf("err = %v, want an unexpected origin error", err)
	}
	if docs != nil {
		t.Errorf("documents = %v, want none", docNumbers(docs))
	}
	if len(*asked) != 1 {
		t.Errorf("pages asked = %v, want only the first", *asked)
	}
}

func TestDocuments_NextPageUnparseable(t *testing.T) {
	_, client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"results":[{"document_number":"a"}],"next_page_url":"http://[::1"}`)
	})
	docs, err := client.Documents(t.Context(), fedreg.Query{Term: "x"})
	if err == nil || !strings.Contains(err.Error(), "unparseable next_page_url") {
		t.Errorf("err = %v, want an unparseable next_page_url error", err)
	}
	if docs != nil {
		t.Errorf("documents = %v, want none", docNumbers(docs))
	}
}

func TestDocuments_NextPageWithoutResultsStops(t *testing.T) {
	calls := 0
	_, client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		fmt.Fprint(w, `{"count":0,"next_page_url":"https://evil.example/documents.json?page=2"}`)
	})
	docs, err := client.Documents(t.Context(), fedreg.Query{Term: "x"})
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if len(docs) != 0 || calls != 1 {
		t.Errorf("documents = %v after %d calls, want none after 1", docNumbers(docs), calls)
	}
}

func TestDocuments_PageCap(t *testing.T) {
	tests := []struct {
		name     string
		truncate bool
		want     []string
	}{
		{name: "all or nothing", want: nil},
		{name: "truncate", truncate: true, want: []string{"doc-1", "doc-2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, asked := pagedServer(t, 100, "")
			docs, err := client.Documents(t.Context(), fedreg.Query{Term: "x", MaxPages: 2, Truncate: tt.truncate})
			if !errors.Is(err, fedreg.ErrPageCap) {
				t.Errorf("err = %v, want ErrPageCap", err)
			}
			if got := docNumbers(docs); (docs == nil) != (tt.want == nil) || !slices.Equal(got, tt.want) {
				t.Errorf("documents = %v, want %v", got, tt.want)
			}
			if len(*asked) != 2 {
				t.Errorf("pages asked = %v, want 2", *asked)
			}
		})
	}
}

func TestDocuments_DefaultPageCap(t *testing.T) {
	client, asked := pagedServer(t, 100, "")
	_, err := client.Documents(t.Context(), fedreg.Query{Term: "x"})
	if !errors.Is(err, fedreg.ErrPageCap) {
		t.Errorf("err = %v, want ErrPageCap", err)
	}
	if len(*asked) != 5 {
		t.Errorf("read %d pages, want the default 5", len(*asked))
	}
}

func TestDocuments_Failures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "503",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "down", http.StatusServiceUnavailable)
			},
			want: "503",
		},
		{
			name: "malformed JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"results":[{"document_number":`)
			},
			want: "decode response",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := newServer(t, tt.handler)
			docs, err := client.Documents(t.Context(), fedreg.Query{Term: "x"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one containing %q", err, tt.want)
			}
			if docs != nil {
				t.Errorf("documents = %v, want none", docNumbers(docs))
			}
		})
	}
}

func TestDocuments_SecondPageFails(t *testing.T) {
	var srv *httptest.Server
	srv, client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"results":[{"document_number":"a"}],"next_page_url":%q}`, srv.URL+"/documents.json?page=2")
	})
	docs, err := client.Documents(t.Context(), fedreg.Query{Term: "x"})
	if err == nil || !strings.Contains(err.Error(), "page 2") {
		t.Errorf("err = %v, want a page 2 error", err)
	}
	if docs != nil {
		t.Errorf("documents = %v, want none", docNumbers(docs))
	}
}

func TestDocuments_BadBaseURL(t *testing.T) {
	client := fedreg.NewClientWithBaseURL(http.DefaultClient, "http://[::1")
	if _, err := client.Documents(t.Context(), fedreg.Query{Term: "x"}); err == nil {
		t.Error("want an error for an unparseable base URL")
	}
}

func TestDocuments_RequestFails(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	client := fedreg.NewClientWithBaseURL(srv.Client(), srv.URL)
	srv.Close()
	if _, err := client.Documents(t.Context(), fedreg.Query{Term: "x"}); err == nil {
		t.Error("want an error from a closed server")
	}
}

func TestDocuments_BadRequestURL(t *testing.T) {
	client := fedreg.NewClientWithBaseURL(http.DefaultClient, "ht tp://x")
	if _, err := client.Documents(t.Context(), fedreg.Query{Term: "x"}); err == nil {
		t.Error("want an error for a base URL NewRequest rejects")
	}
}

func TestNewClient(t *testing.T) {
	if fedreg.NewClient(http.DefaultClient) == nil {
		t.Error("NewClient returned nil")
	}
}

func TestDocument_DocketID(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "regulations.gov docket",
			doc:  `{"docket_ids":["Docket No. CFPB-2024-0002"],"regulations_dot_gov_info":{"docket_id":"CFPB-2024-0002"}}`,
			want: "CFPB-2024-0002",
		},
		{
			name: "empty object falls back to docket_ids",
			doc:  `{"docket_ids":["ED-2024-OPE-0069","other"],"regulations_dot_gov_info":{}}`,
			want: "ED-2024-OPE-0069",
		},
		{
			name: "no docket_id falls back to docket_ids",
			doc:  `{"docket_ids":["ED-2024-OPE-0069"],"regulations_dot_gov_info":{"agency_id":"ED"}}`,
			want: "ED-2024-OPE-0069",
		},
		{
			name: "missing info falls back to docket_ids",
			doc:  `{"docket_ids":["ED-2024-OPE-0069"]}`,
			want: "ED-2024-OPE-0069",
		},
		{name: "neither", doc: `{"docket_ids":[],"regulations_dot_gov_info":{}}`, want: ""},
		{name: "null info", doc: `{"regulations_dot_gov_info":null}`, want: ""},
		{name: "array info", doc: `{"regulations_dot_gov_info":[]}`, want: ""},
		{
			name: "array info falls back",
			doc:  `{"docket_ids":["COE-2025-0006"],"regulations_dot_gov_info":[]}`, want: "COE-2025-0006",
		},
		{
			name: "label dropped",
			doc:  `{"docket_ids":["OMB Control No. 2900-0020","Docket ID: COE-2025-0006"]}`, want: "COE-2025-0006",
		},
		{name: "FCC docket isn't on Regulations.gov", doc: `{"docket_ids":["GN Docket No. 18-122"]}`, want: ""},
		{name: "several FCC dockets", doc: `{"docket_ids":["WC Docket Nos. 23-320, 17-108"]}`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d fedreg.Document
			if err := json.Unmarshal([]byte(tt.doc), &d); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := d.DocketID(); got != tt.want {
				t.Errorf("DocketID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDocument_DocketIDZeroValue(t *testing.T) {
	if got := (fedreg.Document{}).DocketID(); got != "" {
		t.Errorf("DocketID() = %q, want empty", got)
	}
}
