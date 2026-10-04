package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

// --- Mocks ---

type mockGraphRepo struct {
	related       []model.RelatedBill
	collaborators []model.Collaborator
	companion     []model.CompanionVote
	err           error

	calls    int
	id       string
	congress int
	limit    int
	deadline time.Duration
}

func (m *mockGraphRepo) record(ctx context.Context, id string, congress, limit int) {
	m.calls++
	m.id, m.congress, m.limit = id, congress, limit
	if d, ok := ctx.Deadline(); ok {
		m.deadline = time.Until(d)
	}
}

func (m *mockGraphRepo) RelatedBills(ctx context.Context, billID string, limit int) ([]model.RelatedBill, error) {
	m.record(ctx, billID, 0, limit)
	return m.related, m.err
}

func (m *mockGraphRepo) Collaborators(
	ctx context.Context, memberID string, congress, limit int,
) ([]model.Collaborator, error) {
	m.record(ctx, memberID, congress, limit)
	return m.collaborators, m.err
}

func (m *mockGraphRepo) CompanionVotes(ctx context.Context, billID string, limit int) ([]model.CompanionVote, error) {
	m.record(ctx, billID, 0, limit)
	return m.companion, m.err
}

func (m *mockGraphRepo) BillsChangingSection(
	_ context.Context, _, _ string, _, _ int,
) ([]model.SectionBill, error) {
	return nil, errors.New("not used by handlers")
}

func (m *mockGraphRepo) LawChangedByBill(_ context.Context, _ string, _ int) ([]model.LawRef, error) {
	return nil, errors.New("not used by handlers")
}

type listCongressRepo struct {
	congresses []model.Congress
	err        error
}

func (m *listCongressRepo) List(_ context.Context) ([]model.Congress, error) {
	return m.congresses, m.err
}

func newGraphRouter(g *mockGraphRepo, congresses *listCongressRepo) (*handler.Handler, *chi.Mux) {
	h := newTestHandler(&mockBillRepo{})
	h.Graph = g
	if congresses != nil {
		h.Congresses = congresses
	}
	h.SetLogger(slog.New(slog.DiscardHandler))

	r := chi.NewRouter()
	r.Get("/api/v1/bills/{id}/related", h.GetRelatedBills)
	r.Get("/api/v1/bills/{id}/companion-votes", h.GetCompanionVotes)
	r.Get("/api/v1/members/{id}/collaborators", h.GetCollaborators)
	return h, r
}

func serve(r http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// --- GET /bills/{id}/related ---

func TestGetRelatedBills(t *testing.T) {
	g := &mockGraphRepo{related: []model.RelatedBill{
		{BillID: "s-119-1", Congress: 119, BillType: "s", Number: 1, RelationTypes: []string{"Identical bill"}},
		{BillID: "hr-118-1", Congress: 118, BillType: "hr", Number: 1, SharedSubjects: 3},
	}}
	_, r := newGraphRouter(g, nil)

	w := serve(r, "/api/v1/bills/hr-119-1/related")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
	var got []model.RelatedBill
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].BillID != "s-119-1" || got[1].SharedSubjects != 3 {
		t.Errorf("body = %+v, want the repo's rows in order", got)
	}
	if g.id != "hr-119-1" || g.limit != 10 {
		t.Errorf("repo got (%q, limit %d), want (hr-119-1, 10)", g.id, g.limit)
	}
	if g.deadline <= 0 || g.deadline > 2*time.Second {
		t.Errorf("query deadline = %v, want within 2s", g.deadline)
	}
}

func TestGetRelatedBills_Limit(t *testing.T) {
	for query, want := range map[string]int{
		"?limit=5":   5,
		"?limit=500": 50,
		"?limit=0":   10,
		"?limit=-3":  10,
		"?limit=abc": 10,
	} {
		t.Run(query, func(t *testing.T) {
			g := &mockGraphRepo{related: []model.RelatedBill{}}
			_, r := newGraphRouter(g, nil)
			if w := serve(r, "/api/v1/bills/hr-119-1/related"+query); w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if g.limit != want {
				t.Errorf("limit = %d, want %d", g.limit, want)
			}
		})
	}
}

func TestGetRelatedBills_Empty(t *testing.T) {
	_, r := newGraphRouter(&mockGraphRepo{related: []model.RelatedBill{}}, nil)

	w := serve(r, "/api/v1/bills/sres-119-12/related")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if body := w.Body.String(); body != "[]\n" {
		t.Errorf("body = %q, want an empty JSON array", body)
	}
}

