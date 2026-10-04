package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/district"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

// --- District lookup mock ---

// lookupAnswer is a mockDistrictLookup's answer for one congress.
type lookupAnswer struct {
	results []district.Result
	err     error
	block   bool // wait until the request is cancelled, then return its error
}

// mockDistrictLookup answers results and err for every congress, except those in byCongress,
// whether asked for an address or a point. FindReps calls it from two goroutines, so it records
// calls under a lock.
type mockDistrictLookup struct {
	results    []district.Result
	err        error
	byCongress map[int]lookupAnswer

	mu     sync.Mutex
	calls  []int
	points []point // the points FromCoordinates was asked for, in call order
}

// point is a latitude and longitude passed to FromCoordinates.
type point struct{ lat, lon float64 }

func (m *mockDistrictLookup) FromAddress(ctx context.Context, _ string, congress int) ([]district.Result, error) {
	m.mu.Lock()
	m.calls = append(m.calls, congress)
	m.mu.Unlock()
	return m.answer(ctx, congress)
}

func (m *mockDistrictLookup) FromCoordinates(
	ctx context.Context, lat, lon float64, congress int,
) ([]district.Result, error) {
	m.mu.Lock()
	m.calls = append(m.calls, congress)
	m.points = append(m.points, point{lat, lon})
	m.mu.Unlock()
	return m.answer(ctx, congress)
}

// answer is the mock's reply for a congress.
func (m *mockDistrictLookup) answer(ctx context.Context, congress int) ([]district.Result, error) {
	a, ok := m.byCongress[congress]
	if !ok {
		return m.results, m.err
	}
	if a.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return a.results, a.err
}

// pointsAsked lists the points looked up.
func (m *mockDistrictLookup) pointsAsked() []point {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.points)
}

// congresses lists the congresses looked up, sorted.
func (m *mockDistrictLookup) congresses() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Sorted(slices.Values(m.calls))
}

// --- Member repo mock with configurable returns ---

type mockMemberRepoForReps struct {
	districtMembers map[string][]model.Member // key: "STATE-DISTRICT"
	senators        map[string][]model.Member // key: state
}

func (m *mockMemberRepoForReps) List(_ context.Context, _ model.ListParams) (*model.ListResult[model.Member], error) {
	return &model.ListResult[model.Member]{Items: []model.Member{}}, nil
}
func (m *mockMemberRepoForReps) GetByID(_ context.Context, _ string) (*model.MemberDetail, error) {
	return nil, nil //nolint:nilnil // mock
}
func (m *mockMemberRepoForReps) GetByDistrict(_ context.Context, state string, dist int) ([]model.Member, error) {
	key := fmt.Sprintf("%s-%d", state, dist)
	return m.districtMembers[key], nil
}
func (m *mockMemberRepoForReps) GetSenators(_ context.Context, state string) ([]model.Member, error) {
	return m.senators[state], nil
}
func (m *mockMemberRepoForReps) GetRecentVotes(
	_ context.Context, _ string, _ int,
) ([]model.MemberVoteSummary, error) {
	return []model.MemberVoteSummary{}, nil
}

func newRepsHandler(dl *mockDistrictLookup, mr *mockMemberRepoForReps) *handler.Handler {
	h := &handler.Handler{
		Bills:      &mockBillRepo{},
		Members:    mr,
		Users:      &mockUserRepo{},
		Votes:      &mockVoteRepo{},
		Congresses: &listCongressRepo{congresses: []model.Congress{{Number: 120}, {Number: 119, IsCurrent: true}}},
		Scorecard:  &mockScorecard{},
		District:   dl,
	}
	h.SetLogger(slog.New(slog.DiscardHandler))
	return h
}

// repsPost is a POST /reps request for an address.
func repsPost(t *testing.T, address string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]string{"address": address})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, "/api/v1/reps", bytes.NewReader(body))
}

// findReps calls FindReps for an address and decodes the JSON body.
func findReps(t *testing.T, h *handler.Handler, address string) (int, repsBody) {
	t.Helper()
	w := httptest.NewRecorder()
	h.FindReps(w, repsPost(t, address))

	var body repsBody
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
	}
	return w.Code, body
}

