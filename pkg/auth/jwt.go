package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTConfig holds JWT configuration
type JWTConfig struct {
	Secret           string
	Algorithm        string
	TTL              int // Minutes
	RefreshTTL       int // Minutes
	BlacklistEnabled bool
}

// Claims represents JWT claims
type Claims struct {
	jwt.RegisteredClaims
	UserID interface{} `json:"uid,omitempty"`
	Email  string      `json:"email,omitempty"`
	Role   string      `json:"role,omitempty"`
}

// JWTManager handles JWT operations
type JWTManager struct {
	config    JWTConfig
	blacklist map[string]time.Time // Simple in-memory blacklist
}

// NewJWTManager creates a new JWT manager
func NewJWTManager(config JWTConfig) *JWTManager {
	if config.Algorithm == "" {
		config.Algorithm = "HS256"
	}
	if config.TTL == 0 {
		config.TTL = 60 // Default 60 minutes
	}
	if config.RefreshTTL == 0 {
		config.RefreshTTL = 20160 // Default 2 weeks
	}

	return &JWTManager{
		config:    config,
		blacklist: make(map[string]time.Time),
	}
}

// GenerateToken generates a JWT token for a user
func (j *JWTManager) GenerateToken(user Authenticatable, customClaims ...map[string]interface{}) (string, error) {
	now := time.Now()
	expiresAt := now.Add(time.Duration(j.config.TTL) * time.Minute)

	// Generate unique JWT ID
	jti := generateJTI()

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   fmt.Sprintf("%v", user.GetAuthIdentifier()),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
		},
		UserID: user.GetAuthIdentifier(),
	}

	// Add custom claims if provided
	if len(customClaims) > 0 {
		for key, value := range customClaims[0] {
			switch key {
			case "email":
				if email, ok := value.(string); ok {
					claims.Email = email
				}
			case "role":
				if role, ok := value.(string); ok {
					claims.Role = role
				}
			}
		}
	}

	token := jwt.NewWithClaims(j.getSigningMethod(), claims)
	return token.SignedString([]byte(j.config.Secret))
}

// GenerateRefreshToken generates a refresh token
func (j *JWTManager) GenerateRefreshToken(user Authenticatable) (string, error) {
	now := time.Now()
	expiresAt := now.Add(time.Duration(j.config.RefreshTTL) * time.Minute)

	jti := generateJTI()

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   fmt.Sprintf("%v", user.GetAuthIdentifier()),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
		},
		UserID: user.GetAuthIdentifier(),
	}

	token := jwt.NewWithClaims(j.getSigningMethod(), claims)
	return token.SignedString([]byte(j.config.Secret))
}

// ValidateToken validates a JWT token
func (j *JWTManager) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate signing method
		if token.Method.Alg() != j.config.Algorithm {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Method.Alg())
		}
		return []byte(j.config.Secret), nil
	})

	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}

	// Check if token is blacklisted
	if j.config.BlacklistEnabled && j.IsBlacklisted(claims.ID) {
		return nil, errors.New("token has been revoked")
	}

	return claims, nil
}

// RefreshToken creates a new token from a refresh token
func (j *JWTManager) RefreshToken(refreshTokenString string, provider UserProvider) (string, error) {
	// Validate refresh token
	claims, err := j.ValidateToken(refreshTokenString)
	if err != nil {
		return "", err
	}

	// Get user
	user, err := provider.FindByID(claims.UserID)
	if err != nil {
		return "", err
	}

	// Blacklist old refresh token
	if j.config.BlacklistEnabled {
		j.RevokeToken(claims.ID)
	}

	// Generate new access token
	return j.GenerateToken(user)
}

// RevokeToken adds token to blacklist
func (j *JWTManager) RevokeToken(jti string) {
	if j.config.BlacklistEnabled {
		j.blacklist[jti] = time.Now().Add(time.Duration(j.config.TTL) * time.Minute)
	}
}

// IsBlacklisted checks if token is blacklisted
func (j *JWTManager) IsBlacklisted(jti string) bool {
	if !j.config.BlacklistEnabled {
		return false
	}

	expiresAt, exists := j.blacklist[jti]
	if !exists {
		return false
	}

	// Clean up expired blacklist entries
	if time.Now().After(expiresAt) {
		delete(j.blacklist, jti)
		return false
	}

	return true
}

// CleanupBlacklist removes expired entries from blacklist
func (j *JWTManager) CleanupBlacklist() {
	now := time.Now()
	for jti, expiresAt := range j.blacklist {
		if now.After(expiresAt) {
			delete(j.blacklist, jti)
		}
	}
}

// getSigningMethod returns the signing method based on algorithm
func (j *JWTManager) getSigningMethod() jwt.SigningMethod {
	switch j.config.Algorithm {
	case "HS256":
		return jwt.SigningMethodHS256
	case "HS384":
		return jwt.SigningMethodHS384
	case "HS512":
		return jwt.SigningMethodHS512
	default:
		return jwt.SigningMethodHS256
	}
}

// generateJTI generates a unique JWT ID
func generateJTI() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.URLEncoding.EncodeToString(b)
}

// ParseTokenWithoutValidation parses token without validating signature
// Useful for extracting claims from expired tokens
func (j *JWTManager) ParseTokenWithoutValidation(tokenString string) (*Claims, error) {
	token, _, err := jwt.NewParser().ParseUnverified(tokenString, &Claims{})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, errors.New("invalid claims")
	}

	return claims, nil
}
