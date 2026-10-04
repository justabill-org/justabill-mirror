package handler_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

// memberStore is a seeded store of members by bioguide ID, with each one's recent votes. err fails
// every read but GetRecentVotes, which votesErr fails; listCalls records List's params.
type memberStore struct {
	mockMemberRepo

	members   map[string]model.MemberDetail
	recent    map[string][]model.MemberVoteSummary
	list      []model.Member
	err       error
	votesErr  error
	listCalls []model.ListParams
}

func (s *memberStore) GetByID(_ context.Context, id string) (*model.MemberDetail, error) {
	m, ok := s.members[id]
	if !ok || s.err != nil {
		return nil, s.err
	}
	return &m, nil // a copy, as Spanner's reads are: the handler sets its recent votes
}
func (s *memberStore) GetRecentVotes(_ context.Context, id string, _ int) ([]model.MemberVoteSummary, error) {
	if s.votesErr != nil {
		return nil, s.votesErr
	}
	return s.recent[id], nil
}
func (s *memberStore) List(_ context.Context, p model.ListParams) (*model.ListResult[model.Member], error) {
	s.listCalls = append(s.listCalls, p)
	if s.err != nil {
		return nil, s.err
	}
	return &model.ListResult[model.Member]{Items: s.list, Total: len(s.list), Limit: p.Limit}, nil
}

// congressStore answers List with congresses, or err.
type congressStore struct {
	congresses []model.Congress
	err        error
}

func (s *congressStore) List(_ context.Context) ([]model.Congress, error) { return s.congresses, s.err }

func seededMembers() *memberStore {
	billID, question := "hr-119-1", "On Passage"
	return &memberStore{
		members: map[string]model.MemberDetail{
			"P000197": {BioguideID: "P000197", FirstName: "Nancy", LastName: "Pelosi", Terms: []model.MemberTerm{}},
		},
		recent: map[string][]model.MemberVoteSummary{"P000197": {{
			VoteID: "h-119-1-42", BillID: &billID, VoteDate: time.Date(2026, time.March, 4, 0, 0, 0, 0, time.UTC),
			Question: &question, MemberVote: "Yea", Chamber: "House",
		}}},
		list: []model.Member{{BioguideID: "P000197", FirstName: "Nancy", LastName: "Pelosi"}},
	}
}

func memberRouter(h *handler.Handler) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/members", h.ListMembers)
	r.Get("/api/v1/members/{id}", h.GetMember)
	r.Get("/api/v1/congresses", h.ListCongresses)
	return r
}

func TestGetMember(t *testing.T) {
	members := seededMembers()
	h := newTestHandler(&mockBillRepo{})
	h.Members = members

	w := get(t, memberRouter(h), "/api/v1/members/P000197")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	assertJSON(t, w.Body.Bytes(), `{"bioguide_id":"P000197","first_name":"Nancy","last_name":"Pelosi",
		"terms":[],"recent_votes":[{"vote_id":"h-119-1-42","bill_id":"hr-119-1",
		"vote_date":"2026-03-04T00:00:00Z","question":"On Passage","member_vote":"Yea","chamber":"House"}]}`)
}

// A member whose recent votes can't be read is still served, without them: the page shows the
// member, and the votes are a section of it.
func TestGetMember_RecentVotesUnreadable(t *testing.T) {
	members := seededMembers()
	members.votesErr = errSpanner
	h := newTestHandler(&mockBillRepo{})
	h.Members = members

	w := get(t, memberRouter(h), "/api/v1/members/P000197")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	assertJSON(t, w.Body.Bytes(), `{"bioguide_id":"P000197","first_name":"Nancy","last_name":"Pelosi",
		"terms":[],"recent_votes":null}`)
}

func TestGetMember_Errors(t *testing.T) {
	tests := []struct {
		name, id   string
		err        error
		wantStatus int
		wantError  string
	}{
		{name: "unknown member", id: "Z000001", wantStatus: http.StatusNotFound, wantError: "member not found"},
		{name: "malformed id", id: "pelosi", wantStatus: http.StatusBadRequest, wantError: "invalid member id"},
		{name: "lower-case id", id: "p000197", wantStatus: http.StatusBadRequest, wantError: "invalid member id"},
		{name: "read fails", id: "P000197", err: errSpanner, wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			members := seededMembers()
			members.err = tt.err
			h := newTestHandler(&mockBillRepo{})
			h.Members = members

			w := get(t, memberRouter(h), "/api/v1/members/"+tt.id)

			if tt.wantStatus == http.StatusInternalServerError {
				assertHiddenServerError(t, w)
				return
			}
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.wantStatus, w.Body.String())
			}
			if got := errorBody(t, w); got != tt.wantError {
				t.Errorf("error = %q, want %q", got, tt.wantError)
			}
		})
	}
}

