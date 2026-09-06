// Package authtest provides executable specifications (contract tests) for
// [auth.UserStore], [auth.ServerSessionStore], and [contract.LoginThrottler]
// implementations.
//
// Each runner is independent so drivers that only implement one of the three
// can run the relevant runner without forcing implementations of the others.
package authtest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
)

// UserStoreFactory builds a fresh user store seeded with a known user per
// sub-test. SeedUser is the user the runner asks the user store to look up;
// it must be findable by id and by the credentials map
// {"email": SeedEmail, "password": SeedPassword}.
type UserStoreFactory struct {
	// New returns a fresh user store seeded with the SeedUser (or an empty
	// store if the runner only exercises miss paths).
	New func(t *testing.T) auth.UserStore
	// SeedUser is the user the seeded user store must return.
	SeedUser auth.Authenticatable
	// SeedEmail is the email key in the credentials map.
	SeedEmail string
	// SeedPassword is the plaintext password the user store must accept
	// for the seeded user.
	SeedPassword string
}

// RunUserStoreContractTests exercises [auth.UserStore].
func RunUserStoreContractTests(t *testing.T, f UserStoreFactory) {
	t.Helper()

	t.Run("FindByIDCtx_KnownID_ReturnsUser", func(t *testing.T) {
		p := f.New(t)
		got, err := p.FindByIDCtx(context.Background(), f.SeedUser.GetAuthIdentifier())
		if err != nil {
			t.Fatalf("FindByIDCtx: %v", err)
		}
		if got == nil || got.GetAuthIdentifier() != f.SeedUser.GetAuthIdentifier() {
			t.Fatalf("FindByIDCtx returned wrong user: %v", got)
		}
	})

	t.Run("FindByIDCtx_UnknownID_ReturnsErrUserNotFound", func(t *testing.T) {
		p := f.New(t)
		_, err := p.FindByIDCtx(context.Background(), "definitely-not-a-real-id")
		if err == nil {
			t.Fatal("expected error for unknown ID, got nil")
		}
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("expected ErrUserNotFound, got %v", err)
		}
	})

	t.Run("FindByIDCtx_NilID_ReturnsErrUserNotFound", func(t *testing.T) {
		p := f.New(t)
		_, err := p.FindByIDCtx(context.Background(), nil)
		if err == nil {
			t.Fatal("expected error for nil ID, got nil")
		}
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("expected ErrUserNotFound, got %v", err)
		}
	})

	t.Run("FindByCredentialsCtx_ValidEmail_ReturnsUser", func(t *testing.T) {
		p := f.New(t)
		got, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{
			"email": f.SeedEmail,
		})
		if err != nil {
			t.Fatalf("FindByCredentialsCtx: %v", err)
		}
		if got == nil || got.GetAuthIdentifier() != f.SeedUser.GetAuthIdentifier() {
			t.Fatalf("FindByCredentialsCtx returned wrong user: %v", got)
		}
	})

	t.Run("FindByCredentialsCtx_UnknownEmail_ReturnsErrUserNotFound", func(t *testing.T) {
		p := f.New(t)
		_, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{
			"email": "ghost@example.com",
		})
		if err == nil {
			t.Fatal("expected error for unknown email, got nil")
		}
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("expected ErrUserNotFound, got %v", err)
		}
	})

	t.Run("ValidateCredentials_CorrectPassword_True", func(t *testing.T) {
		p := f.New(t)
		got, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{
			"email": f.SeedEmail,
		})
		if err != nil {
			t.Fatalf("seed lookup: %v", err)
		}
		ok := p.ValidateCredentials(got, map[string]interface{}{"password": f.SeedPassword})
		if !ok {
			t.Fatal("expected ValidateCredentials=true for correct password")
		}
	})

	t.Run("ValidateCredentials_WrongPassword_False", func(t *testing.T) {
		p := f.New(t)
		got, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{
			"email": f.SeedEmail,
		})
		if err != nil {
			t.Fatalf("seed lookup: %v", err)
		}
		ok := p.ValidateCredentials(got, map[string]interface{}{"password": "definitely-wrong"})
		if ok {
			t.Fatal("expected ValidateCredentials=false for wrong password")
		}
	})

	t.Run("ValidateCredentials_NoPassword_False", func(t *testing.T) {
		p := f.New(t)
		got, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{
			"email": f.SeedEmail,
		})
		if err != nil {
			t.Fatalf("seed lookup: %v", err)
		}
		// Missing or wrong-type password must collapse to false; the
		// user store must NOT panic.
		if p.ValidateCredentials(got, map[string]interface{}{}) {
			t.Fatal("expected false when no password supplied")
		}
		if p.ValidateCredentials(got, map[string]interface{}{"password": 1234}) {
			t.Fatal("expected false when password is non-string")
		}
	})
}

