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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/api/internal/auth/authtest"
	"github.com/justabill-org/justabill/api/internal/handler"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/repository"
)

// These tests cover the write-side controls of docs/design/89-aggregate-analytics.md (#121).

func testRules() handler.WriteRules {
	return handler.WriteRules{DailyVoteCap: 500, DistrictChangeInterval: 30 * 24 * time.Hour}
}

// goodAppCheck accepts only the token "good".
type goodAppCheck struct{}

func (goodAppCheck) VerifyAppCheck(_ context.Context, token string) error {
	if token == "good" {
		return nil
	}
	return auth.ErrInvalidAppCheckToken
}

// serveWrite sends a signed-in request for user-1 through App Check in audit
// mode (skipped when appCheck is empty) to fn, mounted at route.
func serveWrite(route string, fn http.HandlerFunc, method, target, body, appCheck string,
) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	if appCheck != "" {
		r.Use(mw.AppCheck(goodAppCheck{}, auth.AppCheckAudit, slog.New(slog.DiscardHandler)))
	}
	r.Method(method, route, fn)
	req := signedIn(method, target, body, "t", auth.Principal{UID: "uid-1"}, "user-1")
	if appCheck != "" {
		req.Header.Set(mw.AppCheckHeader, appCheck)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCastVotePassesChecks(t *testing.T) {
	for _, tt := range []struct {
		appCheck string
		want     string
	}{{"", "<nil>"}, {"good", "true"}, {"forged", "false"}} {
		users := newAccountUsers()
		h := newAccountHandler(users, authtest.New(), nil)
		handler.WithWriteRules(testRules())(h)

		w := serveWrite("/api/v1/bills/{id}/vote", h.CastVote, http.MethodPost,
			"/api/v1/bills/119-hr-1/vote", `{"vote":"yea"}`, tt.appCheck)
		if w.Code != http.StatusOK || len(users.voteChecks) != 1 {
			t.Fatalf(
				"app check %q: status %d, %d votes; want 200 and one vote",
				tt.appCheck,
				w.Code,
				len(users.voteChecks),
			)
		}
		got := users.voteChecks[0]
		if got.DailyCap != 500 || fmtBoolPtr(got.AppCheckOK) != tt.want {
			t.Errorf("app check %q: checks = cap %d, ok %s; want cap 500, ok %s",
				tt.appCheck, got.DailyCap, fmtBoolPtr(got.AppCheckOK), tt.want)
		}
	}
}

func fmtBoolPtr(b *bool) string {
	if b == nil {
		return "<nil>"
	}
	return strconv.FormatBool(*b)
}

func TestCastVoteOverDailyCap(t *testing.T) {
	var logs bytes.Buffer
	users := newAccountUsers()
	users.voteErr = fmt.Errorf("cast vote: %w", repository.ErrDailyVoteCap)
	h := newAccountHandler(users, authtest.New(), nil)
	h.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	handler.WithWriteRules(testRules())(h)

	w := serveWrite("/api/v1/bills/{id}/vote", h.CastVote, http.MethodPost,
		"/api/v1/bills/119-hr-1/vote", `{"vote":"nay"}`, "")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"code":"daily_vote_cap"`) ||
		!strings.Contains(w.Body.String(), "500 bills a day") {
		t.Errorf("over the cap: %d %s; want 429 daily_vote_cap naming the cap", w.Code, w.Body)
	}
	if !strings.Contains(logs.String(), `"user_id":"user-1"`) || strings.Contains(logs.String(), "nay") {
		t.Errorf("log = %s; want the user_id and never the vote", logs.String())
	}
}

func TestImportMyVotesRecordsAppCheck(t *testing.T) {
	users := newAccountUsers()
	h := newAccountHandler(users, authtest.New(), nil)
	w := serveWrite("/api/v1/me/votes:import", h.ImportMyVotes, http.MethodPost,
		"/api/v1/me/votes:import", `{"votes":[{"bill_id":"119-hr-1","vote":"yea"}]}`, "good")
	if w.Code != http.StatusOK || fmtBoolPtr(users.importAppCheck) != "true" {
		t.Errorf("import: %d, app check %s; want 200 and true", w.Code, fmtBoolPtr(users.importAppCheck))
	}
}

func TestUpdateMeDistrictChangeLimit(t *testing.T) {
	users := newAccountUsers()
	h := newAccountHandler(users, authtest.New(), nil)
	handler.WithWriteRules(testRules())(h)

	w := serveWrite("/api/v1/me", h.UpdateMe, http.MethodPatch, "/api/v1/me", `{"state":"CA","district":12}`, "")
	if w.Code != http.StatusOK || len(users.updates) != 1 ||
		users.updates[0].DistrictChangeInterval != 30*24*time.Hour {
		t.Fatalf("first change: %d, updates %+v; want 200 with the 30-day interval", w.Code, users.updates)
	}

	next := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	users.updateErr = fmt.Errorf("update user: %w", &repository.DistrictChangeError{NextAllowed: next})
	w = serveWrite("/api/v1/me", h.UpdateMe, http.MethodPatch, "/api/v1/me", `{"district":13}`, "")
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	if w.Code != http.StatusTooManyRequests || body["code"] != "district_change_limited" ||
		body["next_change_after"] != next.UTC().Format(time.RFC3339) {
		t.Errorf("too soon: %d %v; want 429 district_change_limited with next_change_after", w.Code, body)
	}
	retry, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || retry < 71*3600 || retry > 72*3600 {
		t.Errorf("Retry-After = %q, want about 72 hours in seconds", w.Header().Get("Retry-After"))
	}

	users.updateErr = fmt.Errorf("update user: %w: CA has no district 53", repository.ErrInvalidSeat)
	w = serveWrite("/api/v1/me", h.UpdateMe, http.MethodPatch, "/api/v1/me", `{"state":"CA","district":53}`, "")
	clear(body)
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	if w.Code != http.StatusBadRequest || body["code"] != "invalid_seat" {
		t.Errorf("invalid seat: %d %v; want 400 invalid_seat", w.Code, body)
	}

	users.updateErr = errors.New("spanner down")
	w = serveWrite("/api/v1/me", h.UpdateMe, http.MethodPatch, "/api/v1/me", `{"district":13}`, "")
	if w.Code != http.StatusInternalServerError {
		t.Errorf("repository failure: %d, want 500", w.Code)
	}
}

func TestCreateMeRecordsProvider(t *testing.T) {
	users := newAccountUsers()
	p := auth.Principal{UID: "uid-9", Provider: "password"}
	fake := authtest.New()
	fake.Add("t", p)
	h := newAccountHandler(users, fake, nil)
	w := httptest.NewRecorder()
	h.CreateMe(w, signedIn(http.MethodPost, "/api/v1/me", "", "t", p, ""))
	u := users.byAuthUID["uid-9"]
	if w.Code != http.StatusCreated || u == nil || u.SignInProvider == nil || *u.SignInProvider != "password" {
		t.Errorf("create: %d, user %+v; want 201 with provider password", w.Code, u)
	}
	if !strings.Contains(w.Body.String(), `"sign_in_provider":"password"`) {
		t.Errorf("body = %s, want the provider", w.Body)
	}
}
