package congress_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// summariesServer answers /summaries/119 with the recorded page in testdata/summaries/<name> and
// hands each request's query to check.
func summariesServer(t *testing.T, name string, check func(q map[string][]string)) *congress.Client {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "summaries", name))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/summaries/119" {
			http.NotFound(w, r)
			return
		}
		if check != nil {
			check(r.URL.Query())
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return congress.NewClientWithBaseURL(srv.Client(), srv.URL)
}

func TestListSummaries_RequestAndDecode(t *testing.T) {
	from := time.Date(2026, 9, 21, 13, 0, 0, 0, time.FixedZone("EDT", -4*3600))
	to := time.Date(2026, 9, 21, 19, 0, 0, 0, time.UTC)
	client := summariesServer(t, "119-sconres-lists.json", func(q map[string][]string) {
		want := map[string]string{
			"fromDateTime": "2026-09-21T17:00:00Z", "toDateTime": "2026-09-21T19:00:00Z",
			"sort": "updateDate asc", "limit": "250", "offset": "500", "format": "json",
		}
		for k, v := range want {
			if got := q[k]; len(got) != 1 || got[0] != v {
				t.Errorf("query %s = %q, want %q", k, got, v)
			}
		}
	})

	page, err := client.ListSummaries(context.Background(), 119, from, to, 500)
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if page.HasNext || page.Items() != 2 || len(page.Malformed) != 0 {
		t.Fatalf("page = %d items, %d malformed, next %v; want 2, 0, false",
			page.Items(), len(page.Malformed), page.HasNext)
	}
	s := page.Summaries[0]
	if s.Bill.Congress != 119 || s.Bill.Type != "SCONRES" || s.Bill.Number != "38" || s.VersionCode != "00" ||
		s.ActionDate != "2026-08-07" || s.ActionDesc != "Introduced in Senate" || s.CurrentChamber != "Senate" ||
		s.LastSummaryUpdateDate != "2026-09-21T18:10:02Z" || s.UpdateDate != "2026-09-21T18:11:18Z" {
		t.Errorf("first summary = %+v", s)
	}
	if len(s.Text) < 1000 {
		t.Errorf("text is %d bytes, want the whole summary", len(s.Text))
	}
}

func TestListSummaries_PagingSignals(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		name    string
		items   int
		hasNext bool
	}{
		{"119-with-next.json", 3, true},
		{"119-empty.json", 0, false},
	} {
		page, err := summariesServer(t, tt.name, nil).ListSummaries(context.Background(), 119, now, now, 0)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if page.Items() != tt.items || page.HasNext != tt.hasNext {
			t.Errorf("%s: %d items, next %v; want %d, %v", tt.name, page.Items(), page.HasNext, tt.items, tt.hasNext)
		}
	}
}

func TestListSummaries_MalformedItemKeepsThePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pagination":{"count":2},"summaries":[
			{"bill":{"congress":"one hundred nineteen","type":"HR","number":"1"},"versionCode":"00"},
			{"bill":{"congress":119,"type":"HR","number":"2"},"versionCode":"00","text":"<p>ok</p>"}]}`))
	}))
	defer srv.Close()
	page, err := congress.NewClientWithBaseURL(srv.Client(), srv.URL).
		ListSummaries(context.Background(), 119, time.Now(), time.Now(), 0)
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(page.Summaries) != 1 || page.Summaries[0].Bill.Number != "2" || len(page.Malformed) != 1 {
		t.Errorf("page = %+v, want summary 2 and one malformed item", page)
	}
}

func TestListSummaries_Errors(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"json":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"summaries": [`)) },
	} {
		srv := httptest.NewServer(handler)
		_, err := congress.NewClientWithBaseURL(srv.Client(), srv.URL).
			ListSummaries(context.Background(), 119, time.Now(), time.Now(), 250)
		srv.Close()
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestBillSummaries(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "summaries", "s-119-1003-bill.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bill/119/s/1003/summaries" || r.URL.Query().Get("limit") != "250" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)

	got, err := client.BillSummaries(t.Context(), 119, "s", 1003)
	if err != nil {
		t.Fatalf("BillSummaries: %v", err)
	}
	if len(got) != 2 || got[1].VersionCode != "49" || got[1].ActionDesc != "Public Law" ||
		got[1].ActionDate != "2026-06-26" || got[1].UpdateDate != "2026-06-30T12:39:56Z" ||
		!strings.HasPrefix(got[0].Text, "<p><strong>Lulu’s Law</strong></p>") {
		t.Errorf("BillSummaries = %+v", got)
	}

	if _, err = client.BillSummaries(t.Context(), 119, "hr", 1); err == nil {
		t.Error("BillSummaries on a 404: no error")
	}
}