// ServerSessionStoreFactory returns a fresh empty store per sub-test.
type ServerSessionStoreFactory func(t *testing.T) auth.ServerSessionStore

// RunServerSessionStoreContractTests exercises [auth.ServerSessionStore].
func RunServerSessionStoreContractTests(t *testing.T, factory ServerSessionStoreFactory) {
	t.Helper()

	makeSession := func(id, userID string) *auth.StoredSession {
		now := time.Now()
		return &auth.StoredSession{
			ID:         id,
			UserID:     userID,
			Data:       map[string]any{"k": "v"},
			CreatedAt:  now,
			LastSeenAt: now,
			ExpiresAt:  now.Add(time.Hour),
			IPAddress:  "127.0.0.1",
			UserAgent:  "contract-test",
		}
	}

	t.Run("Get_UnknownID_ReturnsErrSessionNotFound", func(t *testing.T) {
		s := factory(t)
		_, err := s.Get(context.Background(), "no-such-id")
		if err == nil {
			t.Fatal("expected error for unknown id")
		}
		if !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("expected ErrSessionNotFound, got %v", err)
		}
	})

	t.Run("Put_Then_Get_RoundTripsSession", func(t *testing.T) {
		s := factory(t)
		sess := makeSession("sess-1", "user-1")
		if err := s.Put(context.Background(), sess); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, err := s.Get(context.Background(), "sess-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.UserID != "user-1" {
			t.Fatalf("UserID mismatch: %q", got.UserID)
		}
	})

	t.Run("Get_ExpiredSession_ReturnsErrSessionExpired", func(t *testing.T) {
		s := factory(t)
		sess := makeSession("expired-1", "user-1")
		sess.ExpiresAt = time.Now().Add(-time.Hour)
		_ = s.Put(context.Background(), sess)
		_, err := s.Get(context.Background(), "expired-1")
		if err == nil {
			t.Fatal("expected error for expired session")
		}
		// The interface doc promises ErrSessionExpired specifically for
		// records past ExpiresAt (the store also removes the record).
		// Collapsing to ErrSessionNotFound would lose the "lease expired"
		// vs. "never existed" distinction admin telemetry relies on.
		if !errors.Is(err, auth.ErrSessionExpired) {
			t.Fatalf("expected ErrSessionExpired, got %v", err)
		}
	})

	t.Run("Delete_UnknownID_IsIdempotent", func(t *testing.T) {
		s := factory(t)
		if err := s.Delete(context.Background(), "never-existed"); err != nil {
			t.Fatalf("Delete on absent id must be nil error, got %v", err)
		}
	})

	t.Run("Delete_PresentSession_Removes", func(t *testing.T) {
		s := factory(t)
		sess := makeSession("del-1", "user-1")
		_ = s.Put(context.Background(), sess)
		if err := s.Delete(context.Background(), "del-1"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		_, err := s.Get(context.Background(), "del-1")
		if !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("expected ErrSessionNotFound after Delete, got %v", err)
		}
	})

	t.Run("DeleteAllForUser_RemovesEverySession", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.Put(ctx, makeSession("s-A", "user-bulk"))
		_ = s.Put(ctx, makeSession("s-B", "user-bulk"))
		if err := s.DeleteAllForUser(ctx, "user-bulk"); err != nil {
			t.Fatalf("DeleteAllForUser: %v", err)
		}
		if _, err := s.Get(ctx, "s-A"); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("s-A survived: %v", err)
		}
		if _, err := s.Get(ctx, "s-B"); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("s-B survived: %v", err)
		}
	})

	t.Run("DeleteAllForUser_UnknownUser_IsNoop", func(t *testing.T) {
		s := factory(t)
		if err := s.DeleteAllForUser(context.Background(), "no-sessions-for-this-user"); err != nil {
			t.Fatalf("DeleteAllForUser must be no-op on unknown user, got %v", err)
		}
	})

	t.Run("ListForUser_ReturnsMetaSansData", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.Put(ctx, makeSession("list-1", "user-list"))
		_ = s.Put(ctx, makeSession("list-2", "user-list"))
		got, err := s.ListForUser(ctx, "user-list")
		if err != nil {
			t.Fatalf("ListForUser: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 sessions, got %d", len(got))
		}
		// SessionMeta intentionally omits Data; that is a struct-level
		// invariant rather than a behavioural one. We assert IDs.
		seen := map[string]bool{}
		for _, m := range got {
			seen[m.ID] = true
		}
		if !seen["list-1"] || !seen["list-2"] {
			t.Fatalf("ListForUser missing entries: %v", seen)
		}
	})
}

