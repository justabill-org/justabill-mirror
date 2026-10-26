package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

// memberStore is a seeded store of members by bioguide ID, with each one's recent votes and
// attendance rows. err fails every read but GetRecentVotes and GetAttendance, which votesErr and
// attendanceErr fail; listCalls records List's params.
type memberStore struct {
	mockMemberRepo

	members       map[string]model.MemberDetail
	recent        map[string][]model.MemberVoteSummary
	attendance    map[string][]model.MemberAttendance
	list          []model.Member
	err           error
	votesErr      error
	attendanceErr error
	listCalls     []model.ListParams
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
func (s *memberStore) GetAttendance(_ context.Context, id string) ([]model.MemberAttendance, error) {
	if s.attendanceErr != nil {
		return nil, s.attendanceErr
	}
	return s.attendance[id], nil
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
		"vote_date":"2026-03-04T00:00:00Z","question":"On Passage","member_vote":"Yea","chamber":"House"}],
		"attendance":{"rule":"attendance-v1","congresses":[]}}`)
}

// A member with no recorded votes gets `[]`, so it can't be read as a failed section (#929): the
// repository returns none as a nil slice.
func TestGetMember_NoRecentVotes(t *testing.T) {
	members := seededMembers()
	members.recent = nil
	h := newTestHandler(&mockBillRepo{})
	h.Members = members

	w := get(t, memberRouter(h), "/api/v1/members/P000197")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	assertJSON(t, w.Body.Bytes(), `{"bioguide_id":"P000197","first_name":"Nancy","last_name":"Pelosi",
		"terms":[],"recent_votes":[],"attendance":{"rule":"attendance-v1","congresses":[]}}`)
}

// A member whose recent votes can't be read is still served, with the section null, the API's
// mark for a failed section (bill detail's, #454): the web says the votes couldn't be loaded
// rather than that there are none (#929).
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
		"terms":[],"recent_votes":null,"attendance":{"rule":"attendance-v1","congresses":[]}}`)
}

// TestGetMember_Attendance: a member's attendance rows come newest congress first under the rule's
// name, each with its missed share of the eligible roll calls rounded to one decimal (#1001).
func TestGetMember_Attendance(t *testing.T) {
	computed := time.Date(2026, time.October, 17, 6, 12, 3, 0, time.UTC)
	members := seededMembers()
	members.attendance = map[string][]model.MemberAttendance{"P000197": {
		{MemberID: "P000197", Congress: 119, Chamber: "House", Eligible: 782, Missed: 44, Present: 3,
			ChamberMembers: 441, ChamberMedianPct: 2.5641025641, ComputedAt: computed},
		// 2 of 3 is 66.666…%: rounded, not cut, to one decimal. 0 missed is 0, not left out.
		{MemberID: "P000197", Congress: 118, Chamber: "Senate", Eligible: 3, Missed: 2,
			ChamberMembers: 100, ChamberMedianPct: 1.25, ComputedAt: computed},
		{MemberID: "P000197", Congress: 117, Chamber: "House", Eligible: 1241,
			ChamberMembers: 447, ChamberMedianPct: 0, ComputedAt: computed},
	}}
	h := newTestHandler(&mockBillRepo{})
	h.Members = members

	w := get(t, memberRouter(h), "/api/v1/members/P000197")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	assertJSON(t, w.Body.Bytes(), `{"bioguide_id":"P000197","first_name":"Nancy","last_name":"Pelosi",
		"terms":[],"recent_votes":[{"vote_id":"h-119-1-42","bill_id":"hr-119-1",
		"vote_date":"2026-03-04T00:00:00Z","question":"On Passage","member_vote":"Yea","chamber":"House"}],
		"attendance":{"rule":"attendance-v1","congresses":[
			{"congress":119,"chamber":"House","eligible":782,"missed":44,"present":3,"missed_pct":5.6,
				"chamber_members":441,"chamber_median_pct":2.6,"computed_at":"2026-10-17T06:12:03Z"},
			{"congress":118,"chamber":"Senate","eligible":3,"missed":2,"present":0,"missed_pct":66.7,
				"chamber_members":100,"chamber_median_pct":1.3,"computed_at":"2026-10-17T06:12:03Z"},
			{"congress":117,"chamber":"House","eligible":1241,"missed":0,"present":0,"missed_pct":0,
				"chamber_members":447,"chamber_median_pct":0,"computed_at":"2026-10-17T06:12:03Z"}]}}`)
}

// A row with no eligible roll calls (the pipeline writes none, but a hand-edited row could) gets
// 0%, not NaN: [json.Marshal] refuses NaN, which would turn the whole member into a 500.
func TestGetMember_AttendanceWithNoEligibleRollCalls(t *testing.T) {
	members := seededMembers()
	members.recent = nil
	members.attendance = map[string][]model.MemberAttendance{"P000197": {
		{MemberID: "P000197", Congress: 119, Chamber: "House", ChamberMembers: 441, ChamberMedianPct: 2.6,
			ComputedAt: time.Date(2026, time.October, 17, 0, 0, 0, 0, time.UTC)},
	}}
	h := newTestHandler(&mockBillRepo{})
	h.Members = members

	w := get(t, memberRouter(h), "/api/v1/members/P000197")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	assertJSON(t, w.Body.Bytes(), `{"bioguide_id":"P000197","first_name":"Nancy","last_name":"Pelosi",
		"terms":[],"recent_votes":[],"attendance":{"rule":"attendance-v1","congresses":[
			{"congress":119,"chamber":"House","eligible":0,"missed":0,"present":0,"missed_pct":0,
				"chamber_members":441,"chamber_median_pct":2.6,"computed_at":"2026-10-17T00:00:00Z"}]}}`)
}

// A member whose attendance can't be read is still served, with attendance null, and the WARN
// names the member, as for recent votes (#929).
func TestGetMember_AttendanceUnreadable(t *testing.T) {
	members := seededMembers()
	members.recent = nil
	members.attendanceErr = errSpanner
	var logs bytes.Buffer
	h := newTestHandler(&mockBillRepo{})
	h.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	h.Members = members

	w := get(t, memberRouter(h), "/api/v1/members/P000197")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	assertJSON(t, w.Body.Bytes(), `{"bioguide_id":"P000197","first_name":"Nancy","last_name":"Pelosi",
		"terms":[],"recent_votes":[],"attendance":null}`)
	var entry struct {
		Level    string `json:"level"`
		Msg      string `json:"msg"`
		MemberID string `json:"member_id"`
	}
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("log = %q, want one JSON line: %v", logs.String(), err)
	}
	if entry.Level != "WARN" || entry.MemberID != "P000197" || entry.Msg != "failed to get member attendance" {
		t.Errorf("log = %+v, want a WARN naming member P000197", entry)
	}
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
	// A state and district picked by the clock, so runs sharing a Redis are unlikely to see each
	// other's keys (the member list reads no search, so a search can't set one apart, #943).
	now := time.Now().UnixNano()
	state := string([]byte{byte('A' + now%26), byte('A' + now/26%26)})
	district := int(now / 676 % 100)
	t.Cleanup(func() {
		_ = c.Delete(context.Background(), handler.ListCacheKey("members:list", model.ListParams{
			Limit: 20, State: &state, District: &district,
		}))
	})
	query := fmt.Sprintf("/api/v1/members?state=%s&district=%d", state, district)
	members := seededMembers()
	h := newTestHandler(&mockBillRepo{})
	h.Members = members
	h.SetCache(c)
	router := memberRouter(h)

	first := get(t, router, query)
	second := get(t, router, query)

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
