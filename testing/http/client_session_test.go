package http

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/guards"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
)

// sessionMockT records Errorf calls so failure paths can be asserted without
// failing the enclosing test. Kept local to this file (file-disjoint from the
// rest of the package's test helpers).
type sessionMockT struct {
	errors []string
}

func (m *sessionMockT) Helper() {}
func (m *sessionMockT) Errorf(format string, args ...interface{}) {
	m.errors = append(m.errors, fmt.Sprintf(format, args...))
}

// stubUserProvider satisfies auth.UserProvider for guard construction. No
// session helper below resolves a user, so every method returns a zero value.
type stubUserProvider struct{}

func (stubUserProvider) FindByIDCtx(ctx context.Context, id interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (stubUserProvider) FindByID(id interface{}) (auth.Authenticatable, error) { return nil, nil }
func (stubUserProvider) FindByCredentialsCtx(ctx context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (stubUserProvider) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (stubUserProvider) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	return false
}
func (stubUserProvider) UpdateRememberTokenCtx(ctx context.Context, user auth.Authenticatable, token string) error {
	return nil
}
func (stubUserProvider) UpdateRememberToken(user auth.Authenticatable, token string) error {
	return nil
}

// newSessionTestGuard builds a real session guard backed by a cookie store and
// an AES-256-GCM encryptor, the same shape production wiring produces.
func newSessionTestGuard(t *testing.T) (*guards.SessionGuard, crypto.Encryptor) {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	guard, err := guards.NewSessionGuard(stubUserProvider{}, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 3600,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionGuard: %v", err)
	}
	return guard, enc
}

// noopHandler is a router that does nothing. The session-data assertions are
// client-level and read the client's own cookie jar, so they need no handler
// behaviour: WithSession seeds the jar directly.
func noopHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
}

func TestClient_WithSession_SeedsReadableSession(t *testing.T) {
	tests := []struct {
		name   string
		seed   map[string]any
		key    string
		expect any
	}{
		{name: "string value", seed: map[string]any{"role": "admin"}, key: "role", expect: "admin"},
		{name: "bool value", seed: map[string]any{"flagged": true}, key: "flagged", expect: true},
		// Numbers round-trip through JSON in the cookie store, so the seeded
		// int reads back as float64.
		{name: "int reads back as float64", seed: map[string]any{"count": 7}, key: "count", expect: float64(7)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard, _ := newSessionTestGuard(t)
			client := NewTestClient(t, noopHandler())

			client.WithSession(guard, tt.seed)
			client.AssertSessionHas(guard, tt.key, tt.expect)
		})
	}
}

func TestClient_WithSession_AssertSessionMissing(t *testing.T) {
	guard, _ := newSessionTestGuard(t)
	client := NewTestClient(t, noopHandler())

	client.WithSession(guard, map[string]any{"role": "admin"})

	client.AssertSessionHas(guard, "role", "admin")
	client.AssertSessionMissing(guard, "nonexistent")
}

func TestClient_AssertSessionHas_Mismatch_Fails(t *testing.T) {
	tests := []struct {
		name    string
		assert  func(c *TestClient, guard *guards.SessionGuard)
		wantErr bool
	}{
		{
			name:    "present key correct value passes",
			assert:  func(c *TestClient, g *guards.SessionGuard) { c.AssertSessionHas(g, "role", "admin") },
			wantErr: false,
		},
		{
			name:    "present key wrong value fails",
			assert:  func(c *TestClient, g *guards.SessionGuard) { c.AssertSessionHas(g, "role", "editor") },
			wantErr: true,
		},
		{
			name:    "missing key fails",
			assert:  func(c *TestClient, g *guards.SessionGuard) { c.AssertSessionHas(g, "ghost", "x") },
			wantErr: true,
		},
		{
			name:    "present key fails AssertSessionMissing",
			assert:  func(c *TestClient, g *guards.SessionGuard) { c.AssertSessionMissing(g, "role") },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard, _ := newSessionTestGuard(t)
			// Seed the session into the client jar with a recording T, then run
			// the assertion under test against the same client. Seeding does not
			// fail, so any recorded error comes from the assertion.
			mt := &sessionMockT{}
			client := NewTestClient(mt, noopHandler())
			client.WithSession(guard, map[string]any{"role": "admin"})

			tt.assert(client, guard)

			if got := len(mt.errors) > 0; got != tt.wantErr {
				t.Errorf("wantErr=%v, got errors=%v", tt.wantErr, mt.errors)
			}
		})
	}
}

