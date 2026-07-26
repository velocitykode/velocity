// Package ormauth provides an ORM-backed [auth.UserProvider].
//
// It exists as a separate leaf because the import direction is fixed:
// auth must never import orm (they are independent subsystems today, and
// auth is pulled in by router-side packages that must not drag the query
// engine). ormauth sits below both and imports each of them, so no cycle
// is created.
//
// # Why a registry
//
// Velocity's ORM is generic: every query entry point resolves its table
// and column set from a compile-time Go type (Model[T], Query[T]). The
// framework ships inside the application's binary but cannot name the
// application's own model type, and configuration only carries a string
// (AUTH_MODEL). A string cannot be turned into a Go type without a
// registry that the application itself populates, so the application
// registers its model under the name its configuration uses (Register
// returns an error; MustRegister is the init()-friendly wrapper):
//
//	func init() {
//	    ormauth.MustRegister("Admin", ormauth.Factory[models.Admin]())
//	}
//
// with AUTH_MODEL=Admin. A name that was never registered is a hard
// startup error naming the model and listing what is registered; it is
// never silently downgraded to the users table.
//
// # Default model
//
// The name "User" (the AUTH_MODEL default) is pre-registered by this
// package to [User], a model whose table and column set reproduce the
// framework's historical hardcoded shape (id, name, email, password,
// remember_token on users). An application that has always relied on the
// default keeps working with no registration of its own. Registering
// "User" again from application code overrides the built-in.
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
//	ormauth.MustRegister("Admin", ormauth.Factory[models.Admin](
//	    ormauth.WithIdentifierColumn("username"),
//	    ormauth.WithRememberTokenColumn("recall_token"),
//	))
//
// Because the remember token is persisted through the ORM's map-based
// update path, the model must also declare a mass-assignment policy that
// permits that column (Fillable, Guarded, or AllowAllColumns). Models
// that declare no policy at all are rejected at startup rather than
// failing on the first remember-me login.
package ormauth
