package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// actionsStore records what syncBillActions writes. Any other PipelineStore method, such as
// UpsertMemberVote, panics through the nil embedded interface.
type actionsStore struct {
	repository.PipelineStore

	replaced     bool
	actions      []repository.BillActionRow
	status       string
	statusWrites int
	history      []repository.BillStatusRow
	votes        []repository.CongressionalVoteRow
}

func (f *actionsStore) ReplaceBillActions(_ context.Context, _ string, rows []repository.BillActionRow) error {
	f.replaced = true
	f.actions = rows
	return nil
}

func (f *actionsStore) ReplaceBillStatus(
	_ context.Context, _, status string, _ *time.Time, rows []repository.BillStatusRow,
) error {
	f.statusWrites++
	f.status = status
	f.history = rows
	return nil
}

func (f *actionsStore) UpsertCongressionalVote(_ context.Context, v repository.CongressionalVoteRow) error {
	f.votes = append(f.votes, v)
	return nil
}

// serviceWithAPI returns a Service whose Congress.gov client talks to handler.
func serviceWithAPI(t *testing.T, store repository.PipelineStore, handler http.HandlerFunc) *Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Service{
		store:  store,
		api:    congress.NewClientWithBaseURL(srv.Client(), srv.URL),
		http:   srv.Client(),
		logger: slog.New(slog.DiscardHandler),
	}
}

// hr1Actions is the recorded page of all 59 HR 1 (119th) actions, newest first.
func hr1Actions(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "congress", "testdata", "hr-119-1-actions-0.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func statuses(rows []repository.BillStatusRow) map[string]bool {
	got := make(map[string]bool, len(rows))
	for _, r := range rows {
		got[r.Status] = true
	}
	return got
}

func TestSyncBillActions_UsesEveryAction(t *testing.T) {
	body := hr1Actions(t)
	store := &actionsStore{}
	s := serviceWithAPI(t, store, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })

	if err := s.syncBillActions(t.Context(), testBillID, 119, "hr", 1); err != nil {
		t.Fatalf("syncBillActions: %v", err)
	}

	if len(store.actions) != 59 {
		t.Fatalf("stored %d actions, want 59", len(store.actions))
	}
	for i, a := range store.actions {
		if a.SortOrder != i+1 {
			t.Fatalf("action %d has SortOrder %d, want a running index", i, a.SortOrder)
		}
	}
	if store.status != "became_law" {
		t.Errorf("status = %q, want became_law", store.status)
	}
	// HR 1 was reported by the Budget Committee on 2025-05-20, 39 actions back. The first
	// 20 actions, all we used to read, don't reach it.
	if !statuses(store.history)["reported"] {
		t.Errorf("status history %v is missing the early reported stage", statuses(store.history))
	}

	var page congress.ActionsResponse
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	const oldPageSize = 20
	if _, _, firstPage := computeStatusHistory(
		apiLifecycleActions(page.Actions[:oldPageSize]),
	); len(
		firstPage,
	) >= len(
		store.history,
	) {
		t.Errorf("the first %d actions give %d stages; the full list should give more than that, got %d",
			oldPageSize, len(firstPage), len(store.history))
	}
}

