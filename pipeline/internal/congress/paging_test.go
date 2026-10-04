package congress_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// fixtureServer serves the recorded pages in testdata. A request for
// /bill/119/hr/1/amendments at offset 250 gets hr-119-1-amendments-250.json; anything
// without a fixture is a 404. Every request must ask for 250 items.
func fixtureServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		q := r.URL.Query()
		if got := q.Get("limit"); got != "250" {
			t.Errorf("%s: limit = %q, want 250", r.URL.Path, got)
		}
		// /bill/119/hr/1/amendments → hr-119-1-amendments
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/bill/"), "/")
		const pathParts = 4
		if len(parts) != pathParts {
			http.NotFound(w, r)
			return
		}
		name := fmt.Sprintf("%s-%s-%s-%s-%s.json", parts[1], parts[0], parts[2], parts[3], q.Get("offset"))
		body, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func TestClient_SubResourcesFromRecordedPages(t *testing.T) {
	srv, requests := fixtureServer(t)
	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)
	ctx := context.Background()

	tests := []struct {
		name     string
		fetch    func() (int, error)
		want     int
		requests int32
	}{
		{"HR 1 actions", func() (int, error) {
			resp, err := client.GetBillActions(ctx, 119, "hr", 1)
			return lenOr(resp, err, func() int { return len(resp.Actions) })
		}, 59, 1},
		{"HR 1 amendments (two pages)", func() (int, error) {
			resp, err := client.GetBillAmendments(ctx, 119, "hr", 1)
			return lenOr(resp, err, func() int { return len(resp.Amendments) })
		}, 493, 2},
		{"HR 1 subjects", func() (int, error) {
			resp, err := client.GetBillSubjects(ctx, 119, "hr", 1)
			return lenOr(resp, err, func() int { return len(resp.Subjects.LegislativeSubjects) })
		}, 239, 1},
		{"HR 1 related bills (count says 39)", func() (int, error) {
			resp, err := client.GetBillRelatedBills(ctx, 119, "hr", 1)
			return lenOr(resp, err, func() int { return len(resp.RelatedBills) })
		}, 35, 1},
		{"HR 82 (118th) cosponsors (two pages)", func() (int, error) {
			resp, err := client.GetBillCosponsors(ctx, 118, "hr", 82)
			return lenOr(resp, err, func() int { return len(resp.Cosponsors) })
		}, 330, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := requests.Load()
			got, err := tt.fetch()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %d items, want %d", got, tt.want)
			}
			if n := requests.Load() - before; n != tt.requests {
				t.Errorf("made %d requests, want %d", n, tt.requests)
			}
		})
	}
}

func lenOr[R any](resp *R, err error, n func() int) (int, error) {
	if err != nil || resp == nil {
		return 0, err
	}
	return n(), nil
}

func TestClient_GetBillAmendments_KeepsOrderAcrossPages(t *testing.T) {
	srv, _ := fixtureServer(t)
	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)

	resp, err := client.GetBillAmendments(context.Background(), 119, "hr", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body, err := os.ReadFile(filepath.Join("testdata", "hr-119-1-amendments-250.json"))
	if err != nil {
		t.Fatal(err)
	}
	var page2 congress.AmendmentsResponse
	if err = json.Unmarshal(body, &page2); err != nil {
		t.Fatal(err)
	}
	// The first item of the second page lands at index 250, and the last item last.
	if got, want := resp.Amendments[250], page2.Amendments[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("amendment 250 = %+v, want the first item of page 2 %+v", got, want)
	}
	if got, want := resp.Amendments[492], page2.Amendments[242]; !reflect.DeepEqual(got, want) {
		t.Errorf("amendment 492 = %+v, want the last item of page 2 %+v", got, want)
	}
}

func TestClient_GetBillSubjects_KeepsPolicyArea(t *testing.T) {
	srv, _ := fixtureServer(t)
	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)

	resp, err := client.GetBillSubjects(context.Background(), 119, "hr", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pa := resp.Subjects.PolicyArea; pa == nil || pa.Name != "Economics and Public Finance" {
		t.Errorf("policy area = %+v, want Economics and Public Finance", pa)
	}
}

// pagedServer answers every sub-resource request with page(offset). A nil body is a 500.
func pagedServer(t *testing.T, page func(offset int) []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
		if err != nil {
			t.Errorf("%s: bad offset %q", r.URL.Path, r.URL.Query().Get("offset"))
		}
		body := page(offset)
		if body == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

// actionsPage is a page of n actions, with a next link when more is true.
func actionsPage(offset, n int, more bool) []byte {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf(`{"actionDate":"2025-01-01","text":"action %d"}`, offset+i)
	}
	next := ""
	if more {
		next = `"next": "https://api.congress.gov/v3/bill/119/hr/1/actions?offset=250&limit=250&format=json",`
	}
	return fmt.Appendf(nil, `{"pagination": {%s "count": 999}, "actions": [%s]}`, next, strings.Join(items, ","))
}

