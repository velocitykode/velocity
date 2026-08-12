package velocity

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/stores/ormauth"
	"github.com/velocitykode/velocity/orm"
)

// TestSetAuthModel_InstallsApplicationModel covers the supported path: an
// application declares its user model through the root package, without
// naming the user store package at all.
func TestSetAuthModel_InstallsApplicationModel(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)
	services := &app.Services{Auth: manager}

	if err := SetAuthModel[wiringAdmin](services,
		WithAuthIdentifierColumn("username"),
		WithAuthPasswordColumn("pass_hash"),
		WithAuthRememberTokenColumn("recall_token"),
	); err != nil {
		t.Fatalf("SetAuthModel: %v", err)
	}

	userStore, ok := manager.DefaultUserStore().(*ormauth.Store[wiringAdmin])
	if !ok {
		t.Fatalf("installed user store is %T, want the application model", manager.DefaultUserStore())
	}
	if got := userStore.Options().IdentifierColumn; got != "username" {
		t.Errorf("IdentifierColumn = %q, want username", got)
	}
}

// TestSetAuthModel_InheritsManagerHasher proves the operator-configured
// bcrypt cost survives the swap rather than resetting to the user store's own
// default.
func TestSetAuthModel_InheritsManagerHasher(t *testing.T) {
	manager := initAuth(auth.Config{BcryptCost: 12}, auth.SessionConfig{}, nil, nil)
	services := &app.Services{Auth: manager}

	if err := SetAuthModel[ormauth.User](services); err != nil {
		t.Fatalf("SetAuthModel: %v", err)
	}

	userStore := manager.DefaultUserStore().(*ormauth.Store[ormauth.User])
	hasher, ok := userStore.Options().Hasher.(*auth.BcryptHasher)
	if !ok {
		t.Fatalf("hasher is %T, want the manager's *auth.BcryptHasher", userStore.Options().Hasher)
	}
	if got := hasher.Cost(); got != 12 {
		t.Errorf("bcrypt cost = %d, want the configured 12", got)
	}
}

// TestSetAuthModel_CallerOptionsWinOverInheritedHasher pins the option
// order: the inherited hasher is applied first so an explicit one replaces
// it rather than being replaced by it.
func TestSetAuthModel_CallerOptionsWinOverInheritedHasher(t *testing.T) {
	manager := initAuth(auth.Config{BcryptCost: 12}, auth.SessionConfig{}, nil, nil)
	services := &app.Services{Auth: manager}

	explicit := auth.NewBcryptHasher(10)
	if err := SetAuthModel[ormauth.User](services, ormauth.WithHasher(explicit)); err != nil {
		t.Fatalf("SetAuthModel: %v", err)
	}

	userStore := manager.DefaultUserStore().(*ormauth.Store[ormauth.User])
	if userStore.Options().Hasher != auth.Hasher(explicit) {
		t.Error("an explicitly supplied hasher was overridden by the inherited one")
	}
}

// TestSetAuthModel_RejectsUnmappableModel proves validation happens before
// installation: a model the user store cannot map is a boot error naming the
// problem, and the previously installed user store is left in place.
func TestSetAuthModel_RejectsUnmappableModel(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)
	services := &app.Services{Auth: manager}
	before := manager.DefaultUserStore()

	err := SetAuthModel[noPolicyModel](services)
	if err == nil {
		t.Fatal("a model with no mass-assignment policy was installed")
	}
	if manager.DefaultUserStore() != before {
		t.Error("a failed SetAuthModel replaced the installed user store")
	}
}

// TestSetAuthModel_WithoutAuthConfigured covers an app whose schemes were
// never built.
func TestSetAuthModel_WithoutAuthConfigured(t *testing.T) {
	if err := SetAuthModel[ormauth.User](&app.Services{}); !errors.Is(err, ErrAuthNotConfigured) {
		t.Errorf("err = %v, want ErrAuthNotConfigured", err)
	}
	if err := SetAuthModel[ormauth.User](nil); !errors.Is(err, ErrAuthNotConfigured) {
		t.Errorf("nil services: err = %v, want ErrAuthNotConfigured", err)
	}
}

// TestORMUserStore_BuildsWithoutInstalling covers the escape hatch used
// when the caller wants the user store itself.
func TestORMUserStore_BuildsWithoutInstalling(t *testing.T) {
	manager := initAuth(auth.Config{}, auth.SessionConfig{}, nil, nil)
	before := manager.DefaultUserStore()

	userStore := ORMUserStore[wiringAdmin](
		WithAuthIdentifierColumn("username"),
		WithAuthPasswordColumn("pass_hash"),
		WithAuthRememberTokenColumn("recall_token"),
		WithAuthCredentialsKey("login"),
	)
	if err := userStore.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := userStore.Options().CredentialsKey; got != "login" {
		t.Errorf("CredentialsKey = %q, want login", got)
	}
	if manager.DefaultUserStore() != before {
		t.Error("ORMUserStore installed the user store; it must only build one")
	}
}

// noPolicyModel declares neither Assignable nor Protected, so the ORM rejects
// every map-based write against it - including the remember-token update.
type noPolicyModel struct {
	orm.IDInt[noPolicyModel]

	Email         string `orm:"column:email"`
	Password      string `orm:"column:password"`
	RememberToken string `orm:"column:remember_token"`
}
