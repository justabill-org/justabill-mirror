package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/db/model"
)

type contextKey string

const (
	userIDKey    contextKey = "user_id"
	principalKey contextKey = "principal"
)

const (
	// userIDCacheSize bounds the auth_uid → user_id cache.
	userIDCacheSize = 10_000
	// userIDCacheTTL is how long a mapping is trusted. A user deleted on
	// another instance can resolve here for up to this long, but their rows
	// are already gone, so writes for them fail.
	userIDCacheTTL = 5 * time.Minute
)

// UserLookup finds a user by their Identity Platform UID.
type UserLookup interface {
	// GetByAuthUID returns nil, nil when no user has authUID.
	GetByAuthUID(ctx context.Context, authUID string) (*model.User, error)
}

// Auth authenticates requests that carry an Identity Platform ID token.
type Auth struct {
	verifier auth.Verifier
	users    UserLookup
	userIDs  *expirable.LRU[string, string]
	log      *slog.Logger
}

// NewAuth creates the auth middleware.
func NewAuth(verifier auth.Verifier, users UserLookup, log *slog.Logger) *Auth {
	return &Auth{
		verifier: verifier,
		users:    users,
		userIDs:  expirable.NewLRU[string, string](userIDCacheSize, nil, userIDCacheTTL),
		log:      log,
	}
}

// Handler authenticates the request:
//   - no Authorization header: anonymous, and the request continues;
//   - a header that isn't a valid Bearer ID token: 401, never a silent
//     anonymous fallback, so client bugs show;
//   - a key-fetch or backend failure: 503 (fail closed);
//   - a valid token: the Principal goes into the context, and so does the
//     user_id if the user has an account (POST /me creates it).
func (a *Auth) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		raw, ok := BearerToken(r)
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, codeInvalidToken, "expected Authorization: Bearer <ID token>")
			return
		}
		p, err := a.verifier.Verify(ctx, raw)
		if err != nil {
			a.verifyFailed(ctx, w, err)
			return
		}
		// Responses to signed-in requests are personal.
		w.Header().Set("Cache-Control", "private, no-store")
		ctx = ContextWithPrincipal(ctx, p)

		userID, err := a.userID(ctx, p.UID)
		if err != nil {
			a.log.ErrorContext(ctx, "look up user by auth uid failed", "error", err)
			writeAuthError(w, http.StatusInternalServerError, "internal", "failed to look up account")
			return
		}
		if userID != "" {
			ctx = ContextWithUserID(ctx, userID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Forget drops the cached user_id for uid, e.g. after deleting the account.
func (a *Auth) Forget(uid string) {
	a.userIDs.Remove(uid)
}

func (a *Auth) verifyFailed(ctx context.Context, w http.ResponseWriter, err error) {
	if errors.Is(err, auth.ErrInvalidToken) {
		a.log.InfoContext(ctx, "rejected ID token", "error", err)
		writeAuthError(w, http.StatusUnauthorized, codeInvalidToken, "invalid or expired ID token")
		return
	}
	a.log.ErrorContext(ctx, "verify ID token failed", "error", err)
	writeAuthError(w, http.StatusServiceUnavailable, "auth_unavailable", "sign-in is temporarily unavailable")
}

// userID resolves uid through the cache. Misses aren't cached, so a user
// who just called POST /me is found on their next request.
func (a *Auth) userID(ctx context.Context, uid string) (string, error) {
	if id, ok := a.userIDs.Get(uid); ok {
		return id, nil
	}
	u, err := a.users.GetByAuthUID(ctx, uid)
	if err != nil {
		return "", err
	}
	if u == nil {
		return "", nil
	}
	a.userIDs.Add(uid, u.ID)
	return u.ID, nil
}

// BearerToken returns the token from an "Authorization: Bearer <token>" header.
func BearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}

// RequirePrincipal rejects requests without a verified ID token. Use it on
// routes that work before the account exists (POST and DELETE /me).
func RequirePrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := PrincipalFromContext(r.Context()); !ok {
			writeAuthError(w, http.StatusUnauthorized, codeUnauthorized, msgSignInRequired)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAuth rejects requests without a user in the context: 401 when
// anonymous, 403 when signed in but the account hasn't been created yet.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserIDFromContext(r.Context()) != "" {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := PrincipalFromContext(r.Context()); ok {
			writeAuthError(w, http.StatusForbidden, "no_account",
				"no account for this sign-in; create it with POST /api/v1/me")
			return
		}
		writeAuthError(w, http.StatusUnauthorized, codeUnauthorized, msgSignInRequired)
	})
}

const (
	codeInvalidToken  = "invalid_token"
	codeUnauthorized  = "unauthorized"
	msgSignInRequired = "sign-in required"
)

// writeAuthError writes {"error": msg, "code": code}. Clients branch on code.
func writeAuthError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{keyError: msg, "code": code})
}

// UserIDFromContext returns the signed-in user's user_id, or "".
func UserIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(userIDKey).(string)
	return id
}

// ContextWithUserID stores the signed-in user's user_id.
func ContextWithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// PrincipalFromContext returns the verified identity, if the request had one.
func PrincipalFromContext(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey).(auth.Principal)
	return p, ok
}

// ContextWithPrincipal stores the verified identity.
func ContextWithPrincipal(ctx context.Context, p auth.Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}