func TestClient_FetchAllStopsOnNextAndEmptyPages(t *testing.T) {
	tests := []struct {
		name      string
		page      func(offset int) []byte
		wantItems int
		wantLast  string
		wantReqs  int32
	}{
		{
			name: "three pages, next on the first two",
			page: func(offset int) []byte {
				if offset == 500 {
					return actionsPage(offset, 3, false)
				}
				return actionsPage(offset, 250, true)
			},
			wantItems: 503, wantLast: "action 502", wantReqs: 3,
		},
		{
			name: "a short page with next still continues",
			page: func(offset int) []byte {
				if offset == 0 {
					return actionsPage(offset, 17, true)
				}
				return actionsPage(offset, 19, false)
			},
			wantItems: 36, wantLast: "action 268", wantReqs: 2,
		},
		{
			name: "an empty page with next stops",
			page: func(offset int) []byte {
				if offset == 0 {
					return actionsPage(offset, 250, true)
				}
				return actionsPage(offset, 0, true)
			},
			wantItems: 250, wantLast: "action 249", wantReqs: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, requests := pagedServer(t, tt.page)
			client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)

			resp, err := client.GetBillActions(context.Background(), 119, "hr", 1)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.Actions) != tt.wantItems {
				t.Errorf("got %d actions, want %d", len(resp.Actions), tt.wantItems)
			}
			if got := resp.Actions[len(resp.Actions)-1].Text; got != tt.wantLast {
				t.Errorf("last action = %q, want %q (upstream order)", got, tt.wantLast)
			}
			if requests.Load() != tt.wantReqs {
				t.Errorf("made %d requests, want %d", requests.Load(), tt.wantReqs)
			}
		})
	}
}

func TestClient_FetchAllPageCap(t *testing.T) {
	srv, requests := pagedServer(t, func(offset int) []byte { return actionsPage(offset, 1, true) })
	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)

	resp, err := client.GetBillActions(context.Background(), 119, "hr", 1)
	if !errors.Is(err, congress.ErrPageCap) {
		t.Fatalf("err = %v, want ErrPageCap", err)
	}
	if resp != nil {
		t.Errorf("got %d actions, want none", len(resp.Actions))
	}
	if requests.Load() != 40 {
		t.Errorf("made %d requests, want 40", requests.Load())
	}
}

func TestClient_FetchAllPageErrorReturnsNothing(t *testing.T) {
	srv, _ := pagedServer(t, func(offset int) []byte {
		if offset == 0 {
			return actionsPage(offset, 250, true)
		}
		return nil
	})
	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)

	resp, err := client.GetBillActions(context.Background(), 119, "hr", 1)
	if err == nil {
		t.Fatal("expected an error for a failed second page")
	}
	if !strings.Contains(err.Error(), "offset 250") {
		t.Errorf("error %q doesn't name the failed page", err)
	}
	if resp != nil {
		t.Errorf("got %d actions, want none", len(resp.Actions))
	}
}

func TestClient_CommitteesAndTextVersionsPage(t *testing.T) {
	srv, _ := pagedServer(t, func(offset int) []byte {
		next := ""
		if offset == 0 {
			next = `"next": "x",`
		}
		return fmt.Appendf(nil, `{"pagination": {%s "count": 2},
			"committees": [{"systemCode": "c%d"}], "textVersions": [{"type": "t%d"}]}`, next, offset, offset)
	})
	client := congress.NewClientWithBaseURL(srv.Client(), srv.URL)
	ctx := context.Background()

	committees, err := client.GetBillCommittees(ctx, 119, "hr", 1)
	if err != nil {
		t.Fatalf("committees: %v", err)
	}
	if got := len(committees.Committees); got != 2 || committees.Committees[1].SystemCode != "c250" {
		t.Errorf("committees = %+v, want c0 then c250", committees.Committees)
	}

	versions, err := client.GetBillTextVersions(ctx, 119, "hr", 1)
	if err != nil {
		t.Fatalf("text versions: %v", err)
	}
	if got := len(versions.TextVersions); got != 2 || versions.TextVersions[1].Type != "t250" {
		t.Errorf("text versions = %+v, want t0 then t250", versions.TextVersions)
	}
}

func TestFixturesHoldNoKeys(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	for _, f := range files {
		body, readErr := os.ReadFile(f)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(strings.ToLower(string(body)), "api_key") {
			t.Errorf("%s contains api_key", f)
		}
	}
}
