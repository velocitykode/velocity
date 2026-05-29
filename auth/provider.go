package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// ORMUserProvider provides users from ORM models
type ORMUserProvider struct {
	db        *sql.DB
	modelType string
	hasher    Hasher
}

// NewORMUserProvider creates a new ORM user provider.
// If hasher is nil, a default bcrypt hasher is used.
func NewORMUserProvider(db *sql.DB, modelType string, hasher Hasher) *ORMUserProvider {
	if hasher == nil {
		hasher = NewBcryptHasher(bcrypt.DefaultCost)
	}
	return &ORMUserProvider{
		db:        db,
		modelType: modelType,
		hasher:    hasher,
	}
}

// FindByIDCtx finds user by ID using the provided context.
func (p *ORMUserProvider) FindByIDCtx(ctx context.Context, id interface{}) (Authenticatable, error) {
	if id == nil {
		return nil, ErrUserNotFound
	}

	if p.db == nil {
		return nil, errors.New("database not initialized")
	}

	var (
		user     AuthUser
		remember sql.NullString
	)
	row := p.db.QueryRowContext(ctx, "SELECT id, name, email, password, remember_token FROM users WHERE id = $1", id)
	err := row.Scan(&user.ID, &user.Name, &user.Email, &user.Password, &remember)
	if err == sql.ErrNoRows {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	user.ID = normalizeID(user.ID)
	user.RememberToken = remember.String

	return &user, nil
}

// FindByID finds user by ID.
//
// Deprecated: use FindByIDCtx with a request-scoped context.Context.
func (p *ORMUserProvider) FindByID(id interface{}) (Authenticatable, error) {
	return p.FindByIDCtx(context.Background(), id)
}

// FindByCredentialsCtx finds user by credentials (email/username) using the
// provided context.
func (p *ORMUserProvider) FindByCredentialsCtx(ctx context.Context, credentials map[string]interface{}) (Authenticatable, error) {
	email, ok := credentials["email"].(string)
	if !ok {
		return nil, errors.New("email is required")
	}

	if p.db == nil {
		return nil, errors.New("database not initialized")
	}

	var (
		user     AuthUser
		remember sql.NullString
	)
	row := p.db.QueryRowContext(ctx, "SELECT id, name, email, password, remember_token FROM users WHERE email = $1", email)
	err := row.Scan(&user.ID, &user.Name, &user.Email, &user.Password, &remember)
	if err == sql.ErrNoRows {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	user.ID = normalizeID(user.ID)
	user.RememberToken = remember.String

	return &user, nil
}

// FindByCredentials finds user by credentials (email/username).
//
// Deprecated: use FindByCredentialsCtx with a request-scoped context.Context.
func (p *ORMUserProvider) FindByCredentials(credentials map[string]interface{}) (Authenticatable, error) {
	return p.FindByCredentialsCtx(context.Background(), credentials)
}

// ValidateCredentials validates user credentials.
func (p *ORMUserProvider) ValidateCredentials(user Authenticatable, credentials map[string]interface{}) bool {
	password, ok := credentials["password"].(string)
	if !ok {
		return false
	}

	return p.hasher.Verify(password, user.GetAuthPassword())
}

// UpdateRememberTokenCtx updates the user's remember token and persists it
// to the database using the provided context.
func (p *ORMUserProvider) UpdateRememberTokenCtx(ctx context.Context, user Authenticatable, token string) error {
	user.SetRememberToken(token)

	if p.db == nil {
		return errors.New("database not initialized")
	}

	_, err := p.db.ExecContext(ctx, "UPDATE users SET remember_token = $1 WHERE id = $2", token, user.GetAuthIdentifier())
	return err
}

// UpdateRememberToken updates user's remember token and persists it.
//
// Deprecated: use UpdateRememberTokenCtx with a request-scoped context.Context.
func (p *ORMUserProvider) UpdateRememberToken(user Authenticatable, token string) error {
	return p.UpdateRememberTokenCtx(context.Background(), user, token)
}

// normalizeID converts numeric ID values from database drivers into uint
// so that GetAuthIdentifier() always returns a consistent type regardless
// of the underlying database driver.
func normalizeID(v interface{}) interface{} {
	switch id := v.(type) {
	case int64:
		return uint(id)
	case int:
		return uint(id)
	case int32:
		return uint(id)
	case float64:
		return uint(id)
	case uint:
		return id
	case uint64:
		return uint(id)
	default:
		// String IDs (UUIDs) pass through unchanged
		return v
	}
}

// AuthUser represents an authenticated user
type AuthUser struct {
	ID            interface{}
	Name          string
	Email         string
	Password      string
	RememberToken string
}

// GetAuthIdentifier returns user ID
func (u *AuthUser) GetAuthIdentifier() interface{} {
	return u.ID
}

// GetAuthPassword returns user password hash
func (u *AuthUser) GetAuthPassword() string {
	return u.Password
}

// GetRememberToken returns remember token
func (u *AuthUser) GetRememberToken() string {
	return u.RememberToken
}

// SetRememberToken sets remember token
func (u *AuthUser) SetRememberToken(token string) {
	u.RememberToken = token
}

// String returns string representation
func (u *AuthUser) String() string {
	return fmt.Sprintf("AuthUser<%v>", u.ID)
}
