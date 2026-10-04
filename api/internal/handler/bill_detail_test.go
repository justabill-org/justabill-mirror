package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/cachekey"
	"github.com/justabill-org/justabill/db/model"
)

// billSections is the number of section reads GET /bills/{id} makes after
// GetByID: ten on the bill repo and the congressional votes.
const billSections = 11

// overlapTimeout bounds how long a section read waits for the others. With
// sequential reads the first one would wait all of it and fail.
const overlapTimeout = 5 * time.Second

// barrier makes every section read wait until all of them have started, so
// the reads only succeed if they run concurrently.
type barrier struct {
	started atomic.Int32
	all     chan struct{}
}

func newBarrier() *barrier { return &barrier{all: make(chan struct{})} }

func (b *barrier) arrive(ctx context.Context) error {
	if b.started.Add(1) == billSections {
		close(b.all)
	}
	select {
	case <-b.all:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(overlapTimeout):
		return errors.New("section reads did not overlap")
	}
}

// overlapBillRepo is a bill repo whose section reads wait at a barrier.
type overlapBillRepo struct {
	*mockBillRepo

	b *barrier
}

func (r *overlapBillRepo) GetActions(ctx context.Context, id string) ([]model.BillAction, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockBillRepo.GetActions(ctx, id)
}
func (r *overlapBillRepo) GetSummary(ctx context.Context, id string) (*model.BillSummary, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockBillRepo.GetSummary(ctx, id)
}
func (r *overlapBillRepo) GetTextVersions(ctx context.Context, id string) ([]model.BillTextVersion, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockBillRepo.GetTextVersions(ctx, id)
}
func (r *overlapBillRepo) GetDiffs(ctx context.Context, id string) ([]model.BillTextDiff, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockBillRepo.GetDiffs(ctx, id)
}
func (r *overlapBillRepo) GetAmendments(ctx context.Context, id string) ([]model.Amendment, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockBillRepo.GetAmendments(ctx, id)
}
func (r *overlapBillRepo) GetStatusHistory(ctx context.Context, id string) ([]model.BillStatusEntry, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockBillRepo.GetStatusHistory(ctx, id)
}
func (r *overlapBillRepo) ListGAOReports(ctx context.Context, id string) ([]model.GAOReport, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockBillRepo.ListGAOReports(ctx, id)
}
func (r *overlapBillRepo) GetSponsorships(ctx context.Context, id string) ([]model.BillSponsorship, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockBillRepo.GetSponsorships(ctx, id)
}

func (r *overlapBillRepo) GetCRSSummary(ctx context.Context, id string) (*model.CRSSummary, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockBillRepo.GetCRSSummary(ctx, id)
}

func (r *overlapBillRepo) GetCRARule(ctx context.Context, id string) (*model.CRARule, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockBillRepo.GetCRARule(ctx, id)
}

// overlapVoteRepo is a vote repo whose congressional votes read waits at the barrier.
type overlapVoteRepo struct {
	*mockVoteRepo

	b *barrier
}

func (r *overlapVoteRepo) GetCongressionalVotes(ctx context.Context, id string) ([]model.CongressionalVote, error) {
	if err := r.b.arrive(ctx); err != nil {
		return nil, err
	}
	return r.mockVoteRepo.GetCongressionalVotes(ctx, id)
}

// getBill serves GET /api/v1/bills/{id} through a chi router, so {id} is set.
func getBill(t *testing.T, h *handler.Handler, id string) (int, map[string]json.RawMessage) {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/v1/bills/{id}", h.GetBill)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/bills/"+id, nil))
	var body map[string]json.RawMessage
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return w.Code, body
}

// testCache connects to the Redis at REDIS_URL and skips the test without it.
func testCache(t *testing.T) *cache.Cache {
	t.Helper()
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping Redis integration test")
	}
	c, err := cache.New(redisURL)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// uniqueBillID returns a bill ID unique to this run, so runs sharing a Redis
