package velocity

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/providers/ormauth"
	"github.com/velocitykode/velocity/orm"
)

// TestInitAuth_ResolvesConfiguredModel covers the happy path: the default
// AUTH_MODEL resolves through the ormauth registry and lands as a provider.
func TestInitAuth_ResolvesConfiguredModel(t *testing.T) {
	manager, err := initAuth(auth.Config{
		Providers: map[string]auth.ProviderConfig{
			"users": {Driver: "orm", Model: ormauth.DefaultModelName},
		},
	}, auth.SessionConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("initAuth: %v", err)
	}
	if _, err := manager.Provider("users"); err != nil {
		t.Fatalf("provider not registered: %v", err)
	}
}

// TestInitAuth_EmptyModelUsesDefault covers a ProviderConfig whose Model was
// never set.
func TestInitAuth_EmptyModelUsesDefault(t *testing.T) {
	manager, err := initAuth(auth.Config{
		Providers: map[string]auth.ProviderConfig{
			"users": {Driver: "orm"},
		},
	}, auth.SessionConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("initAuth: %v", err)
	}
	if _, err := manager.Provider("users"); err != nil {
		t.Fatalf("provider not registered: %v", err)
	}
}

// TestInitAuth_UnregisteredModelIsAStartupError is the regression test for
// the dead AUTH_MODEL knob: a model name nobody registered used to produce
// byte-identical SQL against the users table. It must now abort boot,
// naming the model.
func TestInitAuth_UnregisteredModelIsAStartupError(t *testing.T) {
	_, err := initAuth(auth.Config{
		Providers: map[string]auth.ProviderConfig{
			"users": {Driver: "orm", Model: "Nonexistent"},
		},
	}, auth.SessionConfig{}, nil, nil)
	if err == nil {
		t.Fatal("an unregistered AUTH_MODEL booted successfully")
	}
	if !strings.Contains(err.Error(), "Nonexistent") {
		t.Errorf("error does not name the model: %v", err)
	}
	if !strings.Contains(err.Error(), `auth provider "users"`) {
		t.Errorf("error does not name the provider: %v", err)
	}
}

// TestInitAuth_UnknownDriverIsAStartupError covers the missing default case
// in the provider switch: an unrecognised driver used to register nothing
// and surface one loop later as a guard-level "provider not found" warning,
// naming the guard rather than the driver typo.
func TestInitAuth_UnknownDriverIsAStartupError(t *testing.T) {
	_, err := initAuth(auth.Config{
		Providers: map[string]auth.ProviderConfig{
			"users": {Driver: "eloquent", Model: ormauth.DefaultModelName},
		},
	}, auth.SessionConfig{}, nil, nil)
	if err == nil {
		t.Fatal("an unknown provider driver booted successfully")
	}
	if !strings.Contains(err.Error(), `unknown driver "eloquent"`) {
		t.Errorf("error does not name the driver: %v", err)
	}
	if !strings.Contains(err.Error(), `auth provider "users"`) {
		t.Errorf("error does not name the provider: %v", err)
	}
}

// TestInitAuth_ProviderErrorsAreDeterministic pins the sorted iteration: Go
// randomises map order, so without it a config with two broken providers
// would report a different one on every boot.
func TestInitAuth_ProviderErrorsAreDeterministic(t *testing.T) {
	cfg := auth.Config{
		Providers: map[string]auth.ProviderConfig{
			"admins": {Driver: "bogus-a"},
			"users":  {Driver: "bogus-b"},
		},
	}

	for i := 0; i < 20; i++ {
		_, err := initAuth(cfg, auth.SessionConfig{}, nil, nil)
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), `auth provider "admins"`) {
			t.Fatalf("iteration %d reported %v, want the alphabetically first provider", i, err)
		}
	}
}

// wiringAdmin stands in for an application's own auth model: a name that
// only exists because the application registered it.
type wiringAdmin struct {
	orm.IDInt[wiringAdmin]

	Username    string `orm:"column:username"`
	PassHash    string `orm:"column:pass_hash"`
	RecallToken string `orm:"column:recall_token"`
}

// Fillable declares the mass-assignment policy the remember-token write
// needs.
func (wiringAdmin) Fillable() []string {
	return []string{"username", "pass_hash", "recall_token"}
}

// TestInitAuth_HonorsRegisteredNonDefaultModel closes the loop the old
// provider never did: AUTH_MODEL=Admin must select the application's model,
// not silently resolve to the users table. The provider that comes back is
// typed on the registered model, which is the assertion that would have
// failed before - the old provider stored the model name in a field it
// never read.
func TestInitAuth_HonorsRegisteredNonDefaultModel(t *testing.T) {
	if err := ormauth.Register("Admin", ormauth.Factory[wiringAdmin](
		ormauth.WithIdentifierColumn("username"),
		ormauth.WithPasswordColumn("pass_hash"),
		ormauth.WithRememberTokenColumn("recall_token"),
	)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { ormauth.Unregister("Admin") })

	manager, err := initAuth(auth.Config{
		Providers: map[string]auth.ProviderConfig{
			"admins": {Driver: "orm", Model: "Admin"},
		},
	}, auth.SessionConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("initAuth: %v", err)
	}

	provider, err := manager.Provider("admins")
	if err != nil {
		t.Fatalf("provider not registered: %v", err)
	}
	typed, ok := provider.(*ormauth.Provider[wiringAdmin])
	if !ok {
		t.Fatalf("provider is %T, want a provider typed on the registered model", provider)
	}
	if opts := typed.Options(); opts.IdentifierColumn != "username" {
		t.Errorf("IdentifierColumn = %q, want the registered value", opts.IdentifierColumn)
	}
}

// TestInitAuth_ThreadsManagerHasherToProvider proves the operator-configured
// bcrypt cost reaches the provider, rather than the provider falling back to
// its own default.
func TestInitAuth_ThreadsManagerHasherToProvider(t *testing.T) {
	manager, err := initAuth(auth.Config{
		BcryptCost: 12,
		Providers: map[string]auth.ProviderConfig{
			"users": {Driver: "orm", Model: ormauth.DefaultModelName},
		},
	}, auth.SessionConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("initAuth: %v", err)
	}

	provider, err := manager.Provider("users")
	if err != nil {
		t.Fatalf("provider not registered: %v", err)
	}
	hasher := provider.(*ormauth.Provider[ormauth.User]).Options().Hasher
	bcryptHasher, ok := hasher.(*auth.BcryptHasher)
	if !ok {
		t.Fatalf("provider hasher is %T, want the manager's *auth.BcryptHasher", hasher)
	}
	if got := bcryptHasher.Cost(); got != 12 {
		t.Errorf("provider bcrypt cost = %d, want the configured 12", got)
	}
}
