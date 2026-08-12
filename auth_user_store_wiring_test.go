package velocity

import (
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/stores/ormauth"
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

// TestInitAuth_InstallsDefaultUserStore covers the zero-configuration path:
// an app that sets nothing still authenticates, against the framework's
// built-in model.
func TestInitAuth_InstallsDefaultUserStore(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)

	userStore := manager.DefaultUserStore()
	if userStore == nil {
		t.Fatal("no default user store installed")
	}
	if _, ok := userStore.(*ormauth.Store[ormauth.User]); !ok {
		t.Fatalf("default user store is %T, want the built-in User model", userStore)
	}
}

// TestInitAuth_ThreadsManagerHasherToStore proves the operator-configured
// bcrypt cost reaches the user store rather than the user store falling back to
// its own default.
func TestInitAuth_ThreadsManagerHasherToStore(t *testing.T) {
	manager := initAuth(auth.Config{BcryptCost: 12}, auth.SessionConfig{}, nil, nil)

	userStore, ok := manager.DefaultUserStore().(*ormauth.Store[ormauth.User])
	if !ok {
		t.Fatalf("default user store is %T", manager.DefaultUserStore())
	}
	hasher, ok := userStore.Options().Hasher.(*auth.BcryptHasher)
	if !ok {
		t.Fatalf("provider hasher is %T, want the manager's *auth.BcryptHasher", userStore.Options().Hasher)
	}
	if got := hasher.Cost(); got != 12 {
		t.Errorf("provider bcrypt cost = %d, want the configured 12", got)
	}
}

// TestSetUserStore_SwapsTheAuthModel is the supported way to change which
// model authenticates: hand the manager a user store built on the app's own
// type. No configuration string, no registry, no name to typo.
func TestSetUserStore_SwapsTheAuthModel(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)

	swapped := ormauth.New[wiringAdmin](
		ormauth.WithIdentifierColumn("username"),
		ormauth.WithPasswordColumn("pass_hash"),
		ormauth.WithRememberTokenColumn("recall_token"),
	)
	if err := swapped.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	manager.SetUserStore(swapped)

	if _, ok := manager.DefaultUserStore().(*ormauth.Store[wiringAdmin]); !ok {
		t.Fatalf("provider after swap is %T, want the application model", manager.DefaultUserStore())
	}
}

// TestSetUserStore_RepointsRegisteredSchemes is the ordering guarantee that
// makes the swap usable from a module: schemes built during
// initAuth already hold the default user store, so SetUserStore must reach
// them too. Without this, swapping the model would appear to work while
// every scheme kept authenticating against the old one.
func TestSetUserStore_RepointsRegisteredSchemes(t *testing.T) {
	manager := auth.NewManager()
	scheme := &recordingScheme{}
	manager.RegisterScheme("web", scheme)

	replacement := ormauth.New[ormauth.User]()
	manager.SetUserStore(replacement)

	if scheme.userStore == nil {
		t.Fatal("SetUserStore did not reach an already-registered scheme")
	}
	if scheme.userStore != auth.UserStore(replacement) {
		t.Errorf("scheme received %T, want the user store just installed", scheme.userStore)
	}
}

// TestSetUserStore_IgnoresNil keeps a nil argument from silently
// uninstalling authentication.
func TestSetUserStore_IgnoresNil(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)
	before := manager.DefaultUserStore()

	manager.SetUserStore(nil)

	if manager.DefaultUserStore() != before {
		t.Error("SetUserStore(nil) replaced the installed provider")
	}
}

// TestRegisterUserStore_EscapeHatch covers the named-user store API kept for
// the uncommon app that authenticates two identity stores in one process.
// It is deliberately absent from configuration.
func TestRegisterUserStore_EscapeHatch(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)

	second := ormauth.New[wiringAdmin](
		ormauth.WithIdentifierColumn("username"),
		ormauth.WithPasswordColumn("pass_hash"),
		ormauth.WithRememberTokenColumn("recall_token"),
	)
	manager.RegisterUserStore("admins", second)

	got, err := manager.UserStore("admins")
	if err != nil {
		t.Fatalf("Store(admins): %v", err)
	}
	if _, ok := got.(*ormauth.Store[wiringAdmin]); !ok {
		t.Fatalf("named user store is %T", got)
	}

	// The default is untouched: the escape hatch does not fan out.
	if _, ok := manager.DefaultUserStore().(*ormauth.Store[ormauth.User]); !ok {
		t.Errorf("RegisterUserStore changed the default user store to %T", manager.DefaultUserStore())
	}
}

// recordingScheme captures the user store handed to it by SetUserStore.
type recordingScheme struct {
	userStore auth.UserStore
}

func (g *recordingScheme) SetUserStore(p auth.UserStore)                   { g.userStore = p }
func (g *recordingScheme) Check(*http.Request) bool                        { return false }
func (g *recordingScheme) User(*http.Request) auth.Authenticatable         { return nil }
func (g *recordingScheme) ID(*http.Request) interface{}                    { return nil }
func (g *recordingScheme) Logout(http.ResponseWriter, *http.Request) error { return nil }
func (g *recordingScheme) Login(http.ResponseWriter, *http.Request, auth.Authenticatable, ...bool) error {
	return nil
}
func (g *recordingScheme) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (g *recordingScheme) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}