// timelessJSONLogger logs JSON to w without the time, so a test that searches the log for parts
// of an address can't match the clock (a timestamp such as 20:28:17.296910193Z holds a ZIP).
func timelessJSONLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

// A record whose time contains an address's ZIP leaves no trace of it in a timeless log.
func TestTimelessJSONLogger(t *testing.T) {
	var logs bytes.Buffer
	logger := timelessJSONLogger(&logs)
	rec := slog.NewRecord(time.Date(2026, 10, 2, 20, 28, 17, 296910193, time.UTC), slog.LevelInfo, "reps found", 0)
	rec.AddAttrs(slog.String("district_source", "state"))
	if err := logger.Handler().Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if strings.Contains(out, "96910") || strings.Contains(out, `"time"`) {
		t.Errorf("log carries the time: %s", out)
	}
	if !strings.Contains(out, `"msg":"reps found","district_source":"state"`) {
		t.Errorf("log lost the message or an attribute: %s", out)
	}
}

type repsBody struct {
	Reps      []model.Member `json:"reps"`
	Senators  []model.Member `json:"senators"`
	Districts []struct {
		State    string `json:"state"`
		District int    `json:"district"`
		AtLarge  bool   `json:"at_large"`
		Congress int    `json:"congress"`
		Source   string `json:"source"`
	} `json:"districts"`
}

// A missing, blank or overlong address, or a body that isn't JSON, is a 400 that never reaches
// the geocoder.
func TestFindReps_BadAddress(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"no body", ""},
		{"not JSON", "address=1100+Congress+Ave"},
		{"no address", `{}`},
		{"empty", `{"address":""}`},
		{"blank", `{"address":"   "}`},
		{"not a string", `{"address":1100}`},
		{"201 characters", `{"address":"` + strings.Repeat("a", 201) + `"}`},
		{"201 characters, multibyte", `{"address":"` + strings.Repeat("å", 201) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dl := &mockDistrictLookup{}
			h := newRepsHandler(dl, &mockMemberRepoForReps{})
			w := httptest.NewRecorder()
			h.FindReps(w, httptest.NewRequest(http.MethodPost, "/api/v1/reps", strings.NewReader(tt.body)))
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			if got := dl.congresses(); len(got) != 0 {
				t.Errorf("looked up congresses %v for a bad address", got)
			}
		})
	}
}

// 200 characters is the longest address accepted, counted in characters, not bytes.
func TestFindReps_LongestAddress(t *testing.T) {
	for _, address := range []string{strings.Repeat("a", 200), strings.Repeat("å", 200)} {
		dl := &mockDistrictLookup{results: []district.Result{}}
		h := newRepsHandler(dl, &mockMemberRepoForReps{})
		if code, _ := findReps(t, h, address); code != http.StatusOK {
			t.Errorf("status = %d for %d bytes, want 200", code, len(address))
		}
	}
}

