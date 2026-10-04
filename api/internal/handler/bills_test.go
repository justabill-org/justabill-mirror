package handler_test

import (
	"context"
	"encoding/json"
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
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// --- Mocks ---

type mockBillRepo struct {
	listResult *model.ListResult[model.Bill]
	bill       *model.Bill
	actions    []model.BillAction
	summary    *model.BillSummary
	summaries  map[string]model.BillSummary
	summaryIDs []string
	// summariesErr, if set, fails GetSummaries.
	summariesErr error
	// cards is what GetCardFacts returns; cardIDs records the IDs it was asked for and cardErr,
	// if set, fails it.
	cards   map[string]model.BillCardFacts
	cardIDs []string
	cardErr error
	// crsLeads is what GetCRSLeads returns; crsLeadIDs records the IDs it was asked for and
	// crsLeadsErr, if set, fails it.
	crsLeads    map[string]model.CardCRS
	crsLeadIDs  []string
	crsLeadsErr error
	versions    []model.BillTextVersion
	diffs       []model.BillTextDiff
	amendments  []model.Amendment
	sponsors    []model.BillSponsorship
	crs         *model.CRSSummary
	// crsErr, if set, fails GetCRSSummary.
	crsErr error
	// craRule is what GetCRARule returns, with craRuleErr.
	craRule    *model.CRARule
	craRuleErr error
	// texts and diffByID are keyed by billKey(billID, id), like the bill-scoped repository reads.
	texts        map[string]*model.BillText
	diffByID     map[string]*model.BillTextDiff
	diffSummary  *model.BillTextDiffSummary
	summaryReads int
	index        []model.BillIndexEntry
	indexErr     error
	indexCalls   int
	indexOf      int
	// listCalls records the params of every List call.
	listCalls []model.ListParams
	// byIDCalls counts GetByID calls, the first read of GET /bills/{id}.
	byIDCalls int
	// actionsErr, if set, fails GetActions.
	actionsErr error
	// counts is what CountByStatus returns, with countErr; countCalls records its params.
	counts     *model.BillCounts
	countErr   error
	countCalls []model.ListParams
	// policyAreas and policyAreasErr answer PolicyAreas; policyAreaCalls counts its calls.
	policyAreas     []string
	policyAreasErr  error
	policyAreaCalls int
	// statuses and statusesErr answer StatusIndex; statusCalls counts its calls and statusesOf
	// records the congress of the last one.
	statuses    []model.BillStatusIndexEntry
	statusesErr error
	statusCalls int
	statusesOf  int
}

func billKey(billID, id string) string { return billID + "/" + id }

func (m *mockBillRepo) List(_ context.Context, params model.ListParams) (*model.ListResult[model.Bill], error) {
	m.listCalls = append(m.listCalls, params)
	return m.listResult, nil
}
func (m *mockBillRepo) CountByStatus(_ context.Context, params model.ListParams) (*model.BillCounts, error) {
	m.countCalls = append(m.countCalls, params)
	return m.counts, m.countErr
}
func (m *mockBillRepo) Index(_ context.Context, congress int) ([]model.BillIndexEntry, error) {
	m.indexCalls++
	m.indexOf = congress
	if m.index == nil {
		return []model.BillIndexEntry{}, m.indexErr
	}
	return m.index, m.indexErr
}
func (m *mockBillRepo) StatusIndex(_ context.Context, congress int) ([]model.BillStatusIndexEntry, error) {
	m.statusCalls++
	m.statusesOf = congress
	if m.statuses == nil && m.statusesErr == nil {
		return []model.BillStatusIndexEntry{}, nil
	}
	return m.statuses, m.statusesErr
}
func (m *mockBillRepo) PolicyAreas(_ context.Context) ([]string, error) {
	m.policyAreaCalls++
	if m.policyAreas == nil && m.policyAreasErr == nil {
		return []string{}, nil
	}
	return m.policyAreas, m.policyAreasErr
}
func (m *mockBillRepo) GetByID(_ context.Context, _ string) (*model.Bill, error) {
	m.byIDCalls++
	return m.bill, nil
}
func (m *mockBillRepo) GetActions(_ context.Context, _ string) ([]model.BillAction, error) {
	if m.actionsErr != nil {
		return []model.BillAction{{ActionText: "partial"}}, m.actionsErr
	}
	return m.actions, nil
}
func (m *mockBillRepo) GetSummary(_ context.Context, _ string) (*model.BillSummary, error) {
	return m.summary, nil
}
func (m *mockBillRepo) GetSummaries(_ context.Context, ids []string) (map[string]model.BillSummary, error) {
	m.summaryIDs = ids
	return m.summaries, m.summariesErr
}
func (m *mockBillRepo) GetTextVersions(_ context.Context, _ string) ([]model.BillTextVersion, error) {
	return m.versions, nil
}
func (m *mockBillRepo) GetDiffs(_ context.Context, _ string) ([]model.BillTextDiff, error) {
	return m.diffs, nil
}
func (m *mockBillRepo) GetAmendments(_ context.Context, _ string) ([]model.Amendment, error) {
	return m.amendments, nil
}
func (m *mockBillRepo) GetTextContent(_ context.Context, billID, versionID string) (*model.BillText, error) {
	return m.texts[billKey(billID, versionID)], nil
}
func (m *mockBillRepo) GetDiffByID(_ context.Context, billID, diffID string) (*model.BillTextDiff, error) {
	return m.diffByID[billKey(billID, diffID)], nil
}
func (m *mockBillRepo) GetDiffSummary(_ context.Context, _ string) (*model.BillTextDiffSummary, error) {
	m.summaryReads++
	return m.diffSummary, nil
}
func (m *mockBillRepo) GetStatusHistory(_ context.Context, _ string) ([]model.BillStatusEntry, error) {
	return []model.BillStatusEntry{}, nil
}
func (m *mockBillRepo) GetSponsorships(_ context.Context, _ string) ([]model.BillSponsorship, error) {
	return m.sponsors, nil
}
func (m *mockBillRepo) ListGAOReports(_ context.Context, _ string) ([]model.GAOReport, error) {
	return []model.GAOReport{}, nil
}
func (m *mockBillRepo) GetCRSSummary(_ context.Context, _ string) (*model.CRSSummary, error) {
	return m.crs, m.crsErr
}

func (m *mockBillRepo) GetCRARule(_ context.Context, _ string) (*model.CRARule, error) {
	return m.craRule, m.craRuleErr
}
func (m *mockBillRepo) GetCardFacts(_ context.Context, ids []string) (map[string]model.BillCardFacts, error) {
	m.cardIDs = ids
	if m.cards == nil {
		return map[string]model.BillCardFacts{}, m.cardErr
	}
	return m.cards, m.cardErr
}
func (m *mockBillRepo) GetCRSLeads(_ context.Context, ids []string) (map[string]model.CardCRS, error) {
	m.crsLeadIDs = ids
	if m.crsLeads == nil {
		return map[string]model.CardCRS{}, m.crsLeadsErr
	}
	return m.crsLeads, m.crsLeadsErr
}

type mockMemberRepo struct{}

func (m *mockMemberRepo) List(_ context.Context, _ model.ListParams) (*model.ListResult[model.Member], error) {
	return &model.ListResult[model.Member]{Items: []model.Member{}}, nil
}
func (m *mockMemberRepo) GetByID(_ context.Context, _ string) (*model.MemberDetail, error) {
	return nil, nil //nolint:nilnil // mock
}
func (m *mockMemberRepo) GetByDistrict(_ context.Context, _ string, _ int) ([]model.Member, error) {
	return nil, nil
}
func (m *mockMemberRepo) GetSenators(_ context.Context, _ string) ([]model.Member, error) {
	return nil, nil
}
func (m *mockMemberRepo) GetRecentVotes(
	_ context.Context, _ string, _ int,
) ([]model.MemberVoteSummary, error) {
	return []model.MemberVoteSummary{}, nil
}

type mockUserRepo struct{}

func (m *mockUserRepo) CreateForAuthUID(_ context.Context, _, _, _ string) (*model.User, error) {
	return &model.User{}, nil
}
func (m *mockUserRepo) GetByID(_ context.Context, _ string) (*model.User, error) {
	return nil, nil //nolint:nilnil // mock
}
func (m *mockUserRepo) GetByAuthUID(_ context.Context, _ string) (*model.User, error) {
	return nil, nil //nolint:nilnil // mock
}
func (m *mockUserRepo) Delete(_ context.Context, _ string) error { return nil }
func (m *mockUserRepo) ImportVotes(
	_ context.Context, _ string, _ []model.UserVote, _ model.VoteChecks,
) (model.ImportResult, error) {
	return model.ImportResult{}, nil
}
func (m *mockUserRepo) DeleteVote(_ context.Context, _, _ string) error { return nil }
func (m *mockUserRepo) Export(_ context.Context, _ string) (*model.UserExport, error) {
	return nil, nil //nolint:nilnil // mock
}
func (m *mockUserRepo) Update(_ context.Context, _ string, _ model.UserUpdate) (*model.User, error) {
	return &model.User{}, nil
}
func (m *mockUserRepo) CastVote(_ context.Context, _, _, _ string, _ model.VoteChecks) error {
	return nil
}
func (m *mockUserRepo) GetVotes(
	_ context.Context, _ string, _ model.ListParams,
) (*model.ListResult[model.UserVote], error) {
	return &model.ListResult[model.UserVote]{Items: []model.UserVote{}}, nil
}
func (m *mockUserRepo) AddFavorite(_ context.Context, _, _ string) error    { return nil }
func (m *mockUserRepo) RemoveFavorite(_ context.Context, _, _ string) error { return nil }
func (m *mockUserRepo) GetFavorites(
	_ context.Context, _ string, _ model.ListParams,
) (*model.ListResult[model.UserFavorite], error) {
	return &model.ListResult[model.UserFavorite]{Items: []model.UserFavorite{}}, nil
}

type mockVoteRepo struct {
	votes       []model.CongressionalVote
	memberVotes []model.MemberVote
}

func (m *mockVoteRepo) GetCongressionalVotes(_ context.Context, _ string) ([]model.CongressionalVote, error) {
	return m.votes, nil
}
func (m *mockVoteRepo) GetMemberVotes(_ context.Context, _ string) ([]model.MemberVote, error) {
	return m.memberVotes, nil
}
func (m *mockVoteRepo) MemberPositions(_ context.Context, _ string, _ int) ([]model.MemberPosition, error) {
	return []model.MemberPosition{}, nil
}

type mockCongressRepo struct{}

func (m *mockCongressRepo) List(_ context.Context) ([]model.Congress, error) {
	return []model.Congress{}, nil
}

type mockScorecard struct{}

func (m *mockScorecard) GetScorecard(_ context.Context, _ string, _ []int) ([]model.RepScore, error) {
	return []model.RepScore{}, nil
}
func (m *mockScorecard) CompareWithMember(
	_ context.Context, _, _ string, _ []int,
) ([]model.VoteComparison, error) {
	return []model.VoteComparison{}, nil
}

func newTestHandler(bills repository.BillRepo) *handler.Handler {
	h := &handler.Handler{
		Bills:      bills,
		Members:    &mockMemberRepo{},
		Users:      &mockUserRepo{},
		Votes:      &mockVoteRepo{},
		Congresses: &mockCongressRepo{},
		Scorecard:  &mockScorecard{},
	}
	h.SetLogger(slog.New(slog.DiscardHandler))
	return h
}

// --- Tests ---

func TestListBills(t *testing.T) {
	h := newTestHandler(&mockBillRepo{
		listResult: &model.ListResult[model.Bill]{
			Items:  []model.Bill{{ID: "hr-119-1", Title: "Test Bill"}},
			Total:  1,
			Offset: 0,
			Limit:  20,
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil)
	w := httptest.NewRecorder()
	h.ListBills(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	items, ok := body["items"].([]any)
	if !ok {
		t.Fatal("expected 'items' array in response")
	}
	if len(items) != 1 {
		t.Errorf("expected 1 item, got %d", len(items))
	}
}

func TestListBills_IncludeSummary(t *testing.T) {
	short, who := "Funds the thing.", "Rural hospitals."
	repo := &mockBillRepo{
		listResult: &model.ListResult[model.Bill]{
			Items: []model.Bill{{ID: "hr-119-1", Title: "One"}, {ID: "s-119-2", Title: "Two"}},
			Total: 7, Offset: 0, Limit: 2,
		},
		summaries: map[string]model.BillSummary{
			"hr-119-1": {BillID: "hr-119-1", ShortSummary: &short, WhoItAffects: &who},
		},
	}
	h := newTestHandler(repo)

	w := httptest.NewRecorder()
	h.ListBills(w, httptest.NewRequest(http.MethodGet, "/api/v1/bills?limit=2&include=summary", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}

	var body struct {
		Items []struct {
			ID      string         `json:"id"`
			Title   string         `json:"title"`
			Summary map[string]any `json:"summary"`
		} `json:"items"`
		Total int `json:"total"`
		Limit int `json:"limit"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 7 || body.Limit != 2 || len(body.Items) != 2 {
		t.Fatalf("page = %+v", body)
	}
	// One batch read for the page, in list order.
	if len(repo.summaryIDs) != 2 || repo.summaryIDs[0] != "hr-119-1" || repo.summaryIDs[1] != "s-119-2" {
		t.Errorf("summary IDs = %v", repo.summaryIDs)
	}
	first, second := body.Items[0], body.Items[1]
	// who_it_affects only: the why_it_matters alias is gone (design 199).
	_, alias := first.Summary["why_it_matters"]
	if first.ID != "hr-119-1" || first.Title != "One" || first.Summary["short_summary"] != short ||
		first.Summary["who_it_affects"] != who || alias {
		t.Errorf("first item = %+v", first)
	}
	if second.ID != "s-119-2" || second.Summary != nil {
		t.Errorf("second item = %+v, want no summary", second)
	}
}

func TestListBills_WithoutIncludeHasNoSummaries(t *testing.T) {
	repo := &mockBillRepo{listResult: &model.ListResult[model.Bill]{Items: []model.Bill{{ID: "hr-119-1"}}}}
	h := newTestHandler(repo)

	w := httptest.NewRecorder()
	h.ListBills(w, httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil))

	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body.Items[0]["summary"]; ok || repo.summaryIDs != nil {
		t.Errorf("summaries read without include=summary: %v", body.Items[0])
	}
	if _, ok := body.Items[0]["card"]; ok || repo.cardIDs != nil {
		t.Errorf("card facts read without include=card: %v", body.Items[0])
	}
}

func TestListBills_UnknownInclude(t *testing.T) {
	for _, q := range []string{"include=text", "include=summary,votes", "include=card&include=cards", "include=crs"} {
		h := newTestHandler(&mockBillRepo{listResult: &model.ListResult[model.Bill]{}})
		w := httptest.NewRecorder()
		h.ListBills(w, httptest.NewRequest(http.MethodGet, "/api/v1/bills?"+q, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
		// The message names every value a client may send.
		for _, value := range []string{`\"summary\"`, `\"card\"`, `\"crs_summary\"`} {
			if msg := w.Body.String(); !strings.Contains(msg, value) {
				t.Errorf("%s: body = %s, want it to name %s", q, msg, value)
			}
		}
	}
}

// TestListBills_UnvotedUser checks where unvoted=true gets its user: only from
// the context the auth middleware fills, never from the old X-Dev-User-Id
// header.
func TestListBills_UnvotedUser(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		user   string
		header string
		want   string
	}{
		{name: "signed in", query: "unvoted=true", user: "u-alice", want: "u-alice"},
		{name: "anonymous ignores unvoted", query: "unvoted=true"},
		{name: "dev header is ignored", query: "unvoted=true", header: "u-alice"},
		{name: "signed in without unvoted", query: "congress=119", user: "u-alice"},
		{name: "unvoted must be true", query: "unvoted=1", user: "u-alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bills := &mockBillRepo{listResult: &model.ListResult[model.Bill]{}}
			h := newTestHandler(bills)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/bills?"+tt.query, nil)
			if tt.header != "" {
				req.Header.Set("X-Dev-User-Id", tt.header)
			}
			if tt.user != "" {
				req = req.WithContext(mw.ContextWithUserID(req.Context(), tt.user))
			}
			w := httptest.NewRecorder()
			h.ListBills(w, req)
			if w.Code != http.StatusOK || len(bills.listCalls) != 1 {
				t.Fatalf("status = %d with %d List calls, want 200 and 1", w.Code, len(bills.listCalls))
			}
			got := ""
			if p := bills.listCalls[0].UnvotedBy; p != nil {
				got = *p
			}
			if got != tt.want {
				t.Errorf("UnvotedBy = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestListBills_UnvotedSkipsCache is the #63 leak: a signed-in user's unvoted
// list must neither come from nor go into the shared list cache, whose key
// has no user in it.
func TestListBills_UnvotedSkipsCache(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping Redis integration test")
	}
	c, err := cache.New(redisURL)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// A query unique to this run, so runs sharing a Redis don't see each other's keys.
	search := fmt.Sprintf("leak%d", time.Now().UnixNano())
	query := "unvoted=true&q=" + search
	t.Cleanup(func() {
		_ = c.Delete(context.Background(), handler.ListCacheKey("bills:list", model.ListParams{
			Limit: 20, Search: &search,
		}))
	})

	bills := &mockBillRepo{listResult: &model.ListResult[model.Bill]{Items: []model.Bill{{ID: "hr-119-1"}}}}
	h := newTestHandler(bills)
	h.SetCache(c)
	list := func(user string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/bills?"+query, nil)
		if user != "" {
			req = req.WithContext(mw.ContextWithUserID(req.Context(), user))
		}
		w := httptest.NewRecorder()
		h.ListBills(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
	}

	list("u-alice") // must not fill the cache
	list("")        // so this reads the repository, then caches the public list
	list("")        // from the cache
	list("u-bob")   // must not read the cache
	if len(bills.listCalls) != 3 {
		t.Fatalf("List calls = %d, want 3 (only the second anonymous request is a cache hit)", len(bills.listCalls))
	}
	for i, want := range []string{"u-alice", "", "u-bob"} {
		got := ""
		if p := bills.listCalls[i].UnvotedBy; p != nil {
			got = *p
		}
		if got != want {
			t.Errorf("List call %d: UnvotedBy = %q, want %q", i, got, want)
		}
	}
}

func TestGetBill_NotFound(t *testing.T) {
	h := newTestHandler(&mockBillRepo{bill: nil})

	r := chi.NewRouter()
	r.Get("/api/v1/bills/{id}", h.GetBill)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bills/hr-119-1234", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

// TestGetBill_Laws checks that bill.laws carries the laws a bill became (#709), and is absent
// for a bill that isn't law.
func TestGetBill_Laws(t *testing.T) {
	tests := []struct {
		name string
		laws []model.BillLaw
		want string
	}{
		{"public law", []model.BillLaw{{Type: model.BillLawTypePublic, Number: "119-95"}},
			`[{"type":"Public Law","number":"119-95"}]`},
		{"private law", []model.BillLaw{{Type: model.BillLawTypePrivate, Number: "117-3"}},
			`[{"type":"Private Law","number":"117-3"}]`},
		{"not law", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(&mockBillRepo{bill: &model.Bill{ID: "s-119-4530", Laws: tt.laws}})
			r := chi.NewRouter()
			r.Get("/api/v1/bills/{id}", h.GetBill)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/bills/s-119-4530", nil))

			var body struct {
				Bill map[string]json.RawMessage `json:"bill"`
			}
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got, found := body.Bill["laws"]
			if tt.want == "" {
				if found {
					t.Errorf("bill.laws = %s, want it absent", got)
				}
				return
			}
			if string(got) != tt.want {
				t.Errorf("bill.laws = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGetBill_Found(t *testing.T) {
	short, modelUsed, code, name := "Funds the thing.", "gemini-3.5-flash", "rh", "Reported in House"
	who := "Rural hospitals."
	h := newTestHandler(&mockBillRepo{
		bill: &model.Bill{ID: "hr-119-1", Title: "Test Bill", Congress: 119},
		sponsors: []model.BillSponsorship{
			{BioguideID: "A000001", FirstName: "Ada", LastName: "Alvarez", Role: "sponsor"},
		},
		summary: &model.BillSummary{
			BillID: "hr-119-1", ShortSummary: &short, ModelUsed: &modelUsed, WhoItAffects: &who,
			SourceVersionCode: &code, SourceVersionName: &name,
		},
	})

	r := chi.NewRouter()
	r.Get("/api/v1/bills/{id}", h.GetBill)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bills/hr-119-1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	bill, ok := body["bill"].(map[string]any)
	if !ok {
		t.Fatal("expected 'bill' object in response")
	}
	if bill["id"] != "hr-119-1" {
		t.Errorf("expected id hr-119-1, got %v", bill["id"])
	}
	// Raw user tallies are unthresholded and must never be served (design #89).
	if _, found := body["user_votes"]; found {
		t.Error("response must not include a raw 'user_votes' tally")
	}

	sponsorships, ok := body["sponsorships"].([]any)
	if !ok || len(sponsorships) != 1 {
		t.Fatalf("expected one sponsorship, got %v", body["sponsorships"])
	}
	if s, _ := sponsorships[0].(map[string]any); s["bioguide_id"] != "A000001" || s["role"] != "sponsor" {
		t.Errorf("unexpected sponsorship %v", sponsorships[0])
	}

	// The summary carries the text version it was written from next to the existing fields.
	summary, _ := body["summary"].(map[string]any)
	_, alias := summary["why_it_matters"]
	if summary["short_summary"] != short || summary["model_used"] != modelUsed ||
		summary["source_version_code"] != code || summary["source_version_name"] != name ||
		summary["who_it_affects"] != who || alias {
		t.Errorf("unexpected summary %v", body["summary"])
	}
}
