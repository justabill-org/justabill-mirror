package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/api/internal/auth/authtest"
	"github.com/justabill-org/justabill/api/internal/handler"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
)

// alreadyVoted is a bill the accountUsers fake treats as voted on already.
const alreadyVoted = "119-s-2"

// accountUsers is an in-memory UserRepo for the account handlers.
type accountUsers struct {
	mockUserRepo

	byAuthUID      map[string]*model.User
	imported       []model.UserVote
	importAppCheck *bool
	importCap      int
	deletedVotes   []string
	deleteVoteErr  error
	deleted        []string
	voteChecks     []model.VoteChecks
	voteErr        error
	updates        []model.UserUpdate
	updateErr      error
}

func newAccountUsers() *accountUsers {
	return &accountUsers{byAuthUID: map[string]*model.User{}}
}

func (m *accountUsers) CreateForAuthUID(_ context.Context, id, authUID, provider string) (*model.User, error) {
	if u, ok := m.byAuthUID[authUID]; ok {
		return u, nil
	}
	u := &model.User{ID: id, AuthUID: authUID, SignInProvider: &provider}
	m.byAuthUID[authUID] = u
	return u, nil
}

func (m *accountUsers) Delete(_ context.Context, id string) error {
	m.deleted = append(m.deleted, id)
	return nil
}

func (m *accountUsers) ImportVotes(
	_ context.Context, _ string, votes []model.UserVote, checks model.VoteChecks,
) (model.ImportResult, error) {
	m.importAppCheck = checks.AppCheckOK
	m.importCap = checks.DailyCap
	var res model.ImportResult
	for _, v := range votes {
		switch {
		case v.BillID == alreadyVoted:
		case checks.DailyCap > 0 && len(m.imported) >= checks.DailyCap:
			res.Capped = append(res.Capped, v.BillID)
		default:
			m.imported = append(m.imported, v)
			res.Imported++
		}
	}
	return res, nil
}

func (m *accountUsers) DeleteVote(_ context.Context, userID, billID string) error {
	m.deletedVotes = append(m.deletedVotes, userID+"|"+billID)
	return m.deleteVoteErr
}

func (m *accountUsers) CastVote(_ context.Context, _, _, _ string, checks model.VoteChecks) error {
	m.voteChecks = append(m.voteChecks, checks)
	return m.voteErr
}

func (m *accountUsers) Update(_ context.Context, id string, u model.UserUpdate) (*model.User, error) {
	m.updates = append(m.updates, u)
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return &model.User{ID: id, State: u.State, District: u.District}, nil
}

func (m *accountUsers) Export(_ context.Context, userID string) (*model.UserExport, error) {
	if userID != "user-1" {
		return nil, nil //nolint:nilnil // matches the repository contract
	}
	return &model.UserExport{User: model.User{ID: userID}, AuthUID: "uid-1", Votes: []model.UserVote{
		{UserID: userID, BillID: "119-hr-1", Vote: "yea", AppCheckOK: new(true), RecordedAt: new(time.Now())},
	}}, nil
}

func newAccountHandler(users *accountUsers, accounts auth.Client, forget func(string)) *handler.Handler {
	h := newTestHandler(&mockBillRepo{})
	h.Users = users
	h.SetLogger(slog.New(slog.DiscardHandler))
	handler.WithAccounts(accounts, forget)(h)
	return h
}

// signedIn returns a request carrying token, as the auth middleware leaves it.
func signedIn(method, target, body, token string, p auth.Principal, userID string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	ctx := mw.ContextWithPrincipal(req.Context(), p)
	if userID != "" {
		ctx = mw.ContextWithUserID(ctx, userID)
	}
	return req.WithContext(ctx)
}

