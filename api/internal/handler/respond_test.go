package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

var errSpanner = errors.New("spanner: unavailable")

// recordingBillRepo records the params List was called with and can fail
// List or one GetBill section.
type recordingBillRepo struct {
	mockBillRepo

	listCalls   int
	params      model.ListParams
	listErr     error
	actionsErr  error
	billDetails *model.Bill
}

func (m *recordingBillRepo) List(_ context.Context, p model.ListParams) (*model.ListResult[model.Bill], error) {
	m.listCalls++
	m.params = p
	if m.listErr != nil {
		return nil, m.listErr
	}
	return &model.ListResult[model.Bill]{Items: []model.Bill{}}, nil
}

func (m *recordingBillRepo) GetByID(_ context.Context, _ string) (*model.Bill, error) {
	return m.billDetails, nil
}

func (m *recordingBillRepo) GetActions(_ context.Context, _ string) ([]model.BillAction, error) {
	if m.actionsErr != nil {
		return nil, m.actionsErr
	}
	return []model.BillAction{}, nil
}

// newLoggedRouter routes the bill routes through chi with request IDs, as
// the server does, and captures the handler's JSON logs in buf.
func newLoggedRouter(bills *recordingBillRepo, buf *bytes.Buffer) *chi.Mux {
	h := newTestHandler(bills)
	h.SetLogger(slog.New(slog.NewJSONHandler(buf, nil)))

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Get("/api/v1/bills", h.ListBills)
	r.Get("/api/v1/bills/{id}", h.GetBill)
	r.Get("/api/v1/members", h.ListMembers)
	return r
}