// don't see each other's cached bills, and deletes its detail key afterwards.
func uniqueBillID(t *testing.T, c *cache.Cache) string {
	t.Helper()
	id := fmt.Sprintf("hr-119-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = c.Delete(context.Background(), cachekey.BillDetail(id)) })
	return id
}

func TestGetBill_SectionsRunConcurrently(t *testing.T) {
	b := newBarrier()
	bills := &overlapBillRepo{mockBillRepo: &mockBillRepo{
		bill:     &model.Bill{ID: "hr-119-1"},
		actions:  []model.BillAction{{ActionText: "Introduced"}},
		sponsors: []model.BillSponsorship{{BioguideID: "A000001", Role: "sponsor"}},
	}, b: b}
	h := newTestHandler(bills)
	h.Votes = &overlapVoteRepo{mockVoteRepo: &mockVoteRepo{votes: []model.CongressionalVote{{ID: "v1"}}}, b: b}

	code, body := getBill(t, h, "hr-119-1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := b.started.Load(); got != billSections {
		t.Errorf("section reads = %d, want %d", got, billSections)
	}
	// A read that timed out at the barrier would leave its section null.
	for _, key := range []string{"bill", "actions", "votes", "status_history", "gao_reports", "sponsorships"} {
		if raw := string(body[key]); raw == "" || raw == "null" {
			t.Errorf("%s = %q, want a value", key, raw)
		}
	}
}

func TestGetBill_FailedSectionIsNull(t *testing.T) {
	bills := &mockBillRepo{
		bill:       &model.Bill{ID: "hr-119-1"},
		actionsErr: errSpanner,
		sponsors:   []model.BillSponsorship{{BioguideID: "A000001", Role: "sponsor"}},
	}
	h := newTestHandler(bills)

	code, body := getBill(t, h, "hr-119-1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	// The repo returned a partial result with its error; the section must still be null.
	if got := string(body["actions"]); got != "null" {
		t.Errorf("actions = %s, want null", got)
	}
	if got := string(body["sponsorships"]); got == "null" {
		t.Error("sponsorships = null, want the other sections unaffected")
	}
}

func TestGetBill_CRSSummary(t *testing.T) {
	crs := &model.CRSSummary{
		BillID:      "s-119-5",
		VersionCode: "00",
		ActionDate:  time.Date(2025, 6, 5, 0, 0, 0, 0, time.UTC),
		ActionDesc:  "Introduced in Senate",
		Text:        "This bill does a thing.\n\n• First item",
		UpdatedAt:   time.Date(2026, 9, 28, 12, 47, 20, 0, time.UTC),
	}
	tests := []struct {
		name string
		repo *mockBillRepo
		want string
	}{
		{
			name: "latest summary",
			repo: &mockBillRepo{bill: &model.Bill{ID: "s-119-5"}, crs: crs},
			want: `{"bill_id":"s-119-5","version_code":"00","action_date":"2025-06-05T00:00:00Z",` +
				`"action_desc":"Introduced in Senate","text":"This bill does a thing.\n\n• First item",` +
				`"updated_at":"2026-09-28T12:47:20Z"}`,
		},
		{name: "none", repo: &mockBillRepo{bill: &model.Bill{ID: "s-119-5"}}, want: "null"},
		{name: "read error", repo: &mockBillRepo{bill: &model.Bill{ID: "s-119-5"}, crsErr: errSpanner}, want: "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := getBill(t, newTestHandler(tt.repo), "s-119-5")
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if got := string(body["crs_summary"]); got != tt.want {
				t.Errorf("crs_summary = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGetBill_FailedCRSSummaryNotCached(t *testing.T) {
	c := testCache(t)
	id := uniqueBillID(t, c)
	bills := &mockBillRepo{bill: &model.Bill{ID: id}, crsErr: errSpanner}
	h := newTestHandler(bills)
	h.SetCache(c)

	for range 2 {
		if code, _ := getBill(t, h, id); code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
	}
	if bills.byIDCalls != 2 {
		t.Errorf("GetByID calls = %d, want 2 (a response without its CRS summary isn't cached)", bills.byIDCalls)
	}
}

func TestGetBill_Cache(t *testing.T) {
	c := testCache(t)
	id := uniqueBillID(t, c)
	bills := &mockBillRepo{
		bill:    &model.Bill{ID: id, Title: "Cached Bill"},
		actions: []model.BillAction{{ActionText: "Introduced"}},
	}
	h := newTestHandler(bills)
	h.SetCache(c)

	code, first := getBill(t, h, id)
	if code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", code)
	}
	code, second := getBill(t, h, id)
	if code != http.StatusOK {
		t.Fatalf("second status = %d, want 200", code)
	}
	if bills.byIDCalls != 1 {
		t.Errorf("GetByID calls = %d, want 1 (the second request is a cache hit)", bills.byIDCalls)
	}
	if len(first) != len(second) {
		t.Fatalf("cached response has %d keys, the first had %d", len(second), len(first))
	}
	for key, raw := range first {
		if string(second[key]) != string(raw) {
			t.Errorf("cached %s = %s, want %s", key, second[key], raw)
		}
	}
}

func TestGetBill_FailedSectionNotCached(t *testing.T) {
	c := testCache(t)
	id := uniqueBillID(t, c)
	bills := &mockBillRepo{bill: &model.Bill{ID: id}, actionsErr: errSpanner}
	h := newTestHandler(bills)
	h.SetCache(c)

	for range 2 {
		if code, _ := getBill(t, h, id); code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
	}
	if bills.byIDCalls != 2 {
		t.Errorf("GetByID calls = %d, want 2 (a response with a failed section isn't cached)", bills.byIDCalls)
	}
}

func TestGetBill_NotFoundNotCached(t *testing.T) {
	c := testCache(t)
	bills := &mockBillRepo{}
	h := newTestHandler(bills)
	h.SetCache(c)
	id := uniqueBillID(t, c)

	for range 2 {
		if code, _ := getBill(t, h, id); code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", code)
		}
	}
	if bills.byIDCalls != 2 {
		t.Errorf("GetByID calls = %d, want 2", bills.byIDCalls)
	}
}

func TestListCacheKey(t *testing.T) {
	parse := func(query string) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/bills?"+query, nil)
		p, err := handler.ParseListParams(r)
		if err != nil {
			t.Fatalf("parse %q: %v", query, err)
		}
		return handler.ListCacheKey("bills:list", p)
	}
	base := parse("congress=119&type=hr")
	for _, query := range []string{
		"type=hr&congress=119",
		"congress=119&type=hr&x=12345",
		"bill_type=hr&congress=119",
		"congress=119&type=hr&offset=0&limit=20",
		"congress=119&type=hr&unvoted=true",
	} {
		if got := parse(query); got != base {
			t.Errorf("key(%q) = %q, want %q", query, got, base)
		}
	}
	// The same set of statuses shares one key, whatever its order or form (#712).
	laws := parse("congress=119&status=became_law,signed")
	for _, query := range []string{
		"congress=119&status=signed,became_law",
		"congress=119&status=signed&status=became_law",
		"status=became_law&congress=119&status=signed,became_law",
	} {
		if got := parse(query); got != laws {
			t.Errorf("key(%q) = %q, want %q", query, got, laws)
		}
	}
	if got, want := parse("status=passed_house"), "bills:list:limit=20&offset=0&status=passed_house"; got != want {
		t.Errorf("key(status=passed_house) = %q, want %q", got, want)
	}
	for _, query := range []string{
		"congress=119&status=became_law",
		"congress=119&status=became_law,signed&sort=latest_action",
	} {
		if got := parse(query); got == laws {
			t.Errorf("key(%q) = %q, want it to differ from %q", query, got, laws)
		}
	}
	for _, query := range []string{
		"congress=118&type=hr",
		"congress=119&type=hr&offset=20",
		"congress=119&type=hr&q=farm",
		"congress=119&type=hr&status=passed_house",
		"congress=119&type=hr&status=passed_house&status_mode=past",
		"congress=119&type=hr&policy_area=Health",
		"congress=119&type=hr&sort=latest_action",
		"congress=119&type=hr%26congress%3D118",
	} {
		if got := parse(query); got == base {
			t.Errorf("key(%q) = %q, want it to differ from %q", query, got, base)
		}
	}
	if got, want := parse(""), "bills:list:limit=20&offset=0"; got != want {
		t.Errorf("key() = %q, want %q", got, want)
	}
}

func TestListCacheKey_UnknownParamsShareCache(t *testing.T) {
	c := testCache(t)
	members := &countingMemberRepo{}
	h := newTestHandler(&mockBillRepo{})
	h.Members = members
	h.SetCache(c)

	// A state unique to this run, so runs sharing a Redis don't see each other's keys.
	state := fmt.Sprintf("T%d", time.Now().UnixNano())
	t.Cleanup(func() {
		chamber := "house"
		_ = c.Delete(context.Background(), handler.ListCacheKey("members:list", model.ListParams{
			Limit: 20, State: &state, Chamber: &chamber,
		}))
	})
	for _, query := range []string{
		"state=" + state + "&chamber=house",
		"chamber=house&state=" + state,
		"chamber=house&state=" + state + fmt.Sprintf("&x=%d", time.Now().UnixNano()),
	} {
		w := httptest.NewRecorder()
		h.ListMembers(w, httptest.NewRequest(http.MethodGet, "/api/v1/members?"+query, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", query, w.Code)
		}
	}
	if members.listCalls != 1 {
		t.Errorf("List calls = %d, want 1 (the reordered and junk-param queries are cache hits)", members.listCalls)
	}
}

// countingMemberRepo counts List calls.
type countingMemberRepo struct {
	mockMemberRepo

	listCalls int
}

func (m *countingMemberRepo) List(ctx context.Context, p model.ListParams) (*model.ListResult[model.Member], error) {
	m.listCalls++
	return m.mockMemberRepo.List(ctx, p)
}
