package auth

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/velocitykode/velocity/pkg/orm"
)

// ORMUserProvider provides users from ORM models
type ORMUserProvider struct {
	modelType string
	hasher    Hasher
}

// NewORMUserProvider creates a new ORM user provider
func NewORMUserProvider(modelType string) *ORMUserProvider {
	return &ORMUserProvider{
		modelType: modelType,
		hasher:    GetHasher(),
	}
}

// FindByID finds user by ID
func (p *ORMUserProvider) FindByID(id interface{}) (Authenticatable, error) {
	if id == nil {
		return nil, ErrUserNotFound
	}

	db := orm.DB()
	if db == nil {
		return nil, errors.New("database not initialized")
	}

	var user MockUser
	row := db.QueryRow("SELECT id, name, email, password FROM users WHERE id = $1", id)
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

	db := orm.DB()
	if db == nil {
		return nil, errors.New("database not initialized")
	}

	var user MockUser
	row := db.QueryRow("SELECT id, name, email, password FROM users WHERE email = $1", email)
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

// UpdateRememberToken updates user's remember token
func (p *ORMUserProvider) UpdateRememberToken(user Authenticatable, token string) error {
	user.SetRememberToken(token)
	// In real implementation, save to database
	return nil
}

// MockUser is a mock user for testing
type MockUser struct {
	ID            interface{}
	Name          string
	Email         string
	Password      string
	RememberToken string
}

// GetAuthIdentifier returns user ID
func (u *MockUser) GetAuthIdentifier() interface{} {
	return u.ID
}

// GetAuthPassword returns user password hash
func (u *MockUser) GetAuthPassword() string {
	return u.Password
}

// GetRememberToken returns remember token
func (u *MockUser) GetRememberToken() string {
	return u.RememberToken
}

// SetRememberToken sets remember token
func (u *MockUser) SetRememberToken(token string) {
	u.RememberToken = token
}

// String returns string representation
func (u *MockUser) String() string {
	return fmt.Sprintf("MockUser<%v: %s>", u.ID, u.Email)
}
