// Package ormauth provides an ORM-backed [auth.UserProvider].
//
// It exists as a separate leaf because the import direction is fixed:
// auth must never import orm (they are independent subsystems today, and
// auth is pulled in by router-side packages that must not drag the query
// engine). ormauth sits below both and imports each of them, so no cycle
// is created.
//
// # Choosing the model
//
// Velocity's ORM is generic: every query entry point resolves its table
// and column set from a compile-time Go type (Model[T], Query[T]). The
// auth model is therefore supplied as a type parameter, in code, not as a
// configuration string - Go cannot turn the name "Admin" into a type, and
// a linker that sees no reference to a type is free to discard it.
//
// An application installs its own model from a service provider:
//
//	func (p *AuthProvider) Boot(s *app.Services) error {
//	    provider := ormauth.New[models.Admin](
//	        ormauth.WithIdentifierColumn("username"),
//	    )
//	    if err := provider.Validate(); err != nil {
//	        return err
//	    }
//	    s.Auth.SetProvider(provider)
//	    return nil
//	}
//
// Swapping the model is editing the type parameter: a typo is a compile
// error rather than a boot-time failure, and the IDE completes the
// available options. auth.Manager.SetProvider re-points every registered
// guard, so this works regardless of whether it runs before or after the
// framework installs its default.
//
// # Default model
//
// velocity.New installs New[User] against the users table, reproducing
// the column set the framework used to hardcode (id, name, email,
// password, remember_token). An application that configures nothing keeps
// working unchanged.
//
// # Model requirements
//
// A model is usable as an auth model when it either implements
// [auth.Authenticatable] itself (preferred - no reflection on the hot
// path) or exposes the identifier, password, and remember-token columns
// that this package maps onto that interface. Column names are options,
// so a model with no "name" column, or with "username" instead of
// "email", is configured rather than special-cased:
//
//	ormauth.New[models.Admin](
//	    ormauth.WithIdentifierColumn("username"),
//	    ormauth.WithPasswordColumn("pass_hash"),
//	    ormauth.WithRememberTokenColumn("recall_token"),
//	)
//
// Because the remember token is persisted through the ORM's map-based
// update path, the model must also declare a mass-assignment policy that
// permits that column (Fillable, Guarded, or AllowAllColumns). Models
// that declare no policy at all are rejected by [Provider.Validate]
// rather than failing on the first remember-me login.
package ormauth
