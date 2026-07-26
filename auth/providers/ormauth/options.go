package ormauth

import (
	"golang.org/x/crypto/bcrypt"

	"github.com/velocitykode/velocity/auth"
)

// Default column names. They reproduce the column set the framework
// previously hardcoded, so a provider constructed with no options at all
// queries exactly what the old raw-SQL provider queried.
const (
	// DefaultIdentifierColumn is the column matched against the login
	// credential (NOT the primary key - see Options.IdentifierColumn).
	DefaultIdentifierColumn = "email"

	// DefaultPasswordColumn holds the password hash.
	DefaultPasswordColumn = "password"

	// DefaultRememberTokenColumn holds the remember-me token.
	DefaultRememberTokenColumn = "remember_token"
)

// Options is the resolved configuration of a Provider. Construct it
// through [Option] values passed to [New] or [Factory] rather than
// building it directly.
type Options struct {
	// Hasher verifies a candidate password against the stored hash.
	// Defaults to the auth package's default bcrypt hasher; the
	// framework overrides it with the auth manager's configured hasher
	// so an operator-tuned bcrypt cost is honoured.
	Hasher auth.Hasher

	// IdentifierColumn is the column a login credential is matched
	// against (email, username, ...). It is distinct from the primary
	// key: the primary key backs GetAuthIdentifier and is read from the
	// model's ORM metadata, never configured here.
	IdentifierColumn string

	// PasswordColumn holds the password hash. Unused when the model
	// implements auth.Authenticatable itself.
	PasswordColumn string

	// RememberTokenColumn holds the remember-me token. Read on lookup
	// and written on both the unconditional login-path update and the
	// atomic compare-and-swap rotation, so it is required even when the
	// model implements auth.Authenticatable itself.
	RememberTokenColumn string

	// CredentialsKey is the key read from the credentials map passed to
	// FindByCredentialsCtx. Defaults to IdentifierColumn, which keeps
	// the default configuration reading credentials["email"].
	CredentialsKey string
}

// Option customises a Provider.
type Option func(*Options)

// WithHasher sets the password hasher. A nil hasher is ignored so a
// caller threading through an unconfigured manager cannot accidentally
// disable verification.
func WithHasher(h auth.Hasher) Option {
	return func(o *Options) {
		if h != nil {
			o.Hasher = h
		}
	}
}

// WithIdentifierColumn sets the column a login credential is matched
// against. Unless [WithCredentialsKey] is also supplied, it doubles as
// the key read from the credentials map.
func WithIdentifierColumn(column string) Option {
	return func(o *Options) {
		if column != "" {
			o.IdentifierColumn = column
		}
	}
}

// WithPasswordColumn sets the column holding the password hash.
func WithPasswordColumn(column string) Option {
	return func(o *Options) {
		if column != "" {
			o.PasswordColumn = column
		}
	}
}

// WithRememberTokenColumn sets the column holding the remember-me token.
func WithRememberTokenColumn(column string) Option {
	return func(o *Options) {
		if column != "" {
			o.RememberTokenColumn = column
		}
	}
}

// WithCredentialsKey sets the credentials-map key read by
// FindByCredentialsCtx when it differs from the identifier column (e.g.
// a login form posting "email" against a users.username column).
func WithCredentialsKey(key string) Option {
	return func(o *Options) {
		if key != "" {
			o.CredentialsKey = key
		}
	}
}

// resolveOptions applies opts over the defaults.
func resolveOptions(opts []Option) Options {
	o := Options{
		IdentifierColumn:    DefaultIdentifierColumn,
		PasswordColumn:      DefaultPasswordColumn,
		RememberTokenColumn: DefaultRememberTokenColumn,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.CredentialsKey == "" {
		o.CredentialsKey = o.IdentifierColumn
	}
	if o.Hasher == nil {
		o.Hasher = auth.NewBcryptHasher(bcrypt.DefaultCost)
	}
	return o
}