func TestCreateMeIsIdempotent(t *testing.T) {
	users := newAccountUsers()
	fake := authtest.New()
	p := auth.Principal{UID: "uid-1", Provider: "google.com"}
	fake.Add("t", p)
	h := newAccountHandler(users, fake, nil)

	first := httptest.NewRecorder()
	h.CreateMe(first, signedIn(http.MethodPost, "/api/v1/me", "", "t", p, ""))
	if first.Code != http.StatusCreated {
		t.Fatalf("first POST /me = %d, want 201", first.Code)
	}
	var created model.User
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("body %s: %v", first.Body.String(), err)
	}
	if strings.Contains(first.Body.String(), "uid-1") {
		t.Errorf("response %s exposes the auth_uid", first.Body.String())
	}

	again := httptest.NewRecorder()
	h.CreateMe(again, signedIn(http.MethodPost, "/api/v1/me", "", "t", p, ""))
	var existing model.User
	_ = json.Unmarshal(again.Body.Bytes(), &existing)
	if again.Code != http.StatusOK || existing.ID != created.ID {
		t.Errorf("repeat POST /me = %d, id %q; want 200 and id %q", again.Code, existing.ID, created.ID)
	}
	if len(users.byAuthUID) != 1 {
		t.Errorf("users = %d, want 1", len(users.byAuthUID))
	}
}

func TestDeleteMe(t *testing.T) {
	recent := auth.Principal{UID: "uid-1", AuthTime: time.Now().Add(-time.Minute)}
	stale := auth.Principal{UID: "uid-1", AuthTime: time.Now().Add(-time.Hour)}
	never := auth.Principal{UID: "uid-1"}
	outage := errors.New("backend unavailable")

	tests := []struct {
		name        string
		principal   auth.Principal
		userID      string
		revoked     bool
		verifyErr   error
		deleteErr   error
		status      int
		code        string
		wantRows    []string
		wantIDPDel  bool
		wantForgets int
	}{
		{name: "recent sign-in", principal: recent, userID: "user-1",
			status: http.StatusNoContent, wantRows: []string{"user-1"}, wantIDPDel: true, wantForgets: 1},
		{name: "no account row yet", principal: recent,
			status: http.StatusNoContent, wantIDPDel: true, wantForgets: 1},
		{name: "stale sign-in", principal: stale, userID: "user-1",
			status: http.StatusUnauthorized, code: "requires_recent_login"},
		{name: "no auth_time", principal: never, userID: "user-1",
			status: http.StatusUnauthorized, code: "requires_recent_login"},
		{name: "revoked token", principal: recent, userID: "user-1", revoked: true,
			status: http.StatusUnauthorized, code: "invalid_token"},
		{name: "revocation check down", principal: recent, userID: "user-1", verifyErr: outage,
			status: http.StatusServiceUnavailable, code: "auth_unavailable"},
		{name: "identity platform delete fails", principal: recent, userID: "user-1", deleteErr: outage,
			status: http.StatusBadGateway, wantRows: []string{"user-1"}, wantForgets: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := authtest.New()
			fake.Add("tok", tt.principal)
			if tt.revoked {
				fake.Revoke(tt.principal.UID)
			}
			fake.Err, fake.DeleteErr = tt.verifyErr, tt.deleteErr
			users := newAccountUsers()
			forgets := 0
			h := newAccountHandler(users, fake, func(string) { forgets++ })

			w := httptest.NewRecorder()
			h.DeleteMe(w, signedIn(http.MethodDelete, "/api/v1/me", "", "tok", tt.principal, tt.userID))

			if w.Code != tt.status {
				t.Errorf("status = %d, want %d (%s)", w.Code, tt.status, w.Body.String())
			}
			if tt.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tt.code+`"`) {
				t.Errorf("body = %s, want code %s", w.Body.String(), tt.code)
			}
			if fmt.Sprint(users.deleted) != fmt.Sprint(tt.wantRows) {
				t.Errorf("deleted rows = %v, want %v", users.deleted, tt.wantRows)
			}
			if got := len(fake.Deleted()) == 1; got != tt.wantIDPDel {
				t.Errorf("identity platform deletes = %v, want deleted %v", fake.Deleted(), tt.wantIDPDel)
			}
			if forgets != tt.wantForgets {
				t.Errorf("cache forgets = %d, want %d", forgets, tt.wantForgets)
			}
		})
	}
}

