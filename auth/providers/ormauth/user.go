package ormauth

import "github.com/velocitykode/velocity/orm"

// User is the framework's default auth model, installed by velocity.New
// when an application does not call auth.Manager.SetUserStore with one of
// its own. Its table and column set reproduce the shape the user store
// previously hardcoded (id, name, email, password, remember_token on
// users), so an application that configures nothing keeps authenticating
// against exactly the same rows and columns.
//
// It deliberately composes orm.IDInt rather than orm.Model: the ORM
// stamps updated_at on every map-based update of a timestamped model,
// and remember-token rotation happens on every remember-me recall. A
// timestamped default would silently start touching users.updated_at on
// login, and would break outright on a users table that has no such
// column.
//
// RememberToken is a *string because the column is nullable for users
// who have never used remember-me; scanning SQL NULL into a plain string
// field is a driver error.
//
// User implements [auth.Authenticatable] directly, which is the path
// applications should prefer for their own models: it skips the
// reflection-based column mapping entirely.
type User struct {
	orm.IDInt[User]

	Name          string  `orm:"column:name" json:"name"`
	Email         string  `orm:"column:email" json:"email"`
	Password      string  `orm:"column:password" json:"-"`
	RememberToken *string `orm:"column:remember_token" json:"-"`
}

// Fillable declares the mass-assignment allowlist. Without a declared
// policy the ORM denies every map-based write, which would reject the
// remember-token update.
func (User) Fillable() []string {
	return []string{"name", "email", "password", "remember_token"}
}

// GetAuthIdentifier returns the primary key.
func (u *User) GetAuthIdentifier() interface{} { return u.ID }

// GetAuthPassword returns the stored password hash.
func (u *User) GetAuthPassword() string { return u.Password }

// GetRememberToken returns the remember-me token, or "" when the column
// is NULL.
func (u *User) GetRememberToken() string {
	if u.RememberToken == nil {
		return ""
	}
	return *u.RememberToken
}

// SetRememberToken sets the remember-me token.
func (u *User) SetRememberToken(token string) {
	u.RememberToken = &token
}
