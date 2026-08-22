package auth_test

import (
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/authtest"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
)

// TestMemorySessionStore_Contract runs the authtest spec against the
// in-process ServerSessionStore.
func TestMemorySessionStore_Contract(t *testing.T) {
	authtest.RunServerSessionStoreContractTests(t, func(t *testing.T) auth.ServerSessionStore {
		return session.NewMemoryStore()
	})
}

// TestNoopLoginThrottler_Contract runs the authtest spec against the
// default no-op throttler.
func TestNoopLoginThrottler_Contract(t *testing.T) {
	authtest.RunLoginThrottlerContractTests(t, func(t *testing.T) contract.LoginThrottler {
		return auth.NoopLoginThrottler{}
	})
}
