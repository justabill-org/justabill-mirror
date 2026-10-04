package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

// cardRepo is a page of two bills: hr-119-187 has every card fact and a summary, s-119-9 has
// neither.
func cardRepo() *mockBillRepo {
	short := "Names a post office."
	roll, yeas, nays, present, notVoting := 19, 413, 0, 0, 19
	question, result := "On Motion to Suspend the Rules and Pass", "Passed"
	lawType, lawNumber := model.LawTypePublic, "119-62"
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	return &mockBillRepo{
		listResult: &model.ListResult[model.Bill]{
			Items: []model.Bill{{ID: "hr-119-187", Title: "One"}, {ID: "s-119-9", Title: "Two"}},
			Total: 12, Offset: 0, Limit: 2,
		},
		summaries: map[string]model.BillSummary{"hr-119-187": {BillID: "hr-119-187", ShortSummary: &short}},
		cards: map[string]model.BillCardFacts{"hr-119-187": {
			CRS: &model.CardCRS{
				VersionCode: "49", ActionDate: day(time.March, 2), ActionDesc: "Public Law", Lead: "This act names…",
			},
			Passage: []model.PassageEntry{
				{
					Chamber:    "house",
					Method:     model.PassageRoll,
					Date:       day(time.January, 13),
					Question:   &question,
					Result:     &result,
					RollNumber: &roll,
					Yeas:       &yeas,
					Nays:       &nays,
					Present:    &present,
					NotVoting:  &notVoting,
				},
				{Chamber: "senate", Method: model.PassageVoice, Date: day(time.February, 3)},
			},
			Enacted:        &model.Enactment{Date: day(time.February, 20), LawType: &lawType, LawNumber: &lawNumber},
			LawChangeCount: 2,
		}},
	}
}

// fullCard and emptyCard are cardRepo's two bills' card objects, as the web reads them.
const (
	fullCard = `{"crs":{"version_code":"49","action_date":"2026-03-02T00:00:00Z","action_desc":"Public Law",` +
		`"lead":"This act names…"},"passage":[{"chamber":"house","method":"roll","date":"2026-01-13T00:00:00Z",` +
		`"question":"On Motion to Suspend the Rules and Pass","result":"Passed","roll_number":19,"yeas":413,` +
		`"nays":0,"present":0,"not_voting":19},{"chamber":"senate","method":"voice","date":"2026-02-03T00:00:00Z"}],` +
		`"enacted":{"date":"2026-02-20T00:00:00Z","law_type":"public","law_number":"119-62"},"law_change_count":2}`
	emptyCard = `{"crs":null,"passage":[],"enacted":null,"law_change_count":0}`
)

