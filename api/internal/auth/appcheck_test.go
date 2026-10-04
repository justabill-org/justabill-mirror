package auth_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"firebase.google.com/go/v4/appcheck"

	"github.com/justabill-org/justabill/api/internal/auth"
)

func TestParseAppCheckMode(t *testing.T) {
	for in, want := range map[string]auth.AppCheckMode{
		"": auth.AppCheckOff, "off": auth.AppCheckOff, "Audit": auth.AppCheckAudit, "enforce": auth.AppCheckEnforce,
	} {
		if got, err := auth.ParseAppCheckMode(in); err != nil || got != want {
			t.Errorf("ParseAppCheckMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := auth.ParseAppCheckMode("on"); err == nil {
		t.Error(`ParseAppCheckMode("on"): want an error`)
	}
}

// appCheckKeys serves a JWKS for a fresh RSA key and signs tokens with it,
// standing in for Google's App Check key endpoint.
type appCheckKeys struct {
	key *rsa.PrivateKey
}

func newAppCheckKeys(t *testing.T) *appCheckKeys {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	k := &appCheckKeys{key: key}
	enc := base64.RawURLEncoding
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
		"n": enc.EncodeToString(key.N.Bytes()),
		"e": enc.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	}))
	t.Cleanup(srv.Close)

	// The SDK reads its key URL from a package variable.
	orig := appcheck.JWKSUrl
	appcheck.JWKSUrl = srv.URL                    //nolint:reassign // the SDK has no other way in
	t.Cleanup(func() { appcheck.JWKSUrl = orig }) //nolint:reassign // restore it
	return k
}

// sign returns an RS256 token with header overrides and claims.
func (k *appCheckKeys) sign(t *testing.T, header, claims map[string]any) string {
	t.Helper()
	h := map[string]any{"alg": "RS256", "typ": "JWT", "kid": "k1"}
	maps.Copy(h, header)
	enc := base64.RawURLEncoding
	hj, _ := json.Marshal(h)
	cj, _ := json.Marshal(claims)
	input := enc.EncodeToString(hj) + "." + enc.EncodeToString(cj)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + enc.EncodeToString(sig)
}

func appCheckClaims(project string, exp time.Time) map[string]any {
	return map[string]any{
		"iss": "https://firebaseappcheck.googleapis.com/123456",
		"sub": "1:123456:web:abc",
		"aud": []string{"projects/123456", "projects/" + project},
		"iat": time.Now().Add(-time.Minute).Unix(),
		"exp": exp.Unix(),
	}
}

func TestFirebaseAppCheckVerifiesTokens(t *testing.T) {
	keys := newAppCheckKeys(t)
	checker, err := auth.NewFirebaseAppCheck(t.Context(), "example-prod")
	if err != nil {
		t.Fatalf("NewFirebaseAppCheck: %v", err)
	}
	hour := time.Now().Add(time.Hour)

	valid := keys.sign(t, nil, appCheckClaims("example-prod", hour))
	if err = checker.VerifyAppCheck(t.Context(), valid); err != nil {
		t.Fatalf("valid token: %v", err)
	}

	stringAud := appCheckClaims("example-prod", hour)
	stringAud["aud"] = "projects/example-prod"
	badIssuer := appCheckClaims("example-prod", hour)
	badIssuer["iss"] = "https://securetoken.google.com/example-prod"
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	forged := (&appCheckKeys{key: other}).sign(t, nil, appCheckClaims("example-prod", hour))

	for name, token := range map[string]string{
		"other project":  keys.sign(t, nil, appCheckClaims("someone-else", hour)),
		"expired":        keys.sign(t, nil, appCheckClaims("example-prod", time.Now().Add(-time.Minute))),
		"wrong issuer":   keys.sign(t, nil, badIssuer),
		"unknown key id": keys.sign(t, map[string]any{"kid": "k2"}, appCheckClaims("example-prod", hour)),
		"forged":         forged,
		"not a JWT":      "not-a-token",
		"HS256":          keys.sign(t, map[string]any{"alg": "HS256"}, appCheckClaims("example-prod", hour)),
		"aud not a list": keys.sign(t, nil, stringAud),
	} {
		if err = checker.VerifyAppCheck(t.Context(), token); !errors.Is(err, auth.ErrInvalidAppCheckToken) {
			t.Errorf("%s: VerifyAppCheck = %v, want ErrInvalidAppCheckToken", name, err)
		}
	}
}
