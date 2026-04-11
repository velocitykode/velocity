package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// BlacklistStore defines the interface for JWT token blacklist storage.
// Implement with Redis or another persistent store for production use.
type BlacklistStore interface {
	// Add adds a token JTI to the blacklist with an expiration time.
	Add(jti string, expiresAt time.Time)
	// IsBlacklisted checks whether a token JTI has been blacklisted.
	IsBlacklisted(jti string) bool
	// Cleanup removes expired entries.
	Cleanup()
}

// InMemoryBlacklistStore is the default in-memory blacklist (not suitable for multi-instance deployments).
type InMemoryBlacklistStore struct {
	mu      sync.RWMutex
	entries map[string]time.Time
}

// NewInMemoryBlacklistStore creates a new in-memory blacklist store.
func NewInMemoryBlacklistStore() *InMemoryBlacklistStore {
	return &InMemoryBlacklistStore{
		entries: make(map[string]time.Time),
	}
}

func (s *InMemoryBlacklistStore) Add(jti string, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[jti] = expiresAt
}

func (s *InMemoryBlacklistStore) IsBlacklisted(jti string) bool {
	s.mu.RLock()
	expiresAt, exists := s.entries[jti]
	s.mu.RUnlock()
	if !exists {
		return false
	}
	if time.Now().After(expiresAt) {
		s.mu.Lock()
		delete(s.entries, jti)
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *InMemoryBlacklistStore) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jti, expiresAt := range s.entries {
		if now.After(expiresAt) {
			delete(s.entries, jti)
		}
	}
}

// JWTConfig holds JWT configuration
type JWTConfig struct {
	Secret           string
	Algorithm        string
	TTL              int    // Minutes
	RefreshTTL       int    // Minutes
	Issuer           string // Optional JWT issuer (iss claim)
	Audience         string // Optional JWT audience (aud claim)
	BlacklistEnabled bool
	BlacklistStore   BlacklistStore // Optional persistent store; defaults to in-memory
}

// Claims represents JWT claims
type Claims struct {
	jwt.RegisteredClaims
	UserID    interface{} `json:"uid,omitempty"`
	Email     string      `json:"email,omitempty"`
	Role      string      `json:"role,omitempty"`
	TokenType string      `json:"type,omitempty"` // "access" or "refresh"
}

// JWTManager handles JWT operations
type JWTManager struct {
	config         JWTConfig
	blacklistStore BlacklistStore
}

// NewJWTManager creates a new JWT manager.
// Panics if Secret is empty or shorter than 32 bytes.
func NewJWTManager(config JWTConfig) *JWTManager {
	if config.Secret == "" {
		panic("auth: JWT secret must not be empty")
	}
	if len(config.Secret) < 32 {
		panic("auth: JWT secret must be at least 32 bytes")
	}

	if config.Algorithm == "" {
		config.Algorithm = "HS256"
	}
	if config.TTL == 0 {
		config.TTL = 60 // Default 60 minutes
	}
	if config.RefreshTTL == 0 {
		config.RefreshTTL = 20160 // Default 2 weeks
	}

	store := config.BlacklistStore
	if store == nil {
		store = NewInMemoryBlacklistStore()
		if config.BlacklistEnabled {
			log.Println("jwt: using in-memory token blacklist. Set a persistent BlacklistStore for production multi-instance deployments")
		}
	}

	return &JWTManager{
		config:         config,
		blacklistStore: store,
	}
}

// SetBlacklistStore replaces the blacklist store (e.g., swap in a Redis-backed store).
func (j *JWTManager) SetBlacklistStore(store BlacklistStore) {
	j.blacklistStore = store
}

// GenerateToken generates a JWT token for a user
func (j *JWTManager) GenerateToken(user Authenticatable, customClaims ...map[string]interface{}) (string, error) {
	now := time.Now()
	expiresAt := now.Add(time.Duration(j.config.TTL) * time.Minute)

	// Generate unique JWT ID
	jti, err := generateJTI()
	if err != nil {
		return "", err
	}

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   fmt.Sprintf("%v", user.GetAuthIdentifier()),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    j.config.Issuer,
		},
		UserID:    user.GetAuthIdentifier(),
		TokenType: "access",
	}
	if j.config.Audience != "" {
		claims.Audience = jwt.ClaimStrings{j.config.Audience}
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

	jti, err := generateJTI()
	if err != nil {
		return "", err
	}

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   fmt.Sprintf("%v", user.GetAuthIdentifier()),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    j.config.Issuer,
		},
		UserID:    user.GetAuthIdentifier(),
		TokenType: "refresh",
	}
	if j.config.Audience != "" {
		claims.Audience = jwt.ClaimStrings{j.config.Audience}
	}

	token := jwt.NewWithClaims(j.getSigningMethod(), claims)
	return token.SignedString([]byte(j.config.Secret))
}

// ValidateToken validates a JWT token
func (j *JWTManager) ValidateToken(tokenString string) (*Claims, error) {
	var parserOpts []jwt.ParserOption
	if j.config.Issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(j.config.Issuer))
	}
	if j.config.Audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(j.config.Audience))
	}

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate signing method
		if token.Method.Alg() != j.config.Algorithm {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Method.Alg())
		}
		return []byte(j.config.Secret), nil
	}, parserOpts...)

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

	// Ensure this is actually a refresh token
	if claims.TokenType != "refresh" {
		return "", errors.New("token is not a refresh token")
	}

	// Get user
	user, err := provider.FindByID(claims.UserID)
	if err != nil {
		return "", err
	}

	// Blacklist old refresh token using its actual expiry
	if j.config.BlacklistEnabled {
		j.RevokeToken(claims.ID, claims.ExpiresAt.Time)
	}

	// Generate new access token
	return j.GenerateToken(user)
}

// RevokeToken adds token to blacklist. If expiresAt is provided, use it as the
// blacklist expiry; otherwise falls back to the access token TTL.
func (j *JWTManager) RevokeToken(jti string, expiresAt ...time.Time) {
	if j.config.BlacklistEnabled {
		var expiry time.Time
		if len(expiresAt) > 0 && !expiresAt[0].IsZero() {
			expiry = expiresAt[0]
		} else {
			expiry = time.Now().Add(time.Duration(j.config.TTL) * time.Minute)
		}
		j.blacklistStore.Add(jti, expiry)
	}
}

// IsBlacklisted checks if token is blacklisted
func (j *JWTManager) IsBlacklisted(jti string) bool {
	if !j.config.BlacklistEnabled {
		return false
	}
	return j.blacklistStore.IsBlacklisted(jti)
}

// CleanupBlacklist removes expired entries from blacklist
func (j *JWTManager) CleanupBlacklist() {
	j.blacklistStore.Cleanup()
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

// generateJTI generates a unique JWT ID.
func generateJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: failed to generate JWT ID: %w", err)
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// ParseTokenWithoutValidation parses a token WITHOUT verifying its signature.
//
// WARNING: This method is UNSAFE for authentication or authorization decisions.
// Claims returned by this method have NOT been verified and may have been tampered with.
// Only use this for non-security-sensitive operations such as extracting claims from
// expired tokens for logging or token rotation. Never trust the returned claims
// for granting access or making security decisions.
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