func TestSyncBillActions_StoresRecordedVote(t *testing.T) {
	const (
		house190 = "https://clerk.house.gov/evs/2025/roll190.xml"
		house191 = "https://clerk.house.gov/evs/2025/roll191.xml"
	)
	// Newest first, as Congress.gov lists them; each vote mirrors its recordedVotes entries.
	body := `{"actions": [
	 {"actionDate": "2025-07-03", "text": "Two roll calls", "recordedVotes": [
	  {"chamber": "House", "congress": 119, "date": "2025-07-03T18:00:00Z", "rollNumber": 190,
	   "sessionNumber": 1, "url": "` + house190 + `"},
	  {"chamber": "House", "congress": 119, "date": "2025-07-03T18:31:38Z", "rollNumber": 191,
	   "sessionNumber": 1, "url": "` + house191 + `"}]},
	 {"actionDate": "2025-07-02", "text": "A bad last entry", "recordedVotes": [
	  {"chamber": "House", "congress": 119, "date": "2025-07-02T10:00:00Z", "rollNumber": 189,
	   "sessionNumber": 1, "url": "https://clerk.house.gov/evs/2025/roll189.xml"},
	  {"chamber": "House", "congress": 119, "date": "2025-07-02T11:00:00Z", "rollNumber": 0,
	   "sessionNumber": 1, "url": "https://clerk.house.gov/evs/2025/roll0.xml"}]},
	 {"actionDate": "2025-07-01", "text": "Not a web link", "recordedVotes": [
	  {"chamber": "Senate", "congress": 119, "date": "2025-07-01T16:03:40Z", "rollNumber": 372,
	   "sessionNumber": 1, "url": "javascript:alert(1)"}]},
	 {"actionDate": "2025-06-30", "text": "No such chamber", "recordedVotes": [
	  {"chamber": "Joint", "congress": 119, "date": "2025-06-30T16:00:00Z", "rollNumber": 5,
	   "sessionNumber": 1, "url": "https://www.senate.gov/x.xml"}]},
	 {"actionDate": "2025-05-22", "text": "Passed House", "recordedVotes": [
	  {"chamber": "House", "congress": 119, "date": "2025-05-22T10:00:00Z", "rollNumber": 145,
	   "sessionNumber": 1, "url": "https://clerk.house.gov/evs/2025/roll145.xml"}]},
	 {"actionDate": "2025-05-16", "text": "Reported"}
	]}`
	store := &actionsStore{}
	s := serviceWithAPI(t, store, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })

	if err := s.syncBillActions(t.Context(), testBillID, 119, "hr", 1); err != nil {
		t.Fatalf("syncBillActions: %v", err)
	}

	want := map[string]*model.RecordedVote{
		"Two roll calls":   {RollNumber: 191, URL: house191, Chamber: "House"},
		"A bad last entry": {RollNumber: 189, URL: "https://clerk.house.gov/evs/2025/roll189.xml", Chamber: "House"},
		"Not a web link":   nil,
		"No such chamber":  nil,
		"Passed House":     {RollNumber: 145, URL: "https://clerk.house.gov/evs/2025/roll145.xml", Chamber: "House"},
		"Reported":         nil,
	}
	if len(store.actions) != len(want) {
		t.Fatalf("stored %d actions, want %d", len(store.actions), len(want))
	}
	for _, a := range store.actions {
		w, ok := want[a.ActionText]
		if !ok {
			t.Fatalf("stored an unexpected action %q", a.ActionText)
		}
		switch {
		case w == nil && a.RecordedVote != nil:
			t.Errorf("%q: RecordedVote = %+v, want nil", a.ActionText, *a.RecordedVote)
		case w != nil && (a.RecordedVote == nil || *a.RecordedVote != *w):
			t.Errorf("%q: RecordedVote = %v, want %+v", a.ActionText, a.RecordedVote, *w)
		}
	}
}

func TestSyncBillActions_PageFailureWritesNothing(t *testing.T) {
	store := &actionsStore{}
	s := serviceWithAPI(t, store, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "0" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		items := make([]string, pageSize)
		for i := range items {
			items[i] = fmt.Sprintf(`{"actionDate":"2025-01-01","text":"Passed House %d"}`, i)
		}
		fmt.Fprintf(w, `{"pagination": {"count": 300, "next": "x"}, "actions": [%s]}`, strings.Join(items, ","))
	})

	if err := s.syncBillActions(t.Context(), testBillID, 119, "hr", 1); err == nil {
		t.Error("syncBillActions succeeded although a page failed")
	}

	if store.replaced || store.statusWrites > 0 || store.history != nil {
		t.Errorf("a failed second page wrote data: replaced=%v statusWrites=%d history=%v",
			store.replaced, store.statusWrites, store.history)
	}
}