func TestListMembers_Congress(t *testing.T) {
	tests := []struct {
		name, query string
		// wantCongress is the congress List filters by; 0 is none.
		wantCongress int
	}{
		{name: "no congress", query: ""},
		{name: "congress", query: "congress=119", wantCongress: 119},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			members := seededMembers()
			h := newTestHandler(&mockBillRepo{})
			h.Members = members

			w := get(t, memberRouter(h), "/api/v1/members?"+tt.query)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
			}
			assertJSON(t, w.Body.Bytes(), `{"items":[{"bioguide_id":"P000197","first_name":"Nancy",
				"last_name":"Pelosi"}],"total":1,"offset":0,"limit":20}`)
			if len(members.listCalls) != 1 {
				t.Fatalf("List calls = %d, want 1", len(members.listCalls))
			}
			got := 0
			if c := members.listCalls[0].Congress; c != nil {
				got = *c
			}
			if got != tt.wantCongress {
				t.Errorf("List congress = %d, want %d (0: none)", got, tt.wantCongress)
			}
		})
	}
}

func TestListMembers_ReadFails(t *testing.T) {
	members := seededMembers()
	members.err = errSpanner
	h := newTestHandler(&mockBillRepo{})
	h.Members = members

	assertHiddenServerError(t, get(t, memberRouter(h), "/api/v1/members"))
}

// TestListMembers_Cached: the second request is answered from Redis, byte for byte the first.
func TestListMembers_Cached(t *testing.T) {
	c := testCache(t)
	// A search unique to this run, so runs sharing a Redis don't see each other's keys.
	search := fmt.Sprintf("cached%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = c.Delete(context.Background(), handler.ListCacheKey("members:list", model.ListParams{
			Limit: 20, Search: &search,
		}))
	})
	members := seededMembers()
	h := newTestHandler(&mockBillRepo{})
	h.Members = members
	h.SetCache(c)
	router := memberRouter(h)

	first := get(t, router, "/api/v1/members?q="+search)
	second := get(t, router, "/api/v1/members?q="+search)

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d, want 200 twice", first.Code, second.Code)
	}
	if len(members.listCalls) != 1 {
		t.Errorf("List calls = %d, want 1 (the second request from the cache)", len(members.listCalls))
	}
	if got := second.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("cached Content-Type = %q, want application/json", got)
	}
	assertJSON(t, second.Body.Bytes(), first.Body.String())
}

// TestListMembers_WithoutWorkingCache: with Redis down or no cache at all, every request is
// read from the store and answered.
func TestListMembers_WithoutWorkingCache(t *testing.T) {
	tests := []struct {
		name string
		set  func(*testing.T, *handler.Handler)
	}{
		{name: "redis refuses connections", set: func(t *testing.T, h *handler.Handler) {
			h.SetCache(unreachableCache(t))
		}},
		{name: "no cache", set: func(*testing.T, *handler.Handler) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			members := seededMembers()
			h := newTestHandler(&mockBillRepo{})
			h.Members = members
			tt.set(t, h)
			router := memberRouter(h)

			for range 2 {
				w := get(t, router, "/api/v1/members")
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
				}
				assertJSON(t, w.Body.Bytes(), `{"items":[{"bioguide_id":"P000197","first_name":"Nancy",
					"last_name":"Pelosi"}],"total":1,"offset":0,"limit":20}`)
			}
			if len(members.listCalls) != 2 {
				t.Errorf("List calls = %d, want 2", len(members.listCalls))
			}
		})
	}
}

func TestListCongresses(t *testing.T) {
	end := time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC)
	h := newTestHandler(&mockBillRepo{})
	h.Congresses = &congressStore{congresses: []model.Congress{
		{Number: 119, StartDate: end, IsCurrent: true, HasVotes: true},
		{Number: 118, StartDate: time.Date(2023, time.January, 3, 0, 0, 0, 0, time.UTC), EndDate: &end, HasVotes: true},
	}}

	w := get(t, memberRouter(h), "/api/v1/congresses")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	assertJSON(t, w.Body.Bytes(), `[
		{"number":119,"start_date":"2025-01-03T00:00:00Z","is_current":true,"has_votes":true},
		{"number":118,"start_date":"2023-01-03T00:00:00Z","end_date":"2025-01-03T00:00:00Z",
			"is_current":false,"has_votes":true}]`)
}

func TestListCongresses_ReadFails(t *testing.T) {
	h := newTestHandler(&mockBillRepo{})
	h.Congresses = &congressStore{err: errSpanner}

	assertHiddenServerError(t, get(t, memberRouter(h), "/api/v1/congresses"))
}