// logLines decodes each JSON log line in buf.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line isn't JSON: %q", line)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestServerError_LogsRouteAndCause(t *testing.T) {
	var buf bytes.Buffer
	r := newLoggedRouter(&recordingBillRepo{listErr: errSpanner}, &buf)

	w := serve(r, "/api/v1/bills?congress=119")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"error":"failed to list bills"}` {
		t.Errorf("body = %s", got)
	}
	lines := logLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("want exactly one log line, got %d: %v", len(lines), lines)
	}
	l := lines[0]
	want := map[string]any{
		"level": "ERROR",
		"route": "/api/v1/bills",
		"op":    "failed to list bills",
		"error": errSpanner.Error(),
	}
	for k, v := range want {
		if l[k] != v {
			t.Errorf("log %s = %v, want %v", k, l[k], v)
		}
	}
	if id, _ := l["request_id"].(string); id == "" {
		t.Errorf("log has no request_id: %v", l)
	}
}

func TestServerError_RouteIsPatternNotPath(t *testing.T) {
	var buf bytes.Buffer
	bills := &recordingBillRepo{}
	h := newTestHandler(bills)
	h.SetLogger(slog.New(slog.NewJSONHandler(&buf, nil)))
	h.Users = &failingUserRepo{}

	r := chi.NewRouter()
	r.Post("/api/v1/bills/{id}/vote", h.CastVote)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/bills/hr-119-42/vote",
		strings.NewReader(`{"vote":"yea"}`)))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	lines := logLines(t, &buf)
	if len(lines) != 1 || lines[0]["route"] != "/api/v1/bills/{id}/vote" {
		t.Fatalf("want one log line with the route pattern, got %v", lines)
	}
	if strings.Contains(buf.String(), "hr-119-42") {
		t.Errorf("log leaks the raw path: %s", buf.String())
	}
}

type failingUserRepo struct{ mockUserRepo }

func (*failingUserRepo) CastVote(_ context.Context, _, _, _ string, _ model.VoteChecks) error {
	return errSpanner
}

const (
	searchLongErr   = "search too long; use at most 8 words and 100 characters"
	searchOffsetErr = "offset too large for a search; narrow the search"
)

const policyAreaErr = "policy_area must be a policy area name, as GET /policy-areas lists them"

func TestListParams(t *testing.T) {
	tests := []struct {
		query      string
		wantStatus int
		wantErr    string
		wantOffset int
		wantLimit  int
	}{
		{"", http.StatusOK, "", 0, 20},
		{"offset=40&limit=10", http.StatusOK, "", 40, 10},
		{"offset=10000", http.StatusOK, "", 10000, 20},
		{"limit=0", http.StatusOK, "", 0, 20},
		{"limit=-5", http.StatusOK, "", 0, 20},
		{"limit=500", http.StatusOK, "", 0, 100},
		{"offset=10001", http.StatusBadRequest, "offset too large; narrow the list with filters", 0, 0},
		{"offset=abc", http.StatusBadRequest, "offset must be a non-negative integer", 0, 0},
		{"offset=-1", http.StatusBadRequest, "offset must be a non-negative integer", 0, 0},
		{"offset=1.5", http.StatusBadRequest, "offset must be a non-negative integer", 0, 0},
		{"limit=abc", http.StatusBadRequest, "limit must be an integer", 0, 0},
		// A search is bounded in length, terms and depth (#619).
		{"q=tax&offset=500", http.StatusOK, "", 500, 20},
		{"q=tax&offset=501", http.StatusBadRequest, searchOffsetErr, 0, 0},
		{"search=tax&offset=10000", http.StatusBadRequest, searchOffsetErr, 0, 0},
		{"q=" + url.QueryEscape(strings.Repeat("a", 100)), http.StatusOK, "", 0, 20},
		{"q=" + url.QueryEscape(strings.Repeat("é", 100)), http.StatusOK, "", 0, 20},
		{"q=" + url.QueryEscape(strings.Repeat("a", 101)), http.StatusBadRequest, searchLongErr, 0, 0},
		{"q=" + url.QueryEscape(strings.Repeat("é", 101)), http.StatusBadRequest, searchLongErr, 0, 0},
		{"q=" + url.QueryEscape("one two three four five six seven eight"), http.StatusOK, "", 0, 20},
		{"q=" + url.QueryEscape("  one two\tthree four five six seven eight  "), http.StatusOK, "", 0, 20},
		{
			"q=" + url.QueryEscape("one two three four five six seven eight nine"),
			http.StatusBadRequest,
			searchLongErr,
			0,
			0,
		},
		{"q=%ff", http.StatusBadRequest, "search must be valid UTF-8 text", 0, 0},
		// policy_area is a name, bounded like a search's text (#708).
		{"policy_area=" + url.QueryEscape(strings.Repeat("é", 100)), http.StatusOK, "", 0, 20},
		{"policy_area=" + url.QueryEscape(strings.Repeat("a", 101)), http.StatusBadRequest, policyAreaErr, 0, 0},
		{"policy_area=%ff", http.StatusBadRequest, policyAreaErr, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			var buf bytes.Buffer
			bills := &recordingBillRepo{}
			r := newLoggedRouter(bills, &buf)

			w := serve(r, "/api/v1/bills?"+tt.query)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.wantStatus, w.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				checkBadRequest(t, w, tt.wantErr, bills, &buf)
				return
			}
			if bills.params.Offset != tt.wantOffset || bills.params.Limit != tt.wantLimit {
				t.Errorf("offset, limit = %d, %d, want %d, %d",
					bills.params.Offset, bills.params.Limit, tt.wantOffset, tt.wantLimit)
			}
		})
	}
}

const (
	statusUnknownErr = "unknown status; use the values of a bill's current_status"
	statusPastErr    = "status_mode=past takes one status"
)

// TestListParams_Statuses checks the status filter (#712): comma-separated or repeated, sorted and
// without duplicates; an unknown status, or past with several, is a 400.
func TestListParams_Statuses(t *testing.T) {
	tests := []struct {
		query    string
		wantErr  string
		want     []string
		wantMode string
	}{
		{"", "", nil, ""},
		{"status=signed", "", []string{"signed"}, ""},
		{"status=signed,became_law", "", []string{"became_law", "signed"}, ""},
		{"status=signed&status=became_law", "", []string{"became_law", "signed"}, ""},
		{"status=" + url.QueryEscape(" signed , became_law,,signed"), "", []string{"became_law", "signed"}, ""},
		{"status=signed&status=became_law,signed", "", []string{"became_law", "signed"}, ""},
		{"status=passed_house&status_mode=past", "", []string{"passed_house"}, "past"},
		{"status=signed,signed&status_mode=past", "", []string{"signed"}, "past"},
		{"status=,", "", nil, ""},
		{"status_mode=past", "", nil, ""},
		{"status=passed", statusUnknownErr, nil, ""},
		{"status=signed,Became_Law", statusUnknownErr, nil, ""},
		{"status=signed&status=nope", statusUnknownErr, nil, ""},
		{"status=signed,became_law&status_mode=past", statusPastErr, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			var buf bytes.Buffer
			bills := &recordingBillRepo{}
			w := serve(newLoggedRouter(bills, &buf), "/api/v1/bills?"+tt.query)

			if tt.wantErr != "" {
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
				}
				checkBadRequest(t, w, tt.wantErr, bills, &buf)
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
			}
			if !slices.Equal(bills.params.Statuses, tt.want) || bills.params.StatusMode != tt.wantMode {
				t.Errorf("statuses, mode = %q, %q, want %q, %q",
					bills.params.Statuses, bills.params.StatusMode, tt.want, tt.wantMode)
			}
		})
	}
}

// checkBadRequest checks a 400's message, that the repository wasn't called
// and that nothing was logged.
func checkBadRequest(
	t *testing.T, w *httptest.ResponseRecorder, wantErr string, bills *recordingBillRepo, buf *bytes.Buffer,
) {
	t.Helper()
	var body map[string]string
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["error"] != wantErr {
		t.Errorf("error = %q, want %q", body["error"], wantErr)
	}
	if bills.listCalls != 0 {
		t.Error("repository was called for a bad request")
	}
	if buf.Len() != 0 {
		t.Errorf("a 4xx must not be logged, got %s", buf.String())
	}
}

func TestListMembers_BadOffset(t *testing.T) {
	var buf bytes.Buffer
	r := newLoggedRouter(&recordingBillRepo{}, &buf)

	if w := serve(r, "/api/v1/members?offset=99999"); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestMyLists_BadOffset(t *testing.T) {
	h := newTestHandler(&mockBillRepo{})
	for name, fn := range map[string]http.HandlerFunc{"votes": h.GetMyVotes, "favorites": h.GetMyFavorites} {
		w := httptest.NewRecorder()
		fn(w, httptest.NewRequest(http.MethodGet, "/api/v1/me/"+name+"?offset=x", nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
}

func TestGetBill_PartialSectionLogsError(t *testing.T) {
	var buf bytes.Buffer
	bills := &recordingBillRepo{
		billDetails: &model.Bill{ID: "hr-119-1"},
		actionsErr:  errSpanner,
	}
	r := newLoggedRouter(bills, &buf)

	w := serve(r, "/api/v1/bills/hr-119-1")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (partial responses are kept)", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if v, ok := body["actions"]; !ok || v != nil {
		t.Errorf("actions = %v, want null", v)
	}
	lines := logLines(t, &buf)
	if len(lines) != 1 || lines[0]["level"] != "ERROR" || lines[0]["msg"] != "failed to get bill section" ||
		lines[0]["section"] != "actions" {
		t.Errorf("want one error log for the failed section, got %v", lines)
	}
}

// TestListBills_InvalidSearchIs400 covers #452: a search the database can't read is the client's
// error, answered 400 and not logged as a failure, and so is a search that isn't UTF-8.
func TestListBills_InvalidSearchIs400(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		listErr error
		calls   int
	}{
		{"rejected by the database", "q=H.R.+1",
			fmt.Errorf("count bills: %w: %w", repository.ErrInvalidSearch, errSpanner), 1},
		{"not UTF-8", "q=%FF", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			bills := &recordingBillRepo{listErr: tt.listErr}
			w := serve(newLoggedRouter(bills, &buf), "/api/v1/bills?"+tt.query)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", w.Code, w.Body)
			}
			if bills.listCalls != tt.calls {
				t.Errorf("List calls = %d, want %d", bills.listCalls, tt.calls)
			}
			if buf.Len() != 0 {
				t.Errorf("a 400 logged: %s", buf.String())
			}
		})
	}
}

// unreachableCache is a cache whose Redis refuses connections, so its first
// command fails with a real error rather than cache.ErrUnavailable.
func unreachableCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.New("redis://127.0.0.1:1")
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// assertCacheWarning checks that buf holds exactly one record, the warning
// msg, naming the key's prefix and an error and never the visitor's text.
func assertCacheWarning(t *testing.T, buf *bytes.Buffer, msg, private string) {
	t.Helper()
	if strings.Contains(buf.String(), private) {
		t.Errorf("logs contain the visitor's text %q: %s", private, buf.String())
	}
	lines := logLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("want exactly one log line, got %d: %v", len(lines), lines)
	}
	l := lines[0]
	if l["msg"] != msg || l["level"] != "WARN" {
		t.Errorf("log = %v, want a WARN %q", l, msg)
	}
	if l["key_prefix"] != "bills:list" {
		t.Errorf("log key_prefix = %v, want bills:list", l["key_prefix"])
	}
	if e, _ := l["error"].(string); e == "" {
		t.Errorf("log has no error: %v", l)
	}
}

func TestCacheGetError_LogsKeyPrefixNotSearch(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&recordingBillRepo{})
	h.SetLogger(slog.New(slog.NewJSONHandler(&buf, nil)))
	h.SetCache(unreachableCache(t))

	r := chi.NewRouter()
	r.Get("/api/v1/bills", h.ListBills)
	w := serve(r, "/api/v1/bills?q=my+private%3Asearch")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// The set after the failed get hits the back-off, which isn't logged.
	assertCacheWarning(t, &buf, "cache get error", "private")
}

func TestCacheWriteErrors_LogKeyPrefixNotSearch(t *testing.T) {
	search := "my private:search"
	key := handler.ListCacheKey("bills:list", model.ListParams{Limit: 20, Search: &search})

	tests := []struct {
		name  string
		msg   string
		write func(h *handler.Handler)
	}{
		{"encode", "cache encode error", func(h *handler.Handler) {
			h.CacheJSON(t.Context(), key, make(chan int), time.Minute)
		}},
		{"set", "cache set error", func(h *handler.Handler) {
			h.CacheSet(t.Context(), key, "{}", time.Minute)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := newTestHandler(&recordingBillRepo{})
			h.SetLogger(slog.New(slog.NewJSONHandler(&buf, nil)))
			h.SetCache(unreachableCache(t))

			tt.write(h)

			assertCacheWarning(t, &buf, tt.msg, "private")
		})
	}
}