func listBillsItems(t *testing.T, h *handler.Handler, query string) []map[string]json.RawMessage {
	t.Helper()
	w := httptest.NewRecorder()
	h.ListBills(w, httptest.NewRequest(http.MethodGet, "/api/v1/bills?"+query, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: status = %d, body %s", query, w.Code, w.Body)
	}
	var body struct {
		Items []map[string]json.RawMessage `json:"items"`
		Total int                          `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("%s: decode: %v", query, err)
	}
	if body.Total != 12 || len(body.Items) != 2 {
		t.Fatalf("%s: total %d with %d items, want 12 and 2", query, body.Total, len(body.Items))
	}
	return body.Items
}

func TestListBills_IncludeCard(t *testing.T) {
	tests := []struct {
		query       string
		wantSummary bool
	}{
		{query: "include=card"},
		{query: "include=summary,card", wantSummary: true},
		{query: "include=card&include=summary", wantSummary: true},
		{query: "include=card,%20summary", wantSummary: true},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			repo := cardRepo()
			items := listBillsItems(t, newTestHandler(repo), tt.query)

			// One batch read for the page, in list order.
			if fmt.Sprint(repo.cardIDs) != "[hr-119-187 s-119-9]" {
				t.Errorf("card IDs = %v", repo.cardIDs)
			}
			for i, want := range []string{fullCard, emptyCard} {
				if got := string(items[i]["card"]); got != want {
					t.Errorf("item %d card = %s\nwant %s", i, got, want)
				}
				if string(items[i]["title"]) == "" {
					t.Errorf("item %d lost the bill's fields: %v", i, items[i])
				}
			}
			checkListSummaries(t, items, repo, tt.wantSummary)
		})
	}
}

// checkListSummaries checks cardRepo's page has its summaries (hr-119-187's, then null) when
// want, and no summary key nor summary read otherwise.
func checkListSummaries(t *testing.T, items []map[string]json.RawMessage, repo *mockBillRepo, want bool) {
	t.Helper()
	if !want {
		if summary, ok := items[0]["summary"]; ok || repo.summaryIDs != nil {
			t.Errorf("include=card alone read or served summaries: %s", summary)
		}
		return
	}
	var first struct {
		ShortSummary string `json:"short_summary"`
	}
	if err := json.Unmarshal(items[0]["summary"], &first); err != nil || first.ShortSummary != "Names a post office." {
		t.Errorf("first item summary = %s", items[0]["summary"])
	}
	if second, ok := items[1]["summary"]; !ok || string(second) != "null" {
		t.Errorf("second item summary = %s, want null", second)
	}
}

func TestListBills_IncludeSummaryHasNoCard(t *testing.T) {
	repo := cardRepo()
	items := listBillsItems(t, newTestHandler(repo), "include=summary")
	for i, item := range items {
		if card, ok := item["card"]; ok {
			t.Errorf("item %d has card %s with include=summary alone", i, card)
		}
		if _, ok := item["summary"]; !ok {
			t.Errorf("item %d has no summary key", i)
		}
	}
	if repo.cardIDs != nil {
		t.Errorf("card facts read with include=summary alone: %v", repo.cardIDs)
	}
}

func TestListBills_IncludeReadFails(t *testing.T) {
	failing := errors.New("spanner unavailable")
	tests := []struct {
		name  string
		query string
		fail  func(*mockBillRepo)
		// want is the body when it must stay what it was before include=card (#705).
		want string
	}{
		{name: "card", query: "include=card", fail: func(m *mockBillRepo) { m.cardErr = failing }},
		{name: "card with summary", query: "include=summary,card", fail: func(m *mockBillRepo) { m.cardErr = failing }},
		{
			name:  "summary with card",
			query: "include=summary,card",
			fail:  func(m *mockBillRepo) { m.summariesErr = failing },
		},
		{
			name: "summary", query: "include=summary", fail: func(m *mockBillRepo) { m.summariesErr = failing },
			want: "{\"error\":\"failed to get bill summaries\"}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := cardRepo()
			tt.fail(repo)
			w := httptest.NewRecorder()
			newTestHandler(repo).ListBills(w, httptest.NewRequest(http.MethodGet, "/api/v1/bills?"+tt.query, nil))
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.Code)
			}
			if body := w.Body.String(); strings.Contains(body, "spanner") || (tt.want != "" && body != tt.want) {
				t.Errorf("body = %q, want %q without the cause", body, tt.want)
			}
		})
	}
}

// TestListBills_IncludeCachePrefixes checks that each include set is cached under its own
// prefix, so no request is served a cached body of another include set.
func TestListBills_IncludeCachePrefixes(t *testing.T) {
	c := testCache(t)
	search := fmt.Sprintf("card%d", time.Now().UnixNano())
	params := model.ListParams{Limit: 20, Search: &search}
	// Each request in order, the prefix its body is cached under and the extras it carries. A
	// request that repeats an earlier include set in another order is a cache hit.
	requests := []struct {
		include, prefix string
		extras          []string
		hit             bool
	}{
		{include: "", prefix: "bills:list"},
		{include: "&include=summary", prefix: "bills:list+summary", extras: []string{"summary"}},
		{include: "&include=card", prefix: "bills:list+card", extras: []string{"card"}},
		{include: "&include=summary,card", prefix: "bills:list+summary+card", extras: []string{"summary", "card"}},
		{include: "&include=crs_summary", prefix: "bills:list+crs_summary", extras: []string{"crs_summary"}},
		{
			include: "&include=summary,crs_summary", prefix: "bills:list+summary+crs_summary",
			extras: []string{"summary", "crs_summary"},
		},
		{
			include: "&include=crs_summary,card", prefix: "bills:list+card+crs_summary",
			extras: []string{"card", "crs_summary"},
		},
		{
			include: "&include=summary,card,crs_summary", prefix: "bills:list+summary+card+crs_summary",
			extras: []string{"summary", "card", "crs_summary"},
		},
		{
			include: "&include=card,summary",
			prefix:  "bills:list+summary+card",
			extras:  []string{"summary", "card"},
			hit:     true,
		},
		{
			include: "&include=crs_summary&include=card,summary", prefix: "bills:list+summary+card+crs_summary",
			extras: []string{"summary", "card", "crs_summary"}, hit: true,
		},
	}
	t.Cleanup(func() {
		for _, req := range requests {
			_ = c.Delete(context.Background(), handler.ListCacheKey(req.prefix, params))
		}
	})

	repo := crsRepo()
	h := newTestHandler(repo)
	h.SetCache(c)
	for _, req := range requests {
		lists := len(repo.listCalls)
		items := listBillsItems(t, h, "q="+search+req.include)
		for _, extra := range []string{"summary", "card", "crs_summary"} {
			if _, ok := items[0][extra]; ok != slices.Contains(req.extras, extra) {
				t.Errorf("%q: has %s %v, want %v", req.include, extra, ok, !ok)
			}
		}
		cached, err := c.Get(context.Background(), handler.ListCacheKey(req.prefix, params))
		if err != nil || cached == "" {
			t.Errorf("%q: nothing cached under %s (err %v)", req.include, req.prefix, err)
		}
		if listed := len(repo.listCalls) > lists; listed == req.hit {
			t.Errorf("%q: List called %v, want a cache hit %v", req.include, listed, req.hit)
		}
	}
}
