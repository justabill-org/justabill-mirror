// Package auth verifies Identity Platform (Firebase Authentication) ID tokens
// and manages sign-in accounts. It knows nothing about HTTP, so the same
// Verifier can back Chi middleware today and a Connect interceptor later.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidToken means the token is malformed, expired, revoked, for another
// project, or otherwise not acceptable. Callers answer it with a 401. Any
// other error from a Verifier is an outage (e.g. the public keys can't be
// fetched), which callers answer with a 5xx.
var ErrInvalidToken = errors.New("invalid ID token")

// Principal is the verified identity behind a request.
type Principal struct {
	// UID is the Identity Platform user ID, stored as users.auth_uid.
	UID string
	// Provider is the sign-in provider, e.g. google.com.
	Provider string
	// AuthTime is when the user last actually signed in, as opposed to when
	// the SDK last refreshed the token.
	AuthTime time.Time
}

// SignedInWithin reports whether the user signed in no longer than d before now.
func (p Principal) SignedInWithin(d time.Duration, now time.Time) bool {
	return !p.AuthTime.IsZero() && now.Sub(p.AuthTime) <= d
}

// Verifier checks ID tokens.
type Verifier interface {
	// Verify checks the token's signature, issuer, audience, expiry, issue
	// time and subject. It does not call Identity Platform, apart from
	// fetching and caching Google's public keys.
	Verify(ctx context.Context, rawToken string) (Principal, error)
	// VerifyAndCheckRevoked also asks Identity Platform whether the user's
	// tokens were revoked or the user was disabled. Use it for sensitive
	// operations such as account deletion.
	VerifyAndCheckRevoked(ctx context.Context, rawToken string) (Principal, error)
}

// Client verifies tokens and manages sign-in accounts.
type Client interface {
	Verifier
	// DeleteUser revokes the user's refresh tokens and deletes the user from
	// Identity Platform. Deleting a user that doesn't exist is not an error.
	DeleteUser(ctx context.Context, uid string) error
}

// Settings is the API's sign-in configuration.
type Settings struct {
	// Production is true when APP_ENV=production.
	Production bool
	// Enabled is ACCOUNTS_ENABLED: whether the account routes are served.
	Enabled bool
	// ProjectID is AUTH_PROJECT_ID, the GCP project tokens must be issued for.
	ProjectID string
	// EmulatorHost is FIREBASE_AUTH_EMULATOR_HOST.
	EmulatorHost string
	// AppCheck is APP_CHECK_MODE for the vote and import routes.
	AppCheck AppCheckMode
}

// Validate refuses unsafe or incomplete settings.
func (s Settings) Validate() error {
	// In emulator mode the Admin SDK skips signature checks, so anyone could
	// mint a token for any user. Refuse even with accounts disabled, so the
	// variable can't lie in wait for ACCOUNTS_ENABLED to be turned on.
	if s.Production && s.EmulatorHost != "" {
		return errors.New("FIREBASE_AUTH_EMULATOR_HOST must not be set when APP_ENV=production")
	}
	if s.Enabled && s.ProjectID == "" {
		return fmt.Errorf("AUTH_PROJECT_ID is required when accounts are enabled%s", s.hint())
	}
	return nil
}

func (s Settings) hint() string {
	if s.Production {
		return ""
	}
	return " (locally: AUTH_PROJECT_ID=demo-justabill with the Auth emulator, see .env.example)"
}
