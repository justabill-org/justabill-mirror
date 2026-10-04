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