// The response never echoes the address back.
func TestFindReps_NoAddressInResponse(t *testing.T) {
	h := newRepsHandler(
		&mockDistrictLookup{results: []district.Result{{State: "TX", District: 37}}},
		&mockMemberRepoForReps{},
	)
	w := httptest.NewRecorder()
	h.FindReps(w, repsPost(t, austin))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["address"]; ok {
		t.Errorf("response has an address field: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "Congress Ave") {
		t.Errorf("response contains the address: %s", w.Body)
	}
}

func TestFindReps_DistrictLookupError(t *testing.T) {
	h := newRepsHandler(
		&mockDistrictLookup{err: errors.New("census API down")},
		&mockMemberRepoForReps{},
	)

	req := repsPost(t, "100 Main St 75201")
	w := httptest.NewRecorder()
	h.FindReps(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", w.Code)
	}
}

func TestFindReps_NoDistrictsFound(t *testing.T) {
	h := newRepsHandler(
		&mockDistrictLookup{results: []district.Result{}},
		&mockMemberRepoForReps{},
	)

	req := repsPost(t, "00000")
	w := httptest.NewRecorder()
	h.FindReps(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	reps, ok := body["reps"].([]any)
	if !ok {
		t.Fatal("expected 'reps' array")
	}
	if len(reps) != 0 {
		t.Errorf("expected 0 reps, got %d", len(reps))
	}

	senators, ok := body["senators"].([]any)
	if !ok {
		t.Fatal("expected 'senators' array")
	}
	if len(senators) != 0 {
		t.Errorf("expected 0 senators, got %d", len(senators))
	}
}

func TestFindReps_Success(t *testing.T) {
	h := newRepsHandler(
		&mockDistrictLookup{
			results: []district.Result{
				{State: "TX", District: 7},
			},
		},
		&mockMemberRepoForReps{
			districtMembers: map[string][]model.Member{
				"TX-7": {{BioguideID: "R000123", FirstName: "John", LastName: "Rep"}},
			},
			senators: map[string][]model.Member{
				"TX": {
					{BioguideID: "S000456", FirstName: "Jane", LastName: "Senator"},
					{BioguideID: "S000789", FirstName: "Bob", LastName: "Senator"},
				},
			},
		},
	)

	req := repsPost(t, "100 Main St Dallas TX 75201")
	w := httptest.NewRecorder()
	h.FindReps(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	reps, ok := body["reps"].([]any)
	if !ok {
		t.Fatal("expected 'reps' array")
	}
	if len(reps) != 1 {
		t.Errorf("expected 1 rep, got %d", len(reps))
	}

	senators, ok := body["senators"].([]any)
	if !ok {
		t.Fatal("expected 'senators' array")
	}
	if len(senators) != 2 {
		t.Errorf("expected 2 senators, got %d", len(senators))
	}

	districts, ok := body["districts"].([]any)
	if !ok {
		t.Fatal("expected 'districts' array")
	}
	if len(districts) != 1 {
		t.Errorf("expected 1 district, got %d", len(districts))
	}
}

func TestFindReps_AtLargeAndNonVotingSeats(t *testing.T) {
	members := &mockMemberRepoForReps{
		districtMembers: map[string][]model.Member{
			"WY-0": {{BioguideID: "H001096", LastName: "Hageman"}},
			"DC-0": {{BioguideID: "N000147", LastName: "Norton"}},
		},
		senators: map[string][]model.Member{
			"WY": {{BioguideID: "B001261", LastName: "Barrasso"}, {BioguideID: "L000571", LastName: "Lummis"}},
		},
	}
	tests := []struct {
		state        string
		wantRep      string
		wantSenators int
	}{
		{"WY", "H001096", 2},
		{"DC", "N000147", 0},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			dl := &mockDistrictLookup{results: []district.Result{
				{State: tt.state, District: 0, AtLarge: true, Source: district.SourceGeocoder},
			}}
			h := newRepsHandler(dl, members)

			code, body := findReps(t, h, "an address in "+tt.state)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if got := dl.congresses(); !slices.Contains(got, 119) {
				t.Errorf("looked up congresses %v, want the current congress 119", got)
			}
			checkSingleSeat(t, body, tt.state, tt.wantRep, tt.wantSenators)
		})
	}
}

// checkSingleSeat asserts a FindReps body for a single-seat state or territory in the 119th.
func checkSingleSeat(t *testing.T, body repsBody, state, wantRep string, wantSenators int) {
	t.Helper()
	if len(body.Reps) != 1 || body.Reps[0].BioguideID != wantRep {
		t.Errorf("reps = %+v, want %s", body.Reps, wantRep)
	}
	if body.Senators == nil || len(body.Senators) != wantSenators {
		t.Errorf("senators = %+v, want %d (and never null)", body.Senators, wantSenators)
	}
	if len(body.Districts) != 1 {
		t.Fatalf("districts = %+v, want 1", body.Districts)
	}
	d := body.Districts[0]
	if d.State != state || d.District != 0 || !d.AtLarge || d.Congress != 119 || d.Source != "geocoder" {
		t.Errorf("district = %+v", d)
	}
}

func TestFindReps_NoCurrentCongress(t *testing.T) {
	dl := &mockDistrictLookup{results: []district.Result{{State: "TX", District: 37}}}
	h := newRepsHandler(dl, &mockMemberRepoForReps{})
	h.Congresses = &listCongressRepo{congresses: []model.Congress{{Number: 119}, {Number: 120}}}
	var logs bytes.Buffer
	h.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))

	if code, _ := findReps(t, h, "1100 Congress Ave, Austin, TX"); code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
	if got := dl.congresses(); len(got) != 0 {
		t.Errorf("looked up congresses %v with no current congress", got)
	}
	if !strings.Contains(logs.String(), `"level":"ERROR","msg":"no current congress"`) {
		t.Errorf("want an ERROR log for no current congress, got %s", logs.String())
	}
}

