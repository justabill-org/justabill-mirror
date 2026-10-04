package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

const aggregatesCacheControl = "public, max-age=300"

// mockAggregates is a repository.AggregateReader. Like a careless implementation, it returns
// whatever it holds, suppressed cells and bookkeeping included, so the tests show the handler
// filters them itself.
type mockAggregates struct {
	cells     []model.VoteAggregate
	cell      *model.VoteAggregate
	members   []model.ConstituencyPosition
	alignment []model.RepAlignment
	err       error
	calls     int
}

func (m *mockAggregates) BillAggregates(_ context.Context, _ string) ([]model.VoteAggregate, error) {
	m.calls++
	return m.cells, m.err
}

func (m *mockAggregates) BillAggregate(_ context.Context, _, _ string) (*model.VoteAggregate, error) {
	m.calls++
	return m.cell, m.err
}

func (m *mockAggregates) MemberAlignment(_ context.Context, _ string) ([]model.RepAlignment, error) {
	m.calls++
	return m.alignment, m.err
}

func (m *mockAggregates) ConstituencyPositions(
	_ context.Context, _, _ string,
) ([]model.ConstituencyPosition, error) {
	return m.members, nil
}

// knownMemberRepo finds one member by ID.
type knownMemberRepo struct {
	mockMemberRepo

	id string
}

func (m *knownMemberRepo) GetByID(_ context.Context, id string) (*model.MemberDetail, error) {
	if id == m.id {
		return &model.MemberDetail{BioguideID: id}, nil
	}
	return nil, nil //nolint:nilnil // not found
}

func newAggregatesRouter(bills *mockBillRepo, agg *mockAggregates) (*handler.Handler, http.Handler) {
	opts := []handler.Option{handler.WithBills(bills), handler.WithMembers(&knownMemberRepo{id: "P000197"})}
	if agg != nil {
		opts = append(opts, handler.WithAggregates(agg))
	}
	h := handler.New(nil, opts...)
	h.SetLogger(slog.New(slog.DiscardHandler))
	r := chi.NewRouter()
	r.Get("/api/v1/bills/{id}/aggregates", h.GetBillAggregates)
	r.Get("/api/v1/bills/{id}/aggregates/{scope_key}", h.GetScopeAggregate)
	r.Get("/api/v1/members/{id}/alignment", h.GetMemberAlignment)
	return h, r
}

// testCells has one cell of each status, with the job's bookkeeping filled in.
func testCells(at time.Time) []model.VoteAggregate {
	published := at.Add(-time.Hour)
	return []model.VoteAggregate{
		{
			BillID: "hr-119-1", Scope: model.AggregateScopeNational, Status: model.AggregateStatusPublished,
			YeaPct: new(61), NayPct: new(39), VotersFloor: new(1230), PublishedAt: &published,
			ComputedAt: at.Add(-time.Minute), BasisYea: new(751), BasisNay: new(479),
		},
		{
			BillID: "hr-119-1", Scope: model.AggregateScopeState, ScopeKey: "CA", Status: model.AggregateStatusHeld,
			YeaPct: new(55), NayPct: new(45), VotersFloor: new(200), PublishedAt: &published, ComputedAt: at,
			BasisYea: new(111), BasisNay: new(90), HoldReason: new(model.HoldReasonBurst),
		},
		{
			BillID:     "hr-119-1",
			Scope:      model.AggregateScopeState,
			ScopeKey:   "TX",
			Status:     model.AggregateStatusSuppressed,
			ComputedAt: at.Add(time.Hour),
			BasisYea:   new(20),
			BasisNay:   new(9),
		},
		{
			BillID: "hr-119-1", Scope: model.AggregateScopeDistrict, ScopeKey: "CA-12",
			Status: model.AggregateStatusPublished, YeaPct: new(70), NayPct: new(30), VotersFloor: new(50),
			PublishedAt: &published, ComputedAt: at.Add(-2 * time.Minute), BasisYea: new(40), BasisNay: new(17),
		},
	}
}

// assertNoInternals fails if body carries a suppressed cell or the job's bookkeeping.
func assertNoInternals(t *testing.T, body string) {
	t.Helper()
	for _, banned := range []string{"basis", "hold_reason", "burst", "suppressed", "TX", "751"} {
		if strings.Contains(body, banned) {
			t.Errorf("body contains %q: %s", banned, body)
		}
	}
}

