package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/appcheck"
)

// ErrInvalidAppCheckToken means the App Check token is malformed, expired,
// signed with an unknown key, or for another project.
var ErrInvalidAppCheckToken = errors.New("invalid App Check token")

// AppCheckMode is APP_CHECK_MODE: how the vote and import routes treat
// Firebase App Check (docs/design/89-aggregate-analytics.md).
type AppCheckMode string

const (
	// AppCheckOff skips App Check; votes are stored with app_check_ok NULL.
	AppCheckOff AppCheckMode = "off"
	// AppCheckAudit checks tokens and records the result, but rejects nothing.
	AppCheckAudit AppCheckMode = "audit"
	// AppCheckEnforce rejects writes without a valid token.
	AppCheckEnforce AppCheckMode = "enforce"
)

// ParseAppCheckMode reads APP_CHECK_MODE; empty means off.
func ParseAppCheckMode(s string) (AppCheckMode, error) {
	switch m := AppCheckMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "", AppCheckOff:
		return AppCheckOff, nil
	case AppCheckAudit, AppCheckEnforce:
		return m, nil
	default:
		return "", fmt.Errorf("APP_CHECK_MODE=%q: want off, audit or enforce", s)
	}
}

// AppChecker verifies App Check tokens.
type AppChecker interface {
	// VerifyAppCheck returns nil for a valid token, and an error wrapping
	// ErrInvalidAppCheckToken for a bad one. Any other error is an outage.
	VerifyAppCheck(ctx context.Context, token string) error
}

// FirebaseAppCheck is an AppChecker backed by the Firebase Admin SDK. It
// verifies tokens locally against Google's public keys, which it fetches at
// start-up and refreshes every six hours. There is no App Check emulator:
// local development runs with APP_CHECK_MODE=off.
type FirebaseAppCheck struct {
	client *appcheck.Client
}

// NewFirebaseAppCheck creates an AppChecker for projectID, fetching the
// signing keys. The keys are refreshed until ctx is done.
func NewFirebaseAppCheck(ctx context.Context, projectID string) (*FirebaseAppCheck, error) {
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID})
	if err != nil {
		return nil, fmt.Errorf("firebase app: %w", err)
	}
	client, err := app.AppCheck(ctx)
	if err != nil {
		return nil, fmt.Errorf("app check client: %w", err)
	}
	return &FirebaseAppCheck{client: client}, nil
}

// VerifyAppCheck implements AppChecker. Keys come from the cache, so every
// failure is a bad token.
func (f *FirebaseAppCheck) VerifyAppCheck(_ context.Context, token string) (err error) {
	// The SDK type-asserts claims without checking, so a signed token with an
	// odd claim shape panics instead of failing.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: malformed claims", ErrInvalidAppCheckToken)
		}
	}()
	if _, err = f.client.VerifyToken(token); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAppCheckToken, err)
	}
	return nil
}