func TestFindReps_CongressListError(t *testing.T) {
	h := newRepsHandler(&mockDistrictLookup{}, &mockMemberRepoForReps{})
	h.Congresses = &listCongressRepo{err: errors.New("spanner down")}

	if code, _ := findReps(t, h, "1100 Congress Ave, Austin, TX"); code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", code)
	}
}

func TestFindReps_LookupErrorLogsCongressNotAddress(t *testing.T) {
	h := newRepsHandler(
		&mockDistrictLookup{err: fmt.Errorf("wrapped: %w", district.ErrUnexpectedLayer)},
		&mockMemberRepoForReps{},
	)
	var logs bytes.Buffer
	h.SetLogger(timelessJSONLogger(&logs))

	if code, _ := findReps(t, h, "1100 Congress Ave, Austin, TX"); code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", code)
	}
	out := logs.String()
	if !strings.Contains(out, `"level":"ERROR","msg":"district lookup failed","congress":119`) {
		t.Errorf("want an ERROR log with the congress, got %s", out)
	}
	if strings.Contains(out, "Congress Ave") {
		t.Errorf("log contains the address: %s", out)
	}
}

// An Island Area address the geocoder can't match finds its delegate through the single-seat
// fallback, and no log line carries any part of the address.
func TestFindReps_SingleSeatFallbackLogsNoAddress(t *testing.T) {
	geocoder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"addressMatches":[]}}`))
	}))
	t.Cleanup(geocoder.Close)
	members := &mockMemberRepoForReps{
		districtMembers: map[string][]model.Member{"GU-0": {{BioguideID: "M001219", LastName: "Moylan"}}},
	}
	h := newRepsHandler(&mockDistrictLookup{}, members)
	h.District = district.NewCensusLookupWithURL(geocoder.Client(), geocoder.URL)
	var logs bytes.Buffer
	h.SetLogger(timelessJSONLogger(&logs))

	code, body := findReps(t, h, "1 Marine Corps Dr, Hagåtña, GU 96910")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(body.Reps) != 1 || body.Reps[0].BioguideID != "M001219" {
		t.Errorf("reps = %+v, want Guam's delegate", body.Reps)
	}
	if len(body.Districts) != 1 {
		t.Fatalf("districts = %+v, want 1", body.Districts)
	}
	if d := body.Districts[0]; d.State != "GU" || d.District != 0 || !d.AtLarge || d.Source != "state" {
		t.Errorf("district = %+v, want GU-0 at large from the state", d)
	}
	out := logs.String()
	if !strings.Contains(out, `"district_source":"state"`) {
		t.Errorf("want the source in the log, got %s", out)
	}
	for _, part := range []string{"Marine", "Hag", "96910"} {
		if strings.Contains(out, part) {
			t.Errorf("log contains %q from the address: %s", part, out)
		}
	}
}

// New geocodes with the Census Bureau unless WithDistrict sets a lookup, such as
// one pointed at CENSUS_GEOCODER_URL.
func TestNewDistrictLookup(t *testing.T) {
	if _, ok := handler.New(nil).District.(*district.CensusLookup); !ok {
		t.Errorf("default District = %T, want *district.CensusLookup", handler.New(nil).District)
	}
	dl := &mockDistrictLookup{}
	if got := handler.New(nil, handler.WithDistrict(dl)).District; got != dl {
		t.Errorf("WithDistrict: District = %v, want the given lookup", got)
	}
}