func TestAggregateRoutesOffAnswer404(t *testing.T) {
	_, r := newAggregatesRouter(&mockBillRepo{bill: &model.Bill{ID: "hr-119-1"}}, nil)
	for _, path := range []string{
		"/api/v1/bills/hr-119-1/aggregates", "/api/v1/bills/hr-119-1/aggregates/CA-12",
		"/api/v1/members/P000197/alignment",
	} {
		w := serve(r, path)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"aggregates_off"`) {
			t.Errorf("%s: %d %s, want 404 aggregates_off", path, w.Code, w.Body)
		}
	}
}

func TestGetBillAggregates(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	agg := &mockAggregates{cells: testCells(at)}
	_, r := newAggregatesRouter(&mockBillRepo{}, agg)

	w := serve(r, "/api/v1/bills/hr-119-1/aggregates")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Cache-Control"); got != aggregatesCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, aggregatesCacheControl)
	}
	assertNoInternals(t, w.Body.String())

	var resp struct {
		BillID   string     `json:"bill_id"`
		AsOf     *time.Time `json:"as_of"`
		National *struct {
			Status      string `json:"status"`
			YeaPct      int    `json:"yea_pct"`
			VotersFloor int    `json:"voters_floor"`
		} `json:"national"`
		States    []map[string]any `json:"states"`
		Districts []map[string]any `json:"districts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.BillID != "hr-119-1" || resp.AsOf == nil || !resp.AsOf.Equal(at) {
		t.Errorf("bill_id %q, as_of %v; want hr-119-1, %v (the newest served cell)", resp.BillID, resp.AsOf, at)
	}
	if resp.National == nil || resp.National.Status != model.AggregateStatusPublished ||
		resp.National.YeaPct != 61 || resp.National.VotersFloor != 1230 {
		t.Errorf("national = %+v", resp.National)
	}
	if len(resp.States) != 1 || resp.States[0]["scope_key"] != "CA" || resp.States[0]["status"] != "held" {
		t.Errorf("states = %v, want CA held only", resp.States)
	}
	if len(resp.Districts) != 1 || resp.Districts[0]["scope_key"] != "CA-12" {
		t.Errorf("districts = %v, want CA-12", resp.Districts)
	}
}

