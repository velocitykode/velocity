package velocity

import (
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/providers/ormauth"
	"github.com/velocitykode/velocity/orm"
)

// wiringAdmin stands in for an application's own auth model: a different
// type, a different table, and column names that share nothing with the
// framework default.
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

// TestInitAuth_InstallsDefaultProvider covers the zero-configuration path:
// an app that sets nothing still authenticates, against the framework's
// built-in model.
func TestInitAuth_InstallsDefaultProvider(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)

	provider := manager.DefaultProvider()
	if provider == nil {
		t.Fatal("no default provider installed")
	}
	if _, ok := provider.(*ormauth.Provider[ormauth.User]); !ok {
		t.Fatalf("default provider is %T, want the built-in User model", provider)
	}
}

// TestInitAuth_ThreadsManagerHasherToProvider proves the operator-configured
// bcrypt cost reaches the provider rather than the provider falling back to
// its own default.
func TestInitAuth_ThreadsManagerHasherToProvider(t *testing.T) {
	manager := initAuth(auth.Config{BcryptCost: 12}, auth.SessionConfig{}, nil, nil)

	provider, ok := manager.DefaultProvider().(*ormauth.Provider[ormauth.User])
	if !ok {
		t.Fatalf("default provider is %T", manager.DefaultProvider())
	}
	hasher, ok := provider.Options().Hasher.(*auth.BcryptHasher)
	if !ok {
		t.Fatalf("provider hasher is %T, want the manager's *auth.BcryptHasher", provider.Options().Hasher)
	}
	if got := hasher.Cost(); got != 12 {
		t.Errorf("provider bcrypt cost = %d, want the configured 12", got)
	}
}

// TestSetProvider_SwapsTheAuthModel is the supported way to change which
// model authenticates: hand the manager a provider built on the app's own
// type. No configuration string, no registry, no name to typo.
func TestSetProvider_SwapsTheAuthModel(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)

	swapped := ormauth.New[wiringAdmin](
		ormauth.WithIdentifierColumn("username"),
		ormauth.WithPasswordColumn("pass_hash"),
		ormauth.WithRememberTokenColumn("recall_token"),
	)
	if err := swapped.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	manager.SetProvider(swapped)

	if _, ok := manager.DefaultProvider().(*ormauth.Provider[wiringAdmin]); !ok {
		t.Fatalf("provider after swap is %T, want the application model", manager.DefaultProvider())
	}
}

// TestSetProvider_RepointsRegisteredGuards is the ordering guarantee that
// makes the swap usable from a service provider: guards built during
// initAuth already hold the default provider, so SetProvider must reach
// them too. Without this, swapping the model would appear to work while
// every guard kept authenticating against the old one.
func TestSetProvider_RepointsRegisteredGuards(t *testing.T) {
	manager := auth.NewManager()
	guard := &recordingGuard{}
	manager.RegisterGuard("web", guard)

	replacement := ormauth.New[ormauth.User]()
	manager.SetProvider(replacement)

	if guard.provider == nil {
		t.Fatal("SetProvider did not reach an already-registered guard")
	}
	if guard.provider != auth.UserProvider(replacement) {
		t.Errorf("guard received %T, want the provider just installed", guard.provider)
	}
}

// TestSetProvider_IgnoresNil keeps a nil argument from silently
// uninstalling authentication.
func TestSetProvider_IgnoresNil(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)
	before := manager.DefaultProvider()

	manager.SetProvider(nil)

	if manager.DefaultProvider() != before {
		t.Error("SetProvider(nil) replaced the installed provider")
	}
}

// TestRegisterProvider_EscapeHatch covers the named-provider API kept for
// the uncommon app that authenticates two identity stores in one process.
// It is deliberately absent from configuration.
func TestRegisterProvider_EscapeHatch(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)

	second := ormauth.New[wiringAdmin](
		ormauth.WithIdentifierColumn("username"),
		ormauth.WithPasswordColumn("pass_hash"),
		ormauth.WithRememberTokenColumn("recall_token"),
	)
	manager.RegisterProvider("admins", second)

	got, err := manager.Provider("admins")
	if err != nil {
		t.Fatalf("Provider(admins): %v", err)
	}
	if _, ok := got.(*ormauth.Provider[wiringAdmin]); !ok {
		t.Fatalf("named provider is %T", got)
	}

	// The default is untouched: the escape hatch does not fan out.
	if _, ok := manager.DefaultProvider().(*ormauth.Provider[ormauth.User]); !ok {
		t.Errorf("RegisterProvider changed the default provider to %T", manager.DefaultProvider())
	}
}

// recordingGuard captures the provider handed to it by SetProvider.
type recordingGuard struct {
	provider auth.UserProvider
}

func (g *recordingGuard) SetProvider(p auth.UserProvider)                 { g.provider = p }
func (g *recordingGuard) Check(*http.Request) bool                        { return false }
func (g *recordingGuard) User(*http.Request) auth.Authenticatable         { return nil }
func (g *recordingGuard) ID(*http.Request) interface{}                    { return nil }
func (g *recordingGuard) Logout(http.ResponseWriter, *http.Request) error { return nil }
func (g *recordingGuard) Login(http.ResponseWriter, *http.Request, auth.Authenticatable, ...bool) error {
	return nil
}
func (g *recordingGuard) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (g *recordingGuard) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}
