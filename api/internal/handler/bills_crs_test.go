package handler_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
)

// crsRepo is cardRepo's page with a CRS summary lead for hr-119-187 and none for s-119-9.
func crsRepo() *mockBillRepo {
	repo := cardRepo()
	repo.crsLeads = map[string]model.CardCRS{"hr-119-187": {
		VersionCode: "49",
		ActionDate:  time.Date(2025, time.December, 26, 0, 0, 0, 0, time.UTC),
		ActionDesc:  "Public Law",
		Lead:        "This act directs the Forest Service to publish data on access to federal waterways.",
	}}
	return repo
}

// fullCRS is crsRepo's hr-119-187 crs_summary, as the web reads it.
const fullCRS = `{"version_code":"49","action_date":"2025-12-26T00:00:00Z","action_desc":"Public Law",` +
	`"lead":"This act directs the Forest Service to publish data on access to federal waterways."}`

func TestListBills_IncludeCRSSummary(t *testing.T) {
	tests := []struct {
		query                 string
		wantSummary, wantCard bool
	}{
		{query: "include=crs_summary"},
		{query: "include=summary,crs_summary", wantSummary: true},
		{query: "include=crs_summary&include=summary", wantSummary: true},
		{query: "include=card,%20crs_summary", wantCard: true},
		{query: "include=crs_summary,card,summary", wantSummary: true, wantCard: true},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			repo := crsRepo()
			items := listBillsItems(t, newTestHandler(repo), tt.query)

			// One batch read for the page, in list order.
			if fmt.Sprint(repo.crsLeadIDs) != "[hr-119-187 s-119-9]" {
				t.Errorf("CRS lead IDs = %v", repo.crsLeadIDs)
			}
			for i, want := range []string{fullCRS, "null"} {
				if got, ok := items[i]["crs_summary"]; !ok || string(got) != want {
					t.Errorf("item %d crs_summary = %s (present %v)\nwant %s", i, got, ok, want)
				}
				if string(items[i]["title"]) == "" {
					t.Errorf("item %d lost the bill's fields: %v", i, items[i])
				}
			}
			checkListSummaries(t, items, repo, tt.wantSummary)
			checkListCard(t, items, repo, tt.wantCard)
		})
	}
}

// checkListCard checks crsRepo's first bill has its card when want, and no card key nor card
// read otherwise.
func checkListCard(t *testing.T, items []map[string]json.RawMessage, repo *mockBillRepo, want bool) {
	t.Helper()
	card, ok := items[0]["card"]
	if ok != want || (repo.cardIDs != nil) != want {
		t.Errorf("card served %v, read %v; want %v", ok, repo.cardIDs, want)
	}
	if want && string(card) != fullCard {
		t.Errorf("card = %s\nwant %s", card, fullCard)
	}
}

// TestListBills_IncludesLeaveOtherBodiesAlone checks that a list without include=crs_summary
// neither reads CRS summaries nor carries the key.
func TestListBills_IncludesLeaveOtherBodiesAlone(t *testing.T) {
	for _, q := range []string{"", "include=summary", "include=card", "include=summary,card"} {
		repo := crsRepo()
		items := listBillsItems(t, newTestHandler(repo), q)
		if crs, ok := items[0]["crs_summary"]; ok || repo.crsLeadIDs != nil {
			t.Errorf("%q: CRS summaries read (%v) or served (%s)", q, repo.crsLeadIDs, crs)
		}
	}
}

func TestListBills_IncludeCRSSummaryReadFails(t *testing.T) {
	for _, q := range []string{"include=crs_summary", "include=summary,card,crs_summary"} {
		repo := crsRepo()
		repo.crsLeadsErr = errors.New("spanner unavailable")
		w := httptest.NewRecorder()
		newTestHandler(repo).ListBills(w, httptest.NewRequest(http.MethodGet, "/api/v1/bills?"+q, nil))
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%q: status = %d, want 500", q, w.Code)
		}
		if body := w.Body.String(); strings.Contains(body, "spanner") {
			t.Errorf("%q: body = %q, want it without the cause", q, body)
		}
	}
}
