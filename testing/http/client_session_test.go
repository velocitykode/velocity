package http

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
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

// stubUserStore satisfies auth.UserStore for scheme construction. No
// session helper below resolves a user, so every method returns a zero value.
type stubUserStore struct{}

func (stubUserStore) FindByIDCtx(ctx context.Context, id interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (stubUserStore) FindByID(id interface{}) (auth.Authenticatable, error) { return nil, nil }
func (stubUserStore) FindByCredentialsCtx(ctx context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (stubUserStore) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (stubUserStore) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	return false
}
func (stubUserStore) UpdateRememberTokenCtx(ctx context.Context, user auth.Authenticatable, token string) error {
	return nil
}
func (stubUserStore) UpdateRememberToken(user auth.Authenticatable, token string) error {
	return nil
}

// newSessionTestScheme builds a real session scheme backed by a cookie store and
// an AES-256-GCM encryptor, the same shape production wiring produces.
func newSessionTestScheme(t *testing.T) (*schemes.SessionScheme, crypto.Encryptor) {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	scheme, err := schemes.NewSessionScheme(stubUserStore{}, auth.SessionConfig{
		Name:         "vel_session",
		IdleLifetime: 3600,
		Path:         "/",
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	return scheme, enc
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
			scheme, _ := newSessionTestScheme(t)
			client := NewTestClient(t, noopHandler())

			client.WithSession(scheme, tt.seed)
			client.AssertSessionHas(scheme, tt.key, tt.expect)
		})
	}
}

func TestClient_WithSession_AssertSessionMissing(t *testing.T) {
	scheme, _ := newSessionTestScheme(t)
	client := NewTestClient(t, noopHandler())

	client.WithSession(scheme, map[string]any{"role": "admin"})

	client.AssertSessionHas(scheme, "role", "admin")
	client.AssertSessionMissing(scheme, "nonexistent")
}

func TestClient_AssertSessionHas_Mismatch_Fails(t *testing.T) {
	tests := []struct {
		name    string
		assert  func(c *TestClient, scheme *schemes.SessionScheme)
		wantErr bool
	}{
		{
			name:    "present key correct value passes",
			assert:  func(c *TestClient, g *schemes.SessionScheme) { c.AssertSessionHas(g, "role", "admin") },
			wantErr: false,
		},
		{
			name:    "present key wrong value fails",
			assert:  func(c *TestClient, g *schemes.SessionScheme) { c.AssertSessionHas(g, "role", "editor") },
			wantErr: true,
		},
		{
			name:    "missing key fails",
			assert:  func(c *TestClient, g *schemes.SessionScheme) { c.AssertSessionHas(g, "ghost", "x") },
			wantErr: true,
		},
		{
			name:    "present key fails AssertSessionMissing",
			assert:  func(c *TestClient, g *schemes.SessionScheme) { c.AssertSessionMissing(g, "role") },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, _ := newSessionTestScheme(t)
			// Seed the session into the client jar with a recording T, then run
			// the assertion under test against the same client. Seeding does not
			// fail, so any recorded error comes from the assertion.
			mt := &sessionMockT{}
			client := NewTestClient(mt, noopHandler())
			client.WithSession(scheme, map[string]any{"role": "admin"})

			tt.assert(client, scheme)

			if got := len(mt.errors) > 0; got != tt.wantErr {
				t.Errorf("wantErr=%v, got errors=%v", tt.wantErr, mt.errors)
			}
		})
	}
}

// flashErrorsHandler flashes bag as validation errors into the session inside
// the scheme's session middleware, which saves it on the response, modelling
// router.Context.FlashErrors on a redirect back.
func flashErrorsHandler(t *testing.T, scheme *schemes.SessionScheme, bag map[string]any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := scheme.SessionMiddleware()(func(c *router.Context) error {
			scheme.Session(c.Request).Flash(router.FlashErrorsKey, bag)
			return nil
		})(router.NewContext(w, r))
		if err != nil {
			t.Errorf("session middleware: %v", err)
		}
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
			scheme, _ := newSessionTestScheme(t)
			client := NewTestClient(t, flashErrorsHandler(t, scheme, bag))
			resp := client.Get("/")

			mt := &sessionMockT{}
			resp.t = mt
			resp.AssertSessionHasErrors(scheme, tt.fields...)

			if got := len(mt.errors) > 0; got != tt.wantErr {
				t.Errorf("wantErr=%v, got errors=%v", tt.wantErr, mt.errors)
			}
		})
	}
}

func TestResponse_AssertSessionHasErrors_NoSession_Fails(t *testing.T) {
	scheme, _ := newSessionTestScheme(t)
	// Handler writes nothing, so the response saved no session to read.
	client := NewTestClient(t, noopHandler())
	resp := client.Get("/")

	mt := &sessionMockT{}
	resp.t = mt
	resp.AssertSessionHasErrors(scheme, "email")

	if len(mt.errors) == 0 {
		t.Errorf("expected a failure when the response saved no session")
	}
}

func TestResponse_AssertSessionHasErrors_NoScheme_Fails(t *testing.T) {
	scheme, _ := newSessionTestScheme(t)
	client := NewTestClient(t, flashErrorsHandler(t, scheme, map[string]any{"email": "required"}))
	resp := client.Get("/")

	mt := &sessionMockT{}
	resp.t = mt
	resp.AssertSessionHasErrors(nil, "email")

	if len(mt.errors) == 0 {
		t.Errorf("expected a failure when no scheme is given")
	}
}

func TestResponse_AssertSessionHasErrors_WrongKey_Fails(t *testing.T) {
	scheme, _ := newSessionTestScheme(t)

	// A scheme under a different key cannot open the session cookie, so the
	// errors must read as absent rather than partially trusted.
	other, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("z", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	otherScheme, err := schemes.NewSessionScheme(stubUserStore{}, auth.SessionConfig{
		Name:         "vel_session",
		IdleLifetime: 3600,
		Path:         "/",
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}, other)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}

	client := NewTestClient(t, flashErrorsHandler(t, scheme, map[string]any{"email": "required"}))
	resp := client.Get("/")

	mt := &sessionMockT{}
	resp.t = mt
	resp.AssertSessionHasErrors(otherScheme, "email")

	if len(mt.errors) == 0 {
		t.Errorf("expected a failure when the session is opened under the wrong key")
	}
}
