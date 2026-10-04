package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

const positionsCacheControl = "public, max-age=300, s-maxage=3600, stale-while-revalidate=86400"

// positionsVoteRepo returns positions for one member and records the calls.
type positionsVoteRepo struct {
	mockVoteRepo

	positions map[string][]model.MemberPosition
	err       error
	calls     int
	congress  int
}

func (m *positionsVoteRepo) MemberPositions(
	_ context.Context, memberID string, congress int,
) ([]model.MemberPosition, error) {
	m.calls++
	m.congress = congress
	if m.positions[memberID] == nil {
		return []model.MemberPosition{}, m.err
	}
	return m.positions[memberID], m.err
}

// knownMembersRepo knows the members in ids.
type knownMembersRepo struct {
	mockMemberRepo

	ids map[string]bool
	err error
}

func (m *knownMembersRepo) GetByID(_ context.Context, id string) (*model.MemberDetail, error) {
	if m.err != nil || !m.ids[id] {
		return nil, m.err
	}
	return &model.MemberDetail{BioguideID: id}, nil
}

func newPositionsRouter(votes *positionsVoteRepo, members *knownMembersRepo) (*handler.Handler, *chi.Mux) {
	h := newTestHandler(&mockBillRepo{})
	h.Votes = votes
	h.Members = members
	h.Congresses = loadedCongresses()
	r := chi.NewRouter()
	r.Get("/api/v1/members/{id}/positions", h.GetMemberPositions)
	return h, r
}

func TestGetMemberPositions(t *testing.T) {
	voteDate := time.Date(2025, time.March, 5, 17, 0, 0, 0, time.UTC)
	votes := &positionsVoteRepo{positions: map[string][]model.MemberPosition{
		"A000001": {{
			BillID: "hr-119-1", Vote: model.PositionYea, VoteID: "house-119-s1-roll002", Chamber: "House",
			VoteDate: voteDate, Question: new("On Passage"),
		}},
	}}
	_, r := newPositionsRouter(votes, &knownMembersRepo{ids: map[string]bool{"A000001": true}})

	w := serve(r, "/api/v1/members/A000001/positions?congress=119")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Cache-Control"); got != positionsCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, positionsCacheControl)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"member_id": "A000001",
		"congress":  float64(119),
		"rule":      "final-passage-v1",
		"positions": []any{map[string]any{
			"bill_id": "hr-119-1", "vote": "yea", "vote_id": "house-119-s1-roll002", "chamber": "House",
			"vote_date": "2025-03-05T17:00:00Z", "question": "On Passage",
		}},
	}
	if fmt.Sprint(body) != fmt.Sprint(want) {
		t.Errorf("body = %v\nwant %v", body, want)
	}
	if votes.congress != 119 {
		t.Errorf("repo congress = %d, want 119", votes.congress)
	}
}

