package auth

import (
	"fmt"
)

// NormalizeID converts numeric ID values from database drivers into uint
// so that GetAuthIdentifier() always returns a consistent type regardless
// of the underlying database driver.
//
// Schemes depend on this: SessionScheme encodes the identifier into the
// remember-me cookie and hands it back to FindByID, so a driver that
// surfaces an integer primary key as int64 on one backend and int on
// another must not change the identifier's Go type. String identifiers
// (UUIDs) pass through unchanged.
//
// Exported for UserStore implementations outside this package (see
// auth/stores/ormauth), which must produce identifiers of the same
// shape.
func NormalizeID(v interface{}) interface{} {
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

// AuthUser is a minimal [Authenticatable] value. It is the shape schemes
// and tests use when a user store has no model of its own to hand back.
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
