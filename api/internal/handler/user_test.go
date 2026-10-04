package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// writeUserRepo answers CastVote and AddFavorite with err and records the
// bill IDs it was asked to write.
type writeUserRepo struct {
	mockUserRepo

	err    error
	writes []string
}

func (m *writeUserRepo) CastVote(_ context.Context, _, billID, _ string, _ model.VoteChecks) error {
	m.writes = append(m.writes, billID)
	return m.err
}

func (m *writeUserRepo) AddFavorite(_ context.Context, _, billID string) error {
	m.writes = append(m.writes, billID)
	return m.err
}

func TestVoteAndFavoriteMissingBill(t *testing.T) {
	notFound := fmt.Errorf("bill %q: %w", "hr-119-99999", repository.ErrNotFound)
	routes := []struct {
		name, path, body string
	}{
		{"vote", "/bills/hr-119-99999/vote", `{"vote":"Yea"}`},
		{"favorite", "/me/favorites/hr-119-99999", ""},
	}
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{"saved", nil, http.StatusOK, ""},
		{"no such bill", notFound, http.StatusNotFound, "bill not found"},
		{"other error", errSpanner, http.StatusInternalServerError, ""},
	}
	for _, rt := range routes {
		for _, tt := range tests {
			t.Run(rt.name+"/"+tt.name, func(t *testing.T) {
				users := &writeUserRepo{err: tt.err}
				w := postUserWrite(users, rt.path, rt.body)

				if w.Code != tt.wantStatus {
					t.Fatalf("status = %d, want %d (%s)", w.Code, tt.wantStatus, w.Body.String())
				}
				if len(users.writes) != 1 || users.writes[0] != "hr-119-99999" {
					t.Errorf("writes = %v, want one for hr-119-99999", users.writes)
				}
				if tt.wantError != "" {
					checkErrorBody(t, w, tt.wantError)
				}
			})
		}
	}
}

// votesUserRepo answers GetVotes with votes and records the user it was asked for.
type votesUserRepo struct {
	mockUserRepo

	votes  []model.UserVote
	userID string
}

func (m *votesUserRepo) GetVotes(
	_ context.Context, userID string, params model.ListParams,
) (*model.ListResult[model.UserVote], error) {
	m.userID = userID
	return &model.ListResult[model.UserVote]{
		Items: m.votes, Total: len(m.votes), Offset: params.Offset, Limit: params.Limit,
	}, nil
}

func TestGetMyVotesIncludesBillTitles(t *testing.T) {
	votedAt := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	users := &votesUserRepo{votes: []model.UserVote{
		{UserID: "u1", BillID: "hr-119-1", Vote: "yea", VotedAt: votedAt, Title: "Lower Costs Act"},
		{UserID: "u1", BillID: "hr-119-2", Vote: "nay", VotedAt: votedAt}, // bill row gone
	}}
	h := newTestHandler(&mockBillRepo{})
	h.Users = users
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/votes", nil)
	w := httptest.NewRecorder()
	h.GetMyVotes(w, req.WithContext(mw.ContextWithUserID(req.Context(), "u1")))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if users.userID != "u1" {
		t.Errorf("GetVotes user = %q, want u1", users.userID)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items = %v, want 2", body.Items)
	}
	if got := body.Items[0]["title"]; got != "Lower Costs Act" {
		t.Errorf("hr-119-1 title = %v, want Lower Costs Act", got)
	}
	if _, ok := body.Items[1]["title"]; ok {
		t.Errorf("hr-119-2 = %v, want no title key", body.Items[1])
	}
	if _, ok := body.Items[0]["app_check_ok"]; ok {
		t.Errorf("hr-119-1 = %v, want no app_check_ok key", body.Items[0])
	}
}

// postUserWrite serves a vote or favorite POST against users.
func postUserWrite(users *writeUserRepo, path, body string) *httptest.ResponseRecorder {
	h := newTestHandler(&mockBillRepo{})
	h.Users = users
	r := chi.NewRouter()
	r.Post("/bills/{id}/vote", h.CastVote)
	r.Post("/me/favorites/{billID}", h.AddFavorite)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return w
}

// checkErrorBody checks a JSON error response's message.
func checkErrorBody(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != want {
		t.Errorf("error = %q, want %q", body["error"], want)
	}
}