// LoginThrottlerFactory returns a fresh throttler per sub-test.
type LoginThrottlerFactory func(t *testing.T) contract.LoginThrottler

// RunLoginThrottlerContractTests exercises [contract.LoginThrottler].
func RunLoginThrottlerContractTests(t *testing.T, factory LoginThrottlerFactory) {
	t.Helper()

	t.Run("Allow_FreshKey_ReturnsTrue", func(t *testing.T) {
		th := factory(t)
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		if !th.Allow(r, "fresh-key") {
			t.Fatal("expected Allow=true for fresh key")
		}
	})

	t.Run("RecordSuccess_DoesNotPanic", func(t *testing.T) {
		th := factory(t)
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("RecordSuccess panicked: %v", rec)
			}
		}()
		th.RecordSuccess(r, "any-key")
	})

	t.Run("RecordFailure_DoesNotPanic", func(t *testing.T) {
		th := factory(t)
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("RecordFailure panicked: %v", rec)
			}
		}()
		th.RecordFailure(r, "any-key")
	})

	t.Run("NilRequest_DoesNotPanic", func(t *testing.T) {
		th := factory(t)
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("throttler panicked on nil *http.Request: %v", rec)
			}
		}()
		// Contract.LoginThrottler does not document nil-tolerance; we
		// only check the no-op throttler (and any well-behaved variant)
		// does not panic. Implementations that require r are free to
		// return false / no-op silently.
		_ = th.Allow(nil, "no-req")
		th.RecordFailure(nil, "no-req")
		th.RecordSuccess(nil, "no-req")
	})

	// Optional capability: a throttler implementing contract.LoginDelayer
	// must report no delay for a fresh key and tolerate a nil request.
	t.Run("LoginDelayer_FreshKey_NoDelay", func(t *testing.T) {
		th := factory(t)
		d, ok := th.(contract.LoginDelayer)
		if !ok {
			t.Skip("throttler does not implement contract.LoginDelayer")
		}
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		if got := d.Delay(r, "fresh-delay-key"); got != 0 {
			t.Fatalf("Delay for fresh key = %v, want 0", got)
		}
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("Delay panicked on nil *http.Request: %v", rec)
			}
		}()
		_ = d.Delay(nil, "no-req")
	})

	// Optional capability: a throttler implementing contract.LoginReserver
	// admits a fresh key, counts each reservation, and clears the key on
	// RecordSuccess.
	t.Run("LoginReserver_FreshKey_Reserves", func(t *testing.T) {
		th := factory(t)
		rs, ok := th.(contract.LoginReserver)
		if !ok {
			t.Skip("throttler does not implement contract.LoginReserver")
		}
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		if within, delay := rs.Reserve(r, "fresh-reserve-key"); !within || delay != 0 {
			t.Fatalf("Reserve for fresh key = (%v, %v), want (true, 0)", within, delay)
		}
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("Reserve panicked on nil *http.Request: %v", rec)
			}
		}()
		_, _ = rs.Reserve(nil, "no-req")
	})
}