func TestExportMe(t *testing.T) {
	h := newAccountHandler(newAccountUsers(), authtest.New(), nil)
	p := auth.Principal{UID: "uid-1"}

	w := httptest.NewRecorder()
	h.ExportMe(w, signedIn(http.MethodGet, "/api/v1/me/export", "", "t", p, "user-1"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", got)
	}
	var export model.UserExport
	if err := json.Unmarshal(w.Body.Bytes(), &export); err != nil || export.User.ID != "user-1" ||
		export.AuthUID != "uid-1" {
		t.Errorf("export = %+v (%v)", export, err)
	}

	w = httptest.NewRecorder()
	h.ExportMe(w, signedIn(http.MethodGet, "/api/v1/me/export", "", "t", p, "user-gone"))
	if w.Code != http.StatusNotFound {
		t.Errorf("missing user status = %d, want 404", w.Code)
	}
}

func TestImportMyVotes(t *testing.T) {
	manyVotes := func(n int) string {
		vs := make([]string, n)
		for i := range vs {
			vs[i] = fmt.Sprintf(`{"bill_id":"119-hr-%d","vote":"yea"}`, i)
		}
		return `{"votes":[` + strings.Join(vs, ",") + `]}`
	}
	tests := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"imports and skips", `{"votes":[{"bill_id":"119-hr-1","vote":"yea","voted_at":"2026-09-20T12:00:00Z"},` +
			`{"bill_id":"` + alreadyVoted + `","vote":"nay"}]}`, http.StatusOK,
			`{"imported":1,"skipped":1,"capped":0,"capped_bill_ids":[]}`},
		{"empty", `{"votes":[]}`, http.StatusOK, `{"imported":0,"skipped":0,"capped":0,"capped_bill_ids":[]}`},
		{"exactly the limit", manyVotes(model.MaxImportVotes), http.StatusOK, `"imported":1000`},
		{"over the limit", manyVotes(model.MaxImportVotes + 1), http.StatusRequestEntityTooLarge, "at most 1000"},
		{"bad vote", `{"votes":[{"bill_id":"119-hr-1","vote":"maybe"}]}`, http.StatusBadRequest, "votes[0]"},
		{"missing bill", `{"votes":[{"vote":"yea"}]}`, http.StatusBadRequest, "bill_id"},
		{"long bill id", `{"votes":[{"bill_id":"` + strings.Repeat("x", 65) + `","vote":"yea"}]}`,
			http.StatusBadRequest, "bill_id"},
		{"not json", `votes`, http.StatusBadRequest, "invalid request body"},
		{"huge body", `{"votes":[{"bill_id":"` + strings.Repeat("x", 600<<10) + `"}]}`,
			http.StatusRequestEntityTooLarge, "too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := newAccountUsers()
			h := newAccountHandler(users, authtest.New(), nil)
			w := httptest.NewRecorder()
			req := signedIn(http.MethodPost, "/api/v1/me/votes:import", tt.body, "t",
				auth.Principal{UID: "u"}, "user-1")
			h.ImportMyVotes(w, req)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.status, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.want) {
				t.Errorf("body = %s, want it to contain %s", w.Body.String(), tt.want)
			}
			if tt.status != http.StatusOK && len(users.imported) != 0 {
				t.Errorf("rejected import stored votes: %v", users.imported)
			}
		})
	}
}

func TestImportMyVotesNormalizes(t *testing.T) {
	users := newAccountUsers()
	h := newAccountHandler(users, authtest.New(), nil)
	body := `{"votes":[{"bill_id":"119-hr-1","vote":"Nay","voted_at":"2026-09-20T12:00:00Z"}]}`
	w := httptest.NewRecorder()
	h.ImportMyVotes(w, signedIn(http.MethodPost, "/", body, "t", auth.Principal{UID: "u"}, "user-1"))
	want := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	got := users.imported
	if len(got) != 1 || got[0].Vote != "nay" || got[0].BillID != "119-hr-1" || !got[0].VotedAt.Equal(want) {
		t.Errorf("imported %+v", got)
	}
}

