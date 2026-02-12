package auth

import (
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

// FindByID finds user by ID
func (p *ORMUserProvider) FindByID(id interface{}) (Authenticatable, error) {
	if id == nil {
		return nil, ErrUserNotFound
	}

	if p.db == nil {
		return nil, errors.New("database not initialized")
	}

	var user AuthUser
	row := p.db.QueryRow("SELECT id, name, email, password FROM users WHERE id = $1", id)
	err := row.Scan(&user.ID, &user.Name, &user.Email, &user.Password)
	if err == sql.ErrNoRows {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}

	return &user, nil
}

// FindByCredentials finds user by credentials (email/username)
func (p *ORMUserProvider) FindByCredentials(credentials map[string]interface{}) (Authenticatable, error) {
	email, ok := credentials["email"].(string)
	if !ok {
		return nil, errors.New("email is required")
	}

	if p.db == nil {
		return nil, errors.New("database not initialized")
	}

	var user AuthUser
	row := p.db.QueryRow("SELECT id, name, email, password FROM users WHERE email = $1", email)
	err := row.Scan(&user.ID, &user.Name, &user.Email, &user.Password)
	if err == sql.ErrNoRows {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}

	return &user, nil
}

// ValidateCredentials validates user credentials
func (p *ORMUserProvider) ValidateCredentials(user Authenticatable, credentials map[string]interface{}) bool {
	password, ok := credentials["password"].(string)
	if !ok {
		return false
	}

	return p.hasher.Verify(password, user.GetAuthPassword())
}

// UpdateRememberToken updates user's remember token and persists it to the database.
func (p *ORMUserProvider) UpdateRememberToken(user Authenticatable, token string) error {
	user.SetRememberToken(token)

	if p.db == nil {
		return errors.New("database not initialized")
	}

	_, err := p.db.Exec("UPDATE users SET remember_token = $1 WHERE id = $2", token, user.GetAuthIdentifier())
	return err
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
	return fmt.Sprintf("AuthUser<%v: %s>", u.ID, u.Email)
}
