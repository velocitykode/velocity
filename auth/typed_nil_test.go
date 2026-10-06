package auth

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

type nilUserStore struct{ UserStore }

type nilHasher struct{ Hasher }

type nilThrottler struct{ contract.LoginThrottler }

type nilRenderContext struct{ contract.RenderContext }

// A typed nil is nil at the auth entry points that refuse nil.
func TestTypedNilIsNil(t *testing.T) {
	var (
		store     *nilUserStore
		hasher    *nilHasher
		throttler *nilThrottler
		rc        *nilRenderContext
	)

	m := NewManager()
	m.SetUserStore(store)
	if got := m.DefaultUserStore(); got != nil {
		t.Errorf("DefaultUserStore after SetUserStore(typed nil) = %T, want none", got)
	}

	if _, err := HashRecoveryCode(hasher, "code"); err == nil {
		t.Error("HashRecoveryCode(typed nil hasher) = nil error, want one")
	}
	if _, _, err := (&TOTPGenerator{}).ConsumeRecoveryCodeHashed(hasher, nil, "code"); err == nil {
		t.Error("ConsumeRecoveryCodeHashed(typed nil hasher) = nil error, want one")
	}

	r := httptest.NewRequest("POST", "/login", nil)
	if d := IdentifierDelay(throttler, r, "k"); d != 0 {
		t.Errorf("IdentifierDelay(typed nil throttler) = %v, want 0", d)
	}

	if m.RenderUnauthenticated(rc, errors.New("unauthenticated"), nil) {
		t.Error("RenderUnauthenticated(typed nil) = true, want false")
	}
	if m.RenderAlreadyAuthenticated(rc, errors.New("unauthenticated"), nil) {
		t.Error("RenderAlreadyAuthenticated(typed nil) = true, want false")
	}
}
