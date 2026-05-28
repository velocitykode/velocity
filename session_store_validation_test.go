package velocity

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/guards"
	"github.com/velocitykode/velocity/crypto"
)

// newTestSessionConfig returns a SessionConfig that passes Validate so the
// production-store gate is the only check the test can trip.
func newTestSessionStoreConfig() auth.SessionConfig {
	return auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// newAppWithSessionGuard builds a minimal *App carrying a *auth.Manager with
// a registered *guards.SessionGuard, suitable for exercising
// validateSessionStoreForProduction in isolation.
func newAppWithSessionGuard(t *testing.T, env string) *App {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	mgr := auth.NewManager()
	provider := &stubProvider{}
	guard, err := guards.NewSessionGuard(provider, newTestSessionStoreConfig(), enc)
	if err != nil {
		t.Fatalf("NewSessionGuard: %v", err)
	}
	mgr.RegisterGuard("web", guard)
	mgr.SetDefaultGuard("web")

	cfg := ConfigFromEnv()
	cfg.Env = env
	cfg.Session = newTestSessionStoreConfig()

	return &App{
		Services: &app.Services{
			Auth: mgr,
		},
		config: &cfg,
	}
}

// stubProvider is the minimum auth.UserProvider needed to construct a
// SessionGuard. None of its methods are invoked by the production-gate test.
type stubProvider struct{}

func (stubProvider) FindByID(interface{}) (auth.Authenticatable, error) { return nil, nil }
func (stubProvider) FindByIDCtx(context.Context, interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (stubProvider) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (stubProvider) FindByCredentialsCtx(context.Context, map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (stubProvider) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return false
}
func (stubProvider) UpdateRememberToken(auth.Authenticatable, string) error { return nil }
func (stubProvider) UpdateRememberTokenCtx(context.Context, auth.Authenticatable, string) error {
	return nil
}

// TestValidateSessionStoreForProduction_RefusesCookieStoreWithoutOptIn is the
// H-04 regression test: APP_ENV=production with the default CookieStore and
// no ServerSessionStore MUST fail Bootstrap unless the operator opted in.
func TestValidateSessionStoreForProduction_RefusesCookieStoreWithoutOptIn(t *testing.T) {
	a := newAppWithSessionGuard(t, "production")

	err := validateSessionStoreForProduction(a)
	if err == nil {
		t.Fatal("validateSessionStoreForProduction returned nil; expected ErrCookieStoreInProduction")
	}
	if !errors.Is(err, ErrCookieStoreInProduction) {
		t.Fatalf("expected ErrCookieStoreInProduction, got %v", err)
	}
}

// TestValidateSessionStoreForProduction_AllowsExplicitOptIn pins the escape
// hatch: operators who accept the single-host risk can set
// SessionConfig.AllowCookieStoreInProduction and Bootstrap proceeds.
func TestValidateSessionStoreForProduction_AllowsExplicitOptIn(t *testing.T) {
	a := newAppWithSessionGuard(t, "production")
	a.config.Session.AllowCookieStoreInProduction = true

	if err := validateSessionStoreForProduction(a); err != nil {
		t.Fatalf("validateSessionStoreForProduction with opt-in returned %v; expected nil", err)
	}
}

// TestValidateSessionStoreForProduction_AllowsServerStoreInstalled pins the
// happy path: when the operator wires a ServerSessionStore (via a provider's
// Boot hook, typically), Bootstrap proceeds.
func TestValidateSessionStoreForProduction_AllowsServerStoreInstalled(t *testing.T) {
	a := newAppWithSessionGuard(t, "production")
	mgr := a.Auth.(*auth.Manager)
	mgr.SetServerSessionStore(stubServerStore{})

	if err := validateSessionStoreForProduction(a); err != nil {
		t.Fatalf("validateSessionStoreForProduction with server store returned %v; expected nil", err)
	}
}

// TestValidateSessionStoreForProduction_SkipsNonProductionEnvs covers
// development and testing modes: the gate is production-only.
func TestValidateSessionStoreForProduction_SkipsNonProductionEnvs(t *testing.T) {
	for _, env := range []string{"development", "testing"} {
		t.Run(env, func(t *testing.T) {
			a := newAppWithSessionGuard(t, env)
			if err := validateSessionStoreForProduction(a); err != nil {
				t.Fatalf("env=%q: validateSessionStoreForProduction returned %v; expected nil", env, err)
			}
		})
	}
}

// stubServerStore is the no-op auth.ServerSessionStore used to flip the
// production-gate decision branch in tests.
type stubServerStore struct{}

func (stubServerStore) Get(_ context.Context, _ string) (*auth.StoredSession, error) {
	return nil, auth.ErrSessionNotFound
}
func (stubServerStore) Put(_ context.Context, _ *auth.StoredSession) error { return nil }
func (stubServerStore) Delete(_ context.Context, _ string) error           { return nil }
func (stubServerStore) DeleteAllForUser(_ context.Context, _ string) error { return nil }
func (stubServerStore) ListForUser(_ context.Context, _ string) ([]*auth.SessionMeta, error) {
	return nil, nil
}