func TestCreateMeRefusesRevokedTokens(t *testing.T) {
	p := auth.Principal{UID: "uid-1"}
	tests := []struct {
		name      string
		revoked   bool
		verifyErr error
		accounts  bool
		status    int
	}{
		{name: "revoked or deleted user", revoked: true, accounts: true, status: http.StatusUnauthorized},
		{name: "revocation check down", verifyErr: errors.New("backend unavailable"), accounts: true,
			status: http.StatusServiceUnavailable},
		{name: "accounts not configured", status: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := authtest.New()
			fake.Add("t", p)
			if tt.revoked {
				fake.Revoke(p.UID)
			}
			fake.Err = tt.verifyErr
			users := newAccountUsers()
			h := newAccountHandler(users, fake, nil)
			if !tt.accounts {
				h.Accounts = nil
			}
			w := httptest.NewRecorder()
			h.CreateMe(w, signedIn(http.MethodPost, "/api/v1/me", "", "t", p, ""))
			if w.Code != tt.status {
				t.Errorf("POST /me = %d, want %d (%s)", w.Code, tt.status, w.Body.String())
			}
			if len(users.byAuthUID) != 0 {
				t.Errorf("POST /me stored %v, want no account", users.byAuthUID)
			}
		})
	}
}

func TestDeleteVote(t *testing.T) {
	users := newAccountUsers()
	h := newAccountHandler(users, authtest.New(), nil)
	r := chi.NewRouter()
	r.Delete("/bills/{id}/vote", h.DeleteVote)
	p := auth.Principal{UID: "uid-1"}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, signedIn(http.MethodDelete, "/bills/119-hr-1/vote", "", "t", p, "user-1"))
	if w.Code != http.StatusNoContent || fmt.Sprint(users.deletedVotes) != "[user-1|119-hr-1]" {
		t.Errorf("DELETE vote = %d, deleted %v; want 204 and user-1's vote on 119-hr-1", w.Code, users.deletedVotes)
	}

	users.deleteVoteErr = errors.New("spanner down")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, signedIn(http.MethodDelete, "/bills/119-hr-1/vote", "", "t", p, "user-1"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("DELETE vote with a failing store = %d, want 500", w.Code)
	}
}

func TestImportMyVotesDailyCap(t *testing.T) {
	users := newAccountUsers()
	h := newAccountHandler(users, authtest.New(), nil)
	handler.WithWriteRules(handler.WriteRules{DailyVoteCap: 1})(h)
	body := `{"votes":[{"bill_id":"119-hr-1","vote":"yea"},{"bill_id":"` + alreadyVoted + `","vote":"nay"},` +
		`{"bill_id":"119-hr-2","vote":"nay"},{"bill_id":"119-hr-3","vote":"yea"}]}`
	w := httptest.NewRecorder()
	h.ImportMyVotes(w, signedIn(http.MethodPost, "/", body, "t", auth.Principal{UID: "u"}, "user-1"))

	want := `{"imported":1,"skipped":1,"capped":2,"capped_bill_ids":["119-hr-2","119-hr-3"]}`
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != want {
		t.Errorf("import = %d %s, want 200 %s", w.Code, w.Body.String(), want)
	}
	if users.importCap != 1 {
		t.Errorf("import daily cap = %d, want 1", users.importCap)
	}
}

func TestExportMeListsEveryColumn(t *testing.T) {
	h := newAccountHandler(newAccountUsers(), authtest.New(), nil)
	w := httptest.NewRecorder()
	h.ExportMe(w, signedIn(http.MethodGet, "/api/v1/me/export", "", "t", auth.Principal{UID: "uid-1"}, "user-1"))
	for _, key := range []string{`"agg_excluded_at":null`, `"app_check_ok":true`, `"recorded_at":"`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Errorf("export %s, want it to contain %s", w.Body.String(), key)
		}
	}
}