func TestGetMemberPositions_KnownMemberWithoutPositions(t *testing.T) {
	_, r := newPositionsRouter(&positionsVoteRepo{}, &knownMembersRepo{ids: map[string]bool{"B000002": true}})
	w := serve(r, "/api/v1/members/B000002/positions?congress=118")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"positions":[]`) {
		t.Errorf("body = %s, want an empty positions array", w.Body)
	}
	if got := w.Header().Get("Cache-Control"); got != positionsCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, positionsCacheControl)
	}
}

func TestGetMemberPositions_Errors(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		votes   *positionsVoteRepo
		members *knownMembersRepo
		status  int
	}{
		{"missing congress", "/api/v1/members/A000001/positions", nil, nil, http.StatusBadRequest},
		{"non-numeric congress", "/api/v1/members/A000001/positions?congress=abc", nil, nil, http.StatusBadRequest},
		{"zero congress", "/api/v1/members/A000001/positions?congress=0", nil, nil, http.StatusBadRequest},
		{"negative congress", "/api/v1/members/A000001/positions?congress=-119", nil, nil, http.StatusBadRequest},
		{"out-of-range congress", "/api/v1/members/A000001/positions?congress=201", nil, nil, http.StatusBadRequest},
		{"huge congress", "/api/v1/members/A000001/positions?congress=99999999999999999999", nil, nil,
			http.StatusBadRequest},
		{"malformed id", "/api/v1/members/a000001/positions?congress=119", nil, nil, http.StatusBadRequest},
		{"id too long", "/api/v1/members/A0000011/positions?congress=119", nil, nil, http.StatusBadRequest},
		{"unknown member", "/api/v1/members/Z999999/positions?congress=119", nil, nil, http.StatusNotFound},
		{"congress not loaded", "/api/v1/members/A000001/positions?congress=117", nil, nil, http.StatusBadRequest},
		{"positions query fails", "/api/v1/members/A000001/positions?congress=119",
			&positionsVoteRepo{err: errors.New("spanner down")}, nil, http.StatusInternalServerError},
		{"member lookup fails", "/api/v1/members/A000001/positions?congress=119", nil,
			&knownMembersRepo{err: errors.New("spanner down")}, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			votes, members := tt.votes, tt.members
			if votes == nil {
				votes = &positionsVoteRepo{}
			}
			if members == nil {
				members = &knownMembersRepo{ids: map[string]bool{"A000001": true}}
			}
			_, r := newPositionsRouter(votes, members)
			w := serve(r, tt.path)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.status, w.Body)
			}
			if got := w.Header().Get("Cache-Control"); got != "" {
				t.Errorf("Cache-Control = %q on a %d, want none", got, w.Code)
			}
			if tt.status == http.StatusBadRequest && votes.calls != 0 {
				t.Errorf("repo called %d times for a bad request", votes.calls)
			}
		})
	}
}

func TestGetMemberPositions_Caching(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping Redis integration test")
	}
	c, err := cache.New(redisURL)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// A member ID unique to this run, so runs sharing a Redis don't see each other's keys.
	id := fmt.Sprintf("Q%06d", time.Now().UnixNano()%1_000_000)
	t.Cleanup(func() { _ = c.Delete(context.Background(), "positions:"+id+":119") })

	votes := &positionsVoteRepo{positions: map[string][]model.MemberPosition{
		id: {{BillID: "hr-119-1", Vote: model.PositionNay, VoteID: "house-119-s1-roll001"}},
	}}
	h, r := newPositionsRouter(votes, &knownMembersRepo{ids: map[string]bool{id: true}})
	h.SetCache(c)

	path := "/api/v1/members/" + id + "/positions?congress=119"
	first := serve(r, path)
	second := serve(r, path)
	if votes.calls != 1 {
		t.Errorf("repo calls = %d, want 1 (second request from cache)", votes.calls)
	}
	if second.Code != http.StatusOK || strings.TrimSpace(first.Body.String()) != second.Body.String() {
		t.Errorf("cached response %d %q differs from %q", second.Code, second.Body, first.Body)
	}
	if got := second.Header().Get("Cache-Control"); got != positionsCacheControl {
		t.Errorf("Cache-Control on a cache hit = %q, want %q", got, positionsCacheControl)
	}

	// A zero-padded congress is the same key.
	serve(r, "/api/v1/members/"+id+"/positions?congress=0119")
	if votes.calls != 1 {
		t.Errorf("repo calls = %d after ?congress=0119, want 1 (same key as 119)", votes.calls)
	}
}

func TestGetMemberPositions_CongressesUnreadable(t *testing.T) {
	votes := &positionsVoteRepo{}
	h, r := newPositionsRouter(votes, &knownMembersRepo{ids: map[string]bool{"A000001": true}})
	h.Congresses = &listCongressRepo{err: errors.New("spanner down")}
	if w := serve(r, "/api/v1/members/A000001/positions?congress=119"); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body)
	}
	if votes.calls != 0 {
		t.Errorf("repo called %d times without the congress list", votes.calls)
	}
}
