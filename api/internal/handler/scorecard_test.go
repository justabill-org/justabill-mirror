package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/scoring"
)

// fakeScorecard returns fixed results and records the user, member and congresses it was
// asked about.
type fakeScorecard struct {
	scores      []model.RepScore
	comparisons []model.VoteComparison
	err         error
	calls       int
	userID      string
	memberID    string
	congresses  []int
}

func (f *fakeScorecard) GetScorecard(_ context.Context, userID string, congresses []int) ([]model.RepScore, error) {
	f.calls++
	f.userID, f.congresses = userID, congresses
	return f.scores, f.err
}

func (f *fakeScorecard) CompareWithMember(
	_ context.Context, userID, memberID string, congresses []int,
) ([]model.VoteComparison, error) {
	f.calls++
	f.userID, f.memberID, f.congresses = userID, memberID, congresses
	return f.comparisons, f.err
}

// loadedCongresses has the 118th and the current 119th.
func loadedCongresses() *listCongressRepo {
	return &listCongressRepo{congresses: []model.Congress{{Number: 119, IsCurrent: true}, {Number: 118}}}
}

// scorecardUser is the signed-in user of scorecardRouter's requests.
const scorecardUser = "user-1"

func scorecardRouter(sc *fakeScorecard) http.Handler {
	return scorecardRouterWith(sc, loadedCongresses())
}

func scorecardRouterWith(sc *fakeScorecard, congresses *listCongressRepo) http.Handler {
	h := newTestHandler(&mockBillRepo{})
	h.Scorecard = sc
	h.Congresses = congresses
	h.SetLogger(slog.New(slog.DiscardHandler))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(mw.ContextWithUserID(req.Context(), scorecardUser)))
		})
	})
	r.Get("/me/scorecard", h.GetScorecard)
	r.Get("/me/compare/{memberID}", h.CompareMember)
	return r
}

func getJSON(t *testing.T, h http.Handler, target string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s: body %q: %v", target, rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestGetScorecard_NullPercentage(t *testing.T) {
	pct := 50.0
	sc := &fakeScorecard{scores: []model.RepScore{
		{MemberID: "A000001", MemberName: "Ada Alvarez", Chamber: "House", Party: "D",
			MatchingVotes: 1, TotalCompared: 2, AlignmentPct: &pct, Rule: scoring.RuleName},
		{MemberID: "C000003", MemberName: "Cora Chen", Chamber: "Senate", Party: "R",
			MemberAbsent: 1, Rule: scoring.RuleName},
	}}
	code, body := getJSON(t, scorecardRouter(sc), "/me/scorecard")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if sc.userID != "user-1" {
		t.Errorf("scored user %q, want user-1", sc.userID)
	}
	scores, _ := body["scores"].([]any)
	if len(scores) != 2 {
		t.Fatalf("scores = %v, want 2", body["scores"])
	}
	house, _ := scores[0].(map[string]any)
	senate, _ := scores[1].(map[string]any)
	if house["alignment_pct"] != 50.0 || house["rule"] != scoring.RuleName {
		t.Errorf("house score = %v", house)
	}
	pctVal, present := senate["alignment_pct"]
	if !present || pctVal != nil || senate["member_absent"] != 1.0 || senate["total_compared"] != 0.0 {
		t.Errorf("senate score = %v, want alignment_pct null and member_absent 1", senate)
	}
}

func TestCompareMember_Fields(t *testing.T) {
	q := "On Passage"
	sc := &fakeScorecard{comparisons: []model.VoteComparison{{
		BillID: "hr-119-1", BillTitle: "Companion Act", VoteID: "house-119-s1-roll001", Chamber: "House",
		Congress: 119, VoteDate: time.Date(2025, time.March, 4, 17, 0, 0, 0, time.UTC), Question: &q,
		UserVote: scoring.Yea, MemberVote: scoring.NotVoting,
	}}}
	code, body := getJSON(t, scorecardRouter(sc), "/me/compare/A000001")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if sc.userID != "user-1" || sc.memberID != "A000001" || body["member_id"] != "A000001" {
		t.Errorf("compared user %q with %q, body member_id %v", sc.userID, sc.memberID, body["member_id"])
	}
	rows, _ := body["comparisons"].([]any)
	if len(rows) != 1 {
		t.Fatalf("comparisons = %v, want 1", body["comparisons"])
	}
	row, _ := rows[0].(map[string]any)
	want := map[string]any{
		"bill_id": "hr-119-1", "vote_id": "house-119-s1-roll001", "chamber": "House", "congress": 119.0,
		"vote_date": "2025-03-04T17:00:00Z",
		"question":  "On Passage", "user_vote": "yea", "member_vote": "not_voting",
		"counted": false, "matches": false,
	}
	for k, v := range want {
		if row[k] != v {
			t.Errorf("comparison[%s] = %v, want %v", k, row[k], v)
		}
	}
}

func TestScorecard_Errors(t *testing.T) {
	sc := &fakeScorecard{err: errors.New("spanner down")}
	h := scorecardRouter(sc)
	for _, target := range []string{"/me/scorecard", "/me/compare/A000001"} {
		if code, _ := getJSON(t, h, target); code != http.StatusInternalServerError {
			t.Errorf("GET %s = %d, want 500", target, code)
		}
	}
}

func TestScorecard_CongressFilter(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []int
	}{
		{"no filter is every congress", "", nil},
		{"one congress", "?congress=118", []int{118}},
		{"repeated, sorted and deduplicated", "?congress=119&congress=118&congress=0119", []int{118, 119}},
	}
	for _, tt := range tests {
		for _, path := range []string{"/me/scorecard", "/me/compare/A000001"} {
			t.Run(tt.name+" "+path, func(t *testing.T) {
				sc := &fakeScorecard{}
				if code, body := getJSON(t, scorecardRouter(sc), path+tt.query); code != http.StatusOK {
					t.Fatalf("status = %d, want 200: %v", code, body)
				}
				if !slices.Equal(sc.congresses, tt.want) || (tt.want == nil) != (sc.congresses == nil) {
					t.Errorf("congresses = %#v, want %#v", sc.congresses, tt.want)
				}
			})
		}
	}
}

func TestScorecard_BadCongressFilter(t *testing.T) {
	tests := []struct {
		name  string
		query string
		repo  *listCongressRepo
		want  int
	}{
		{"not a number", "?congress=abc", loadedCongresses(), http.StatusBadRequest},
		{"empty value", "?congress=", loadedCongresses(), http.StatusBadRequest},
		{"out of range", "?congress=201", loadedCongresses(), http.StatusBadRequest},
		{"not loaded", "?congress=117", loadedCongresses(), http.StatusBadRequest},
		{"one of two not loaded", "?congress=119&congress=120", loadedCongresses(), http.StatusBadRequest},
		{"congresses unreadable", "?congress=118", &listCongressRepo{err: errors.New("spanner down")},
			http.StatusInternalServerError},
	}
	for _, tt := range tests {
		for _, path := range []string{"/me/scorecard", "/me/compare/A000001"} {
			t.Run(tt.name+" "+path, func(t *testing.T) {
				sc := &fakeScorecard{}
				code, body := getJSON(t, scorecardRouterWith(sc, tt.repo), path+tt.query)
				if code != tt.want {
					t.Fatalf("status = %d, want %d: %v", code, tt.want, body)
				}
				if sc.calls != 0 {
					t.Errorf("scorecard called %d times for a bad filter", sc.calls)
				}
			})
		}
	}
}