// flashErrorsHandler writes a sealed "_velocity_errors" flash cookie using the
// real router.SealFlash + router.FlashCookie write path, modelling a redirect
// back with validation errors.
func flashErrorsHandler(t *testing.T, enc crypto.Encryptor, bag map[string]any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sealed, err := router.SealFlash(enc, router.FlashErrorsCookie, bag)
		if err != nil {
			t.Errorf("SealFlash: %v", err)
			return
		}
		http.SetCookie(w, router.FlashCookie(router.FlashErrorsCookie, sealed, 300, false))
	})
}

func TestResponse_AssertSessionHasErrors(t *testing.T) {
	bag := map[string]any{"email": "The email field is required.", "name": "The name field is required."}

	tests := []struct {
		name    string
		fields  []string
		wantErr bool
	}{
		{name: "single present field", fields: []string{"email"}, wantErr: false},
		{name: "multiple present fields", fields: []string{"email", "name"}, wantErr: false},
		{name: "absent field fails", fields: []string{"password"}, wantErr: true},
		{name: "mixed present and absent fails", fields: []string{"email", "password"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, enc := newSessionTestGuard(t)
			client := NewTestClient(t, flashErrorsHandler(t, enc, bag))
			resp := client.Get("/")

			mt := &sessionMockT{}
			resp.t = mt
			resp.AssertSessionHasErrors(enc, tt.fields...)

			if got := len(mt.errors) > 0; got != tt.wantErr {
				t.Errorf("wantErr=%v, got errors=%v", tt.wantErr, mt.errors)
			}
		})
	}
}

func TestResponse_AssertSessionHasErrors_NoCookie_Fails(t *testing.T) {
	_, enc := newSessionTestGuard(t)
	// Handler writes nothing, so there is no flash cookie to decrypt.
	client := NewTestClient(t, noopHandler())
	resp := client.Get("/")

	mt := &sessionMockT{}
	resp.t = mt
	resp.AssertSessionHasErrors(enc, "email")

	if len(mt.errors) == 0 {
		t.Errorf("expected a failure when no flash cookie is present")
	}
}

func TestResponse_AssertSessionHasErrors_NoEncryptor_Fails(t *testing.T) {
	_, enc := newSessionTestGuard(t)
	// A nil encryptor is passed, so there is no key to open the bag and the
	// assertion fails clean.
	client := NewTestClient(t, flashErrorsHandler(t, enc, map[string]any{"email": "required"}))
	resp := client.Get("/")

	mt := &sessionMockT{}
	resp.t = mt
	resp.AssertSessionHasErrors(nil, "email")

	if len(mt.errors) == 0 {
		t.Errorf("expected a failure when no encryptor is set")
	}
}

func TestResponse_AssertSessionHasErrors_WrongKey_Fails(t *testing.T) {
	_, enc := newSessionTestGuard(t)

	// A different key cannot authenticate the sealed cookie, so the bag must
	// read as absent rather than partially trusted.
	other, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("z", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	client := NewTestClient(t, flashErrorsHandler(t, enc, map[string]any{"email": "required"}))
	resp := client.Get("/")

	mt := &sessionMockT{}
	resp.t = mt
	resp.AssertSessionHasErrors(other, "email")

	if len(mt.errors) == 0 {
		t.Errorf("expected a failure when decrypting with the wrong key")
	}
}
