package auth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/auth"
)

// These tests run against the Firebase Auth emulator (task infra:up; CI's
// API Tests job starts it) and skip without FIREBASE_AUTH_EMULATOR_HOST.

const emulatorProject = "demo-justabill"

func emulatorHost(t *testing.T) string {
	t.Helper()
	host := os.Getenv("FIREBASE_AUTH_EMULATOR_HOST")
	if host == "" {
		t.Skip("FIREBASE_AUTH_EMULATOR_HOST not set; start the Auth emulator with task infra:up")
	}
	return host
}

func newFirebase(t *testing.T, project string) *auth.Firebase {
	t.Helper()
	f, err := auth.NewFirebase(t.Context(), project)
	if err != nil {
		t.Fatalf("NewFirebase: %v", err)
	}
	return f
}

// mintToken signs in to the emulator as a fake Google user, like
// dev/auth-emulator/mint-token.sh, and returns the ID token and uid.
func mintToken(t *testing.T, host, sub string) (string, string) {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{"sub": sub, "email": sub + "@example.com", "email_verified": true})
	body, _ := json.Marshal(map[string]any{
		"postBody":          "providerId=google.com&id_token=" + url.QueryEscape(string(claims)),
		"requestUri":        "http://localhost",
		"returnSecureToken": true,
	})
	endpoint := "http://" + host + "/identitytoolkit.googleapis.com/v1/accounts:signInWithIdp?key=fake-api-key"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sign in to the emulator: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		IDToken string `json:"idToken"`
		LocalID string `json:"localId"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil || out.IDToken == "" {
		t.Fatalf("emulator sign-in: status %d, %v", resp.StatusCode, err)
	}
	return out.IDToken, out.LocalID
}

// unsignedToken builds an emulator-style (alg none) token with claims, to
// exercise the checks the SDK still makes in emulator mode.
func unsignedToken(claims map[string]any) string {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return enc.EncodeToString(header) + "." + enc.EncodeToString(payload) + "."
}

// validClaims are the claims of a real emulator token for uid. In emulator
// mode the SDK also looks the user up, so uid must exist.
func validClaims(now time.Time, uid string) map[string]any {
	return map[string]any{
		"iss":       "https://securetoken.google.com/" + emulatorProject,
		"aud":       emulatorProject,
		"sub":       uid,
		"user_id":   uid,
		"iat":       now.Add(-time.Minute).Unix(),
		"exp":       now.Add(time.Hour).Unix(),
		"auth_time": now.Add(-time.Minute).Unix(),
		"firebase":  map[string]any{"sign_in_provider": "google.com"},
	}
}

func TestFirebaseVerifiesEmulatorToken(t *testing.T) {
	host := emulatorHost(t)
	f := newFirebase(t, emulatorProject)
	token, uid := mintToken(t, host, fmt.Sprintf("verify-%d", time.Now().UnixNano()))

	p, err := f.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.UID != uid || p.Provider != "google.com" {
		t.Errorf("principal = %+v, want uid %s via google.com", p, uid)
	}
	if !p.SignedInWithin(time.Minute, time.Now()) {
		t.Errorf("auth_time = %v, want within the last minute", p.AuthTime)
	}
	if _, err = f.VerifyAndCheckRevoked(t.Context(), token); err != nil {
		t.Errorf("VerifyAndCheckRevoked: %v", err)
	}
}

func TestFirebaseRejects(t *testing.T) {
	host := emulatorHost(t)
	now := time.Now()
	token, uid := mintToken(t, host, fmt.Sprintf("reject-%d", now.UnixNano()))
	with := func(key string, v any) string {
		c := validClaims(now, uid)
		if v == nil {
			delete(c, key)
		} else {
			c[key] = v
		}
		return unsignedToken(c)
	}

	tests := []struct {
		name    string
		project string
		token   string
	}{
		{"garbage", emulatorProject, "not-a-jwt"},
		{"wrong audience", emulatorProject, with("aud", "other-project")},
		{"wrong issuer", emulatorProject, with("iss", "https://securetoken.google.com/other-project")},
		{"expired", emulatorProject, with("exp", now.Add(-time.Hour).Unix())},
		{"issued in the future", emulatorProject, with("iat", now.Add(time.Hour).Unix())},
		{"no subject", emulatorProject, with("sub", nil)},
		{"real token for another project", "demo-other", token},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFirebase(t, tt.project)
			_, err := f.Verify(t.Context(), tt.token)
			if !errors.Is(err, auth.ErrInvalidToken) {
				t.Errorf("Verify error = %v, want ErrInvalidToken", err)
			}
		})
	}
	if _, err := newFirebase(t, emulatorProject).Verify(t.Context(), with("sub", uid)); err != nil {
		t.Errorf("control token with every claim valid: %v", err)
	}
}

func TestFirebaseDeleteUser(t *testing.T) {
	host := emulatorHost(t)
	f := newFirebase(t, emulatorProject)
	token, uid := mintToken(t, host, fmt.Sprintf("delete-%d", time.Now().UnixNano()))
	ctx := context.WithoutCancel(t.Context())

	if err := f.DeleteUser(ctx, uid); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := f.VerifyAndCheckRevoked(ctx, token); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("VerifyAndCheckRevoked after delete = %v, want ErrInvalidToken", err)
	}
	if err := f.DeleteUser(ctx, uid); err != nil {
		t.Errorf("second DeleteUser = %v, want nil for a missing user", err)
	}
}