func TestGetRelatedBills_InvalidID(t *testing.T) {
	for _, id := range []string{"HR-119-1", "hr-119", "xx-119-1", "hr-0-1", "hr-119-1a", "hr-+119-1", "hr-119-1-2"} {
		t.Run(id, func(t *testing.T) {
			g := &mockGraphRepo{}
			_, r := newGraphRouter(g, nil)
			if w := serve(r, "/api/v1/bills/"+id+"/related"); w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			if g.calls != 0 {
				t.Error("repo was queried for an invalid id")
			}
		})
	}
}

func TestGetRelatedBills_Errors(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"failure": {errors.New("spanner: unavailable"), http.StatusInternalServerError},
		"timeout": {fmt.Errorf("spanner: %w", context.DeadlineExceeded), http.StatusGatewayTimeout},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, r := newGraphRouter(&mockGraphRepo{err: tc.err}, nil)
			if w := serve(r, "/api/v1/bills/hr-119-1/related"); w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// --- GET /members/{id}/collaborators ---

func TestGetCollaborators(t *testing.T) {
	g := &mockGraphRepo{collaborators: []model.Collaborator{
		{BioguideID: "B000002", FirstName: "Ann", LastName: "Bee", Party: new("D"), SharedBills: 4},
	}}
	congresses := &listCongressRepo{congresses: []model.Congress{
		{Number: 120}, {Number: 119, IsCurrent: true}, {Number: 118},
	}}
	_, r := newGraphRouter(g, congresses)

	w := serve(r, "/api/v1/members/A000001/collaborators")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
	var got struct {
		Congress      int                  `json:"congress"`
		Collaborators []model.Collaborator `json:"collaborators"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Congress != 119 || len(got.Collaborators) != 1 || got.Collaborators[0].SharedBills != 4 {
		t.Errorf("body = %+v, want congress 119 and the repo's row", got)
	}
	if g.id != "A000001" || g.congress != 119 || g.limit != 20 {
		t.Errorf("repo got (%q, %d, limit %d), want (A000001, 119, 20)", g.id, g.congress, g.limit)
	}
	if g.deadline <= 0 || g.deadline > 2*time.Second {
		t.Errorf("query deadline = %v, want within 2s", g.deadline)
	}
}

func TestGetCollaborators_Congress(t *testing.T) {
	tests := map[string]struct {
		query      string
		congresses []model.Congress
		want       int
	}{
		"explicit":                 {"?congress=118&limit=500", []model.Congress{{Number: 119, IsCurrent: true}}, 118},
		"newest when none current": {"", []model.Congress{{Number: 118}, {Number: 119}}, 119},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := &mockGraphRepo{collaborators: []model.Collaborator{}}
			_, r := newGraphRouter(g, &listCongressRepo{congresses: tc.congresses})
			if w := serve(r, "/api/v1/members/A000001/collaborators"+tc.query); w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if g.congress != tc.want {
				t.Errorf("congress = %d, want %d", g.congress, tc.want)
			}
		})
	}
}

func TestGetCollaborators_BadRequests(t *testing.T) {
	current := &listCongressRepo{congresses: []model.Congress{{Number: 119, IsCurrent: true}}}
	tests := map[string]struct {
		path       string
		congresses *listCongressRepo
		want       int
	}{
		"lower-case id": {"/api/v1/members/a000001/collaborators", current, http.StatusBadRequest},
		"short id":      {"/api/v1/members/A00001/collaborators", current, http.StatusBadRequest},
		"non-digit id":  {"/api/v1/members/AB00001/collaborators", current, http.StatusBadRequest},
		"bad congress":  {"/api/v1/members/A000001/collaborators?congress=abc", current, http.StatusBadRequest},
		"zero congress": {"/api/v1/members/A000001/collaborators?congress=0", current, http.StatusBadRequest},
		"no congresses": {"/api/v1/members/A000001/collaborators", &listCongressRepo{}, http.StatusBadRequest},
		"congresses error": {
			"/api/v1/members/A000001/collaborators",
			&listCongressRepo{err: errors.New("spanner: unavailable")},
			http.StatusInternalServerError,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := &mockGraphRepo{}
			_, r := newGraphRouter(g, tc.congresses)
			if w := serve(r, tc.path); w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
			if g.calls != 0 {
				t.Error("repo was queried for a bad request")
			}
		})
	}
}

func TestGetCollaborators_Timeout(t *testing.T) {
	g := &mockGraphRepo{err: fmt.Errorf("spanner: %w", context.DeadlineExceeded)}
	_, r := newGraphRouter(g, nil)
	if w := serve(r, "/api/v1/members/A000001/collaborators?congress=119"); w.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", w.Code)
	}
}

// --- GET /bills/{id}/companion-votes ---

// companionRollCall mirrors the endpoint's JSON.
type companionRollCall struct {
	CompanionBillID string    `json:"companion_bill_id"`
	VoteID          string    `json:"vote_id"`
	Chamber         string    `json:"chamber"`
	VoteDate        time.Time `json:"vote_date"`
	Question        *string   `json:"question"`
	Result          *string   `json:"result"`
	Votes           []struct {
		MemberID string  `json:"member_id"`
		LastName string  `json:"last_name"`
		Party    *string `json:"party"`
		Vote     string  `json:"vote"`
	} `json:"votes"`
}

// companionRows returns member votes on the given roll calls of S 1, n members each, in the
// repo's order (newest roll call first).
func companionRows(n int, voteIDs ...string) []model.CompanionVote {
	var rows []model.CompanionVote
	for i, voteID := range voteIDs {
		date := time.Date(2025, time.April, 10-i, 17, 0, 0, 0, time.UTC)
		for m := range n {
			rows = append(rows, model.CompanionVote{
				CompanionBillID: "s-119-1", VoteID: voteID, Chamber: "Senate", VoteDate: date,
				Question: new("On Passage of the Bill"), Result: new("Bill Passed"),
				MemberID: fmt.Sprintf("C%06d", m), LastName: "Chen", Party: new("R"), Vote: "Yea",
			})
		}
	}
	return rows
}

func decodeRollCalls(t *testing.T, w *httptest.ResponseRecorder) []companionRollCall {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
	var got []companionRollCall
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestGetCompanionVotes(t *testing.T) {
	rows := companionRows(2, "senate-119-s1-vote00002", "senate-119-s1-vote00001")
	rows[1].Vote, rows[1].Party = "Nay", nil
	g := &mockGraphRepo{companion: rows}
	_, r := newGraphRouter(g, nil)

	got := decodeRollCalls(t, serve(r, "/api/v1/bills/hr-119-1/companion-votes"))
	if len(got) != 2 {
		t.Fatalf("roll calls = %+v, want two", got)
	}
	first := got[0]
	if first.VoteID != "senate-119-s1-vote00002" || first.CompanionBillID != "s-119-1" ||
		first.Chamber != "Senate" || first.Question == nil || *first.Question != "On Passage of the Bill" ||
		first.Result == nil || *first.Result != "Bill Passed" || first.VoteDate.Day() != 10 {
		t.Errorf("first roll call = %+v, want the newest one with its details", first)
	}
	if len(first.Votes) != 2 || first.Votes[0].Vote != "Yea" || first.Votes[1].Vote != "Nay" ||
		first.Votes[1].Party != nil || first.Votes[0].MemberID != "C000000" {
		t.Errorf("first roll call votes = %+v, want the repo's two member votes", first.Votes)
	}
	if got[1].VoteID != "senate-119-s1-vote00001" || len(got[1].Votes) != 2 {
		t.Errorf("second roll call = %+v", got[1])
	}
	if g.id != "hr-119-1" || g.limit != 2000 {
		t.Errorf("repo got (%q, limit %d), want (hr-119-1, 2000)", g.id, g.limit)
	}
	if g.deadline <= 0 || g.deadline > 2*time.Second {
		t.Errorf("query deadline = %v, want within 2s", g.deadline)
	}
}

func TestGetCompanionVotes_Truncated(t *testing.T) {
	// 2000 rows: 4 full House roll calls of 435 and 260 members of a fifth, which is dropped.
	rows := companionRows(435, "v5", "v4", "v3", "v2", "v1")[:2000]
	_, r := newGraphRouter(&mockGraphRepo{companion: rows}, nil)

	got := decodeRollCalls(t, serve(r, "/api/v1/bills/s-119-1/companion-votes"))
	if len(got) != 4 || got[3].VoteID != "v2" || len(got[3].Votes) != 435 {
		t.Errorf("got %d roll calls, want the 4 complete ones", len(got))
	}

	// One roll call that fills the limit on its own is still shown.
	_, r = newGraphRouter(&mockGraphRepo{companion: companionRows(2000, "v1")}, nil)
	if got = decodeRollCalls(t, serve(r, "/api/v1/bills/s-119-1/companion-votes")); len(got) != 1 {
		t.Errorf("got %d roll calls, want the only one", len(got))
	}
}

func TestGetCompanionVotes_Empty(t *testing.T) {
	_, r := newGraphRouter(&mockGraphRepo{companion: []model.CompanionVote{}}, nil)

	w := serve(r, "/api/v1/bills/hr-118-1/companion-votes")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if body := w.Body.String(); body != "[]\n" {
		t.Errorf("body = %q, want an empty JSON array", body)
	}
}

func TestGetCompanionVotes_BadRequestsAndErrors(t *testing.T) {
	tests := map[string]struct {
		path  string
		err   error
		want  int
		calls int
	}{
		"malformed id": {"/api/v1/bills/hr-119/companion-votes", nil, http.StatusBadRequest, 0},
		"unknown type": {"/api/v1/bills/xx-119-1/companion-votes", nil, http.StatusBadRequest, 0},
		"failure": {
			"/api/v1/bills/hr-119-1/companion-votes", errors.New("spanner: unavailable"),
			http.StatusInternalServerError, 1,
		},
		"timeout": {
			"/api/v1/bills/hr-119-1/companion-votes", fmt.Errorf("spanner: %w", context.DeadlineExceeded),
			http.StatusGatewayTimeout, 1,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := &mockGraphRepo{err: tc.err}
			_, r := newGraphRouter(g, nil)
			if w := serve(r, tc.path); w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
			if g.calls != tc.calls {
				t.Errorf("repo calls = %d, want %d", g.calls, tc.calls)
			}
		})
	}
}

// --- Caching (needs Redis) ---

func TestGraphCaching(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping Redis integration test")
	}
	c, err := cache.New(redisURL)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// A bill number unique to this run, so runs sharing a Redis don't see each other's keys.
	number := time.Now().UnixNano() % 1_000_000_000
	hit := fmt.Sprintf("hr-119-%d", number)
	miss := fmt.Sprintf("s-119-%d", number)
	t.Cleanup(func() {
		_ = c.Delete(context.Background(), "graph:related:"+hit+":10", "graph:related:"+miss+":10")
	})

	g := &mockGraphRepo{related: []model.RelatedBill{{BillID: "s-119-1", RelationTypes: []string{}}}}
	h, r := newGraphRouter(g, nil)
	h.SetCache(c)

	first := serve(r, "/api/v1/bills/"+hit+"/related")
	second := serve(r, "/api/v1/bills/"+hit+"/related")
	if g.calls != 1 {
		t.Errorf("repo calls = %d, want 1 (second request from cache)", g.calls)
	}
	if strings.TrimSpace(first.Body.String()) != second.Body.String() {
		t.Errorf("cached body %q differs from %q", second.Body, first.Body)
	}

	g.related, g.calls = []model.RelatedBill{}, 0
	serve(r, "/api/v1/bills/"+miss+"/related")
	serve(r, "/api/v1/bills/"+miss+"/related")
	if g.calls != 2 {
		t.Errorf("repo calls = %d, want 2 (empty results aren't cached)", g.calls)
	}
}

func TestCompanionVotesCaching(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping Redis integration test")
	}
	c, err := cache.New(redisURL)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	number := time.Now().UnixNano() % 1_000_000_000
	hit := fmt.Sprintf("hr-119-%d", number)
	miss := fmt.Sprintf("s-119-%d", number)
	t.Cleanup(func() {
		_ = c.Delete(context.Background(), "graph:companion-votes:"+hit, "graph:companion-votes:"+miss)
	})

	g := &mockGraphRepo{companion: companionRows(2, "senate-119-s1-vote00001")}
	h, r := newGraphRouter(g, nil)
	h.SetCache(c)

	first := serve(r, "/api/v1/bills/"+hit+"/companion-votes")
	second := serve(r, "/api/v1/bills/"+hit+"/companion-votes")
	if g.calls != 1 {
		t.Errorf("repo calls = %d, want 1 (second request from cache)", g.calls)
	}
	if strings.TrimSpace(first.Body.String()) != second.Body.String() {
		t.Errorf("cached body %q differs from %q", second.Body, first.Body)
	}

	g.companion, g.calls = []model.CompanionVote{}, 0
	serve(r, "/api/v1/bills/"+miss+"/companion-votes")
	serve(r, "/api/v1/bills/"+miss+"/companion-votes")
	if g.calls != 2 {
		t.Errorf("repo calls = %d, want 2 (empty results aren't cached)", g.calls)
	}
}
