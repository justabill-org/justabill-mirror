package auth

import (
	"context"
	"fmt"
	"time"

	firebase "firebase.google.com/go/v4"
	fbauth "firebase.google.com/go/v4/auth"
)

// Firebase is a Client backed by the Firebase Admin SDK. With
// FIREBASE_AUTH_EMULATOR_HOST set in the process environment, the SDK talks to
// the Auth emulator and accepts its unsigned tokens.
type Firebase struct {
	client *fbauth.Client
}

// NewFirebase creates a Client for projectID. Outside the emulator it needs
// Application Default Credentials (Workload Identity in production) for
// DeleteUser and revocation checks.
func NewFirebase(ctx context.Context, projectID string) (*Firebase, error) {
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID})
	if err != nil {
		return nil, fmt.Errorf("firebase app: %w", err)
	}
	client, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("firebase auth client: %w", err)
	}
	return &Firebase{client: client}, nil
}

// Verify implements Verifier.
func (f *Firebase) Verify(ctx context.Context, rawToken string) (Principal, error) {
	tok, err := f.client.VerifyIDToken(ctx, rawToken)
	if err != nil {
		return Principal{}, classify(err)
	}
	return principal(tok), nil
}

// VerifyAndCheckRevoked implements Verifier.
func (f *Firebase) VerifyAndCheckRevoked(ctx context.Context, rawToken string) (Principal, error) {
	tok, err := f.client.VerifyIDTokenAndCheckRevoked(ctx, rawToken)
	if err != nil {
		return Principal{}, classify(err)
	}
	return principal(tok), nil
}

// DeleteUser implements Client.
func (f *Firebase) DeleteUser(ctx context.Context, uid string) error {
	if err := f.client.RevokeRefreshTokens(ctx, uid); err != nil && !fbauth.IsUserNotFound(err) {
		return fmt.Errorf("revoke refresh tokens: %w", err)
	}
	if err := f.client.DeleteUser(ctx, uid); err != nil && !fbauth.IsUserNotFound(err) {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

func principal(tok *fbauth.Token) Principal {
	p := Principal{UID: tok.UID, Provider: tok.Firebase.SignInProvider}
	if tok.AuthTime > 0 {
		p.AuthTime = time.Unix(tok.AuthTime, 0)
	}
	return p
}

// classify wraps token problems in ErrInvalidToken with a short reason, and
// passes everything else (key fetch failures, backend errors) through. The
// SDK's messages quote claim values but never the token itself.
func classify(err error) error {
	var reason string
	switch {
	case fbauth.IsIDTokenExpired(err):
		reason = "expired"
	case fbauth.IsIDTokenRevoked(err):
		reason = "revoked"
	case fbauth.IsUserDisabled(err):
		reason = "user disabled"
	case fbauth.IsUserNotFound(err):
		reason = "user not found"
	case fbauth.IsIDTokenInvalid(err):
		reason = "invalid"
	case fbauth.IsCertificateFetchFailed(err):
		return fmt.Errorf("fetch token signing keys: %w", err)
	default:
		return fmt.Errorf("verify ID token: %w", err)
	}
	return fmt.Errorf("%w (%s): %w", ErrInvalidToken, reason, err)
}
