package velocity

import (
	"errors"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/providers/ormauth"
)

// AuthOption configures the ORM-backed user provider installed by
// [SetAuthModel] / [ORMUserProvider]. Aliased from the provider package so
// application code never has to import it.
type AuthOption = ormauth.Option

// Column-mapping options, re-exported so an application can name its own
// columns without importing the provider package. Defaults are "email",
// "password", and "remember_token".
var (
	// WithAuthIdentifierColumn sets the column a login credential is
	// matched against (email, username, ...). It is distinct from the
	// primary key, which is read from the model's ORM tags.
	WithAuthIdentifierColumn = ormauth.WithIdentifierColumn

	// WithAuthPasswordColumn sets the column holding the password hash.
	WithAuthPasswordColumn = ormauth.WithPasswordColumn

	// WithAuthRememberTokenColumn sets the column holding the remember-me
	// token.
	WithAuthRememberTokenColumn = ormauth.WithRememberTokenColumn

	// WithAuthCredentialsKey sets the credentials-map key read during a
	// login attempt when it differs from the identifier column (e.g. a
	// form posting "email" against a users.username column).
	WithAuthCredentialsKey = ormauth.WithCredentialsKey
)

// ErrAuthNotConfigured is returned by [SetAuthModel] when the application
// has no auth manager, which means velocity.New built no guards (AUTH_GUARD
// unset).
var ErrAuthNotConfigured = errors.New("velocity: auth is not configured (set AUTH_GUARD so guards are built)")

// SetAuthModel points authentication at the application's own model.
//
// This is the supported way to choose which model authenticates. The model
// is a type parameter rather than a configuration string because the ORM
// resolves its table from a compile-time Go type, and Go cannot turn a name
// into a type. Swapping the model is editing T, so a mistake is a compile
// error rather than a boot failure:
//
//	func (p *AppProvider) Register(s *velocity.Services) error {
//	    return velocity.SetAuthModel[models.User](s)
//	}
//
// A model whose columns differ from the defaults names them:
//
//	velocity.SetAuthModel[models.Admin](s,
//	    velocity.WithAuthIdentifierColumn("username"),
//	    velocity.WithAuthPasswordColumn("pass_hash"),
//	)
//
// Call it from a service provider's Register or Boot. velocity.New has
// already built the guards against the framework's built-in user model;
// installing a provider re-points every one of them, so ordering does not
// matter.
//
// The model is validated before installation, so a missing column or an
// absent mass-assignment policy is a boot error naming the problem rather
// than a failure on the first login. The password hasher is inherited from
// the auth manager, preserving the operator-configured bcrypt cost.
func SetAuthModel[T any](s *app.Services, opts ...AuthOption) error {
	manager := auth.FromServices(s)
	if manager == nil {
		return ErrAuthNotConfigured
	}

	provider := ORMUserProvider[T](append([]AuthOption{ormauth.WithHasher(manager.GetHasher())}, opts...)...)
	if err := provider.Validate(); err != nil {
		return err
	}

	manager.SetProvider(provider)
	return nil
}

// ORMUserProvider builds the ORM-backed user provider for model T without
// installing it. Use [SetAuthModel] unless you need the provider itself -
// to hand it to a second guard, say, or to inspect it in a test.
//
// The returned provider reports mapping failures through Validate; it does
// not fail here, so a caller that skips validation gets the error from the
// first query instead.
func ORMUserProvider[T any](opts ...AuthOption) *ormauth.Provider[T] {
	return ormauth.New[T](opts...)
}
