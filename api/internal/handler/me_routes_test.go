package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
)

// accountStore is a seeded store of users by user ID. err fails GetByID and RemoveFavorite;
// reads counts GetByID calls and removed records each removal as "<user>/<bill>".
type accountStore struct {
	mockUserRepo

	users   map[string]model.User
	err     error
	reads   int
	removed []string
}

func (s *accountStore) GetByID(_ context.Context, id string) (*model.User, error) {
	s.reads++
	u, ok := s.users[id]
	if !ok || s.err != nil {
		return nil, s.err
	}
	return &u, nil
}
func (s *accountStore) RemoveFavorite(_ context.Context, userID, billID string) error {
	if s.err != nil {
		return s.err
	}
	s.removed = append(s.removed, userID+"/"+billID)
	return nil
}

func seededAccounts() *accountStore {
	state, district := "KS", 3
	return &accountStore{users: map[string]model.User{"u1": {
		ID: "u1", AuthUID: "firebase-uid-1", State: &state, District: &district,
		CreatedAt: time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
	}}}
}

// meRouter mounts the routes behind mw.RequireAuth, as mountAccountRoutes does, and serves a
// request as user (none when empty).
func meRouter(users *accountStore, bills *billStore) func(method, path, user string) *httptest.ResponseRecorder {
	h := newTestHandler(bills)
	h.Users = users
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/api/v1/me", h.GetMe)
		r.Delete("/api/v1/me/favorites/{billID}", h.RemoveFavorite)
	})
	return func(method, path, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if user != "" {
			req = req.WithContext(mw.ContextWithUserID(req.Context(), user))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
}

func TestGetMe(t *testing.T) {
	serve := meRouter(seededAccounts(), &billStore{})

	w := serve(http.MethodGet, "/api/v1/me", "u1")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	// No auth_uid: the account's sign-in identity stays server-side.
	assertJSON(t, w.Body.Bytes(), `{"id":"u1","state":"KS","district":3,"created_at":"2026-09-01T12:00:00Z"}`)
}

func TestGetMe_Errors(t *testing.T) {
	tests := []struct {
		name, user string
		err        error
		wantStatus int
		wantError  string
		wantReads  int
	}{
		{name: "signed out", user: "", wantStatus: http.StatusUnauthorized, wantError: "sign-in required"},
		{
			name:       "no such user",
			user:       "u-gone",
			wantStatus: http.StatusNotFound,
			wantError:  "user not found",
			wantReads:  1,
		},
		{name: "read fails", user: "u1", err: errSpanner, wantStatus: http.StatusInternalServerError, wantReads: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := seededAccounts()
			users.err = tt.err
			serve := meRouter(users, &billStore{})

			w := serve(http.MethodGet, "/api/v1/me", tt.user)

			if users.reads != tt.wantReads {
				t.Errorf("user reads = %d, want %d", users.reads, tt.wantReads)
			}
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

func TestRemoveFavorite(t *testing.T) {
	bills, _ := seededBillStores()
	users := seededAccounts()
	serve := meRouter(users, bills)

	w := serve(http.MethodDelete, "/api/v1/me/favorites/"+seededBill, "u1")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	assertJSON(t, w.Body.Bytes(), `{"bill_id":"hr-119-1","status":"ok"}`)
	if len(users.removed) != 1 || users.removed[0] != "u1/"+seededBill {
		t.Errorf("removed = %v, want [u1/%s]", users.removed, seededBill)
	}
}

func TestRemoveFavorite_Errors(t *testing.T) {
	tests := []struct {
		name, user, bill string
		err              error
		byIDErr          error
		wantStatus       int
		wantError        string
	}{
		{name: "signed out", bill: seededBill, wantStatus: http.StatusUnauthorized, wantError: "sign-in required"},
		{name: "unknown bill", user: "u1", bill: unknownBill, wantStatus: http.StatusNotFound,
			wantError: "bill not found"},
		{name: "malformed id", user: "u1", bill: "hr-119-1x", wantStatus: http.StatusBadRequest,
			wantError: "invalid bill id"},
		{name: "bill read fails", user: "u1", bill: seededBill, byIDErr: errSpanner,
			wantStatus: http.StatusInternalServerError},
		{name: "remove fails", user: "u1", bill: seededBill, err: errSpanner,
			wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bills, _ := seededBillStores()
			bills.byIDErr = tt.byIDErr
			users := seededAccounts()
			users.err = tt.err
			serve := meRouter(users, bills)

			w := serve(http.MethodDelete, "/api/v1/me/favorites/"+tt.bill, tt.user)

			if len(users.removed) != 0 {
				t.Errorf("removed = %v, want nothing", users.removed)
			}
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
