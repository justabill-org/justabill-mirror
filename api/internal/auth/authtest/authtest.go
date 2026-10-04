// Package authtest provides a fake auth.Client for tests.
package authtest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/justabill-org/justabill/api/internal/auth"
)

// Fake is an auth.Client that accepts only the tokens registered with Add.
type Fake struct {
	mu      sync.Mutex
	tokens  map[string]auth.Principal
	revoked map[string]bool
	deleted []string

	// Err, when set, is returned by Verify and VerifyAndCheckRevoked for every
	// token, e.g. to simulate a key-fetch outage.
	Err error
	// DeleteErr, when set, is returned by DeleteUser.
	DeleteErr error
}

// New returns an empty Fake.
func New() *Fake {
	return &Fake{tokens: map[string]auth.Principal{}, revoked: map[string]bool{}}
}

// Add makes rawToken verify as p.
func (f *Fake) Add(rawToken string, p auth.Principal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[rawToken] = p
}

// Revoke makes VerifyAndCheckRevoked reject every token for uid.
func (f *Fake) Revoke(uid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked[uid] = true
}

// Deleted returns the UIDs passed to DeleteUser, in order.
func (f *Fake) Deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// Verify implements auth.Verifier.
func (f *Fake) Verify(_ context.Context, rawToken string) (auth.Principal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return auth.Principal{}, f.Err
	}
	p, ok := f.tokens[rawToken]
	if !ok {
		return auth.Principal{}, fmt.Errorf("%w (unknown test token)", auth.ErrInvalidToken)
	}
	return p, nil
}

// VerifyAndCheckRevoked implements auth.Verifier.
func (f *Fake) VerifyAndCheckRevoked(ctx context.Context, rawToken string) (auth.Principal, error) {
	p, err := f.Verify(ctx, rawToken)
	if err != nil {
		return p, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revoked[p.UID] {
		return auth.Principal{}, fmt.Errorf("%w (revoked)", auth.ErrInvalidToken)
	}
	return p, nil
}

// DeleteUser implements auth.Client.
func (f *Fake) DeleteUser(_ context.Context, uid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	if uid == "" {
		return errors.New("empty uid")
	}
	f.deleted = append(f.deleted, uid)
	f.revoked[uid] = true
	return nil
}