func TestGetBillAggregatesWithoutCells(t *testing.T) {
	tests := []struct {
		name   string
		bill   *model.Bill
		cells  []model.VoteAggregate
		status int
		body   string
	}{
		{
			"no cells yet", &model.Bill{ID: "hr-119-1"}, nil, http.StatusOK,
			`{"bill_id":"hr-119-1","as_of":null,"national":null,"states":[],"districts":[]}`,
		},
		{
			"only suppressed",
			&model.Bill{ID: "hr-119-1"},
			testCells(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))[2:3],
			http.StatusOK,
			`{"bill_id":"hr-119-1","as_of":null,"national":null,"states":[],"districts":[]}`,
		},
		{"no such bill", nil, nil, http.StatusNotFound, `{"error":"bill not found"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, r := newAggregatesRouter(&mockBillRepo{bill: tt.bill}, &mockAggregates{cells: tt.cells})
			w := serve(r, "/api/v1/bills/hr-119-1/aggregates")
			if w.Code != tt.status || strings.TrimSpace(w.Body.String()) != tt.body {
				t.Errorf("got %d %s, want %d %s", w.Code, w.Body, tt.status, tt.body)
			}
		})
	}
}

func TestAggregateRoutesRejectBadInput(t *testing.T) {
	agg := &mockAggregates{}
	_, r := newAggregatesRouter(&mockBillRepo{}, agg)
	for _, path := range []string{
		"/api/v1/bills/hr-119/aggregates",
		"/api/v1/bills/hr-119-1/aggregates/ca-12",
		"/api/v1/bills/hr-119-1/aggregates/CA-123",
		"/api/v1/bills/hr-119-1/aggregates/CA12",
		"/api/v1/bills/HR-1-1/aggregates/CA",
		"/api/v1/members/p1/alignment",
	} {
		if w := serve(r, path); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", path, w.Code)
		}
	}
	if agg.calls != 0 {
		t.Errorf("repo called %d times for bad requests", agg.calls)
	}
}

func TestAggregateRoutesServerErrors(t *testing.T) {
	_, r := newAggregatesRouter(&mockBillRepo{}, &mockAggregates{err: errors.New("spanner down")})
	for _, path := range []string{
		"/api/v1/bills/hr-119-1/aggregates", "/api/v1/bills/hr-119-1/aggregates/CA",
		"/api/v1/members/P000197/alignment",
	} {
		w := serve(r, path)
		if w.Code != http.StatusInternalServerError || w.Header().Get("Cache-Control") != "" {
			t.Errorf("%s: %d, Cache-Control %q; want 500 and none", path, w.Code, w.Header().Get("Cache-Control"))
		}
	}
}

func TestGetScopeAggregate(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cells := testCells(at)
	members := []model.ConstituencyPosition{{
		MemberID: "P000197", FirstName: "Nancy", LastName: "Pelosi", Party: "D", Congress: 119, Vote: "yea",
		VoteID: "house-119-1-10", Chamber: "House", VoteDate: at,
	}}
	_, r := newAggregatesRouter(&mockBillRepo{}, &mockAggregates{cell: &cells[3], members: members})

	w := serve(r, "/api/v1/bills/hr-119-1/aggregates/CA-12")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != aggregatesCacheControl {
		t.Fatalf("got %d, Cache-Control %q: %s", w.Code, w.Header().Get("Cache-Control"), w.Body)
	}
	assertNoInternals(t, w.Body.String())
	var resp struct {
		Scope    string `json:"scope"`
		ScopeKey string `json:"scope_key"`
		Cell     *struct {
			YeaPct int `json:"yea_pct"`
		} `json:"cell"`
		Members []model.ConstituencyPosition `json:"members"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Scope != model.AggregateScopeDistrict || resp.ScopeKey != "CA-12" || resp.Cell == nil ||
		resp.Cell.YeaPct != 70 || len(resp.Members) != 1 || resp.Members[0].Vote != "yea" {
		t.Errorf("response = %+v", resp)
	}
}

func TestGetScopeAggregateWithoutServedCell(t *testing.T) {
	cells := testCells(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	members := []model.ConstituencyPosition{{MemberID: "C001098", Vote: "nay", Chamber: "Senate"}}
	tests := []struct {
		name    string
		bill    *model.Bill
		cell    *model.VoteAggregate
		members []model.ConstituencyPosition
		status  int
		body    string
	}{
		{
			"suppressed cell, members' votes still shown", nil, &cells[2], members, http.StatusOK,
			`"scope":"state","scope_key":"TX","as_of":null,"cell":null,"members":[{"member_id":"C001098"`,
		},
		{
			"nothing yet", &model.Bill{ID: "hr-119-1"}, nil, nil, http.StatusOK,
			`"scope":"state","scope_key":"TX","as_of":null,"cell":null,"members":[]}`,
		},
		{"no such bill", nil, nil, nil, http.StatusNotFound, `{"error":"bill not found"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agg := &mockAggregates{cell: tt.cell, members: tt.members}
			_, r := newAggregatesRouter(&mockBillRepo{bill: tt.bill}, agg)
			w := serve(r, "/api/v1/bills/hr-119-1/aggregates/TX")
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.body) {
				t.Errorf("got %d %s, want %d containing %s", w.Code, w.Body, tt.status, tt.body)
			}
			if strings.Contains(w.Body.String(), "basis") || strings.Contains(w.Body.String(), `"yea_pct"`) {
				t.Errorf("a cell that isn't served leaked: %s", w.Body)
			}
		})
	}
}

func TestGetMemberAlignment(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rows := []model.RepAlignment{{
		MemberID: "P000197", Congress: 119, ScopeKey: "CA-11", BillsCompared: 22, BillsAgreed: 14, ComputedAt: at,
	}}
	tests := []struct {
		name, id string
		rows     []model.RepAlignment
		status   int
		body     string
	}{
		{
			"rows", "P000197", rows, http.StatusOK,
			`{"member_id":"P000197","alignment":[{"member_id":"P000197","congress":119,"scope_key":"CA-11",` +
				`"bills_compared":22,"bills_agreed":14,"computed_at":"2026-10-01T12:00:00Z"}]}`,
		},
		{"no rows yet", "P000197", nil, http.StatusOK, `{"member_id":"P000197","alignment":[]}`},
		{"no such member", "A000001", nil, http.StatusNotFound, `{"error":"member not found"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, r := newAggregatesRouter(&mockBillRepo{}, &mockAggregates{alignment: tt.rows})
			w := serve(r, "/api/v1/members/"+tt.id+"/alignment")
			if w.Code != tt.status || strings.TrimSpace(w.Body.String()) != tt.body {
				t.Errorf("got %d %s, want %d %s", w.Code, w.Body, tt.status, tt.body)
			}
			if tt.status == http.StatusOK && w.Header().Get("Cache-Control") != aggregatesCacheControl {
				t.Errorf("Cache-Control = %q", w.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestAggregateRoutesCacheInRedis(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping Redis integration test")
	}
	c, err := cache.New(redisURL)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Congress 1 has no bills anywhere real, so these keys are the test's own.
	keys := []string{"aggregates:bill:hr-1-1", "aggregates:scope:hr-1-1:CA-12", "aggregates:alignment:P000197"}
	for _, k := range keys {
		_ = c.Delete(t.Context(), k)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			_ = c.Delete(context.Background(), k)
		}
	})

	cells := testCells(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	agg := &mockAggregates{cells: cells, cell: &cells[3], alignment: []model.RepAlignment{{MemberID: "P000197"}}}
	h, r := newAggregatesRouter(&mockBillRepo{}, agg)
	h.SetCache(c)
	for _, path := range []string{
		"/api/v1/bills/hr-1-1/aggregates", "/api/v1/bills/hr-1-1/aggregates/CA-12",
		"/api/v1/members/P000197/alignment",
	} {
		before := agg.calls
		first := serve(r, path)
		second := serve(r, path)
		if agg.calls != before+1 {
			t.Errorf("%s: repo calls = %d, want 1 (second request from cache)", path, agg.calls-before)
		}
		if second.Code != http.StatusOK || strings.TrimSpace(first.Body.String()) != second.Body.String() {
			t.Errorf("%s: cached response %d %q differs from %q", path, second.Code, second.Body, first.Body)
		}
		if got := second.Header().Get("Cache-Control"); got != aggregatesCacheControl {
			t.Errorf("%s: Cache-Control on a cache hit = %q", path, got)
		}
		assertNoInternals(t, second.Body.String())
	}
}
