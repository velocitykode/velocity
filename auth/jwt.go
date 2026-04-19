package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrUnsupportedSigningMethod is returned when the configured JWT algorithm
// cannot be mapped to a concrete jwt.SigningMethod. Callers must refuse to
// sign or verify tokens when this error is returned — there is no safe
// fallback because silently downgrading to HS256 would allow an attacker to
// substitute any algorithm name in config or tokens.
var ErrUnsupportedSigningMethod = errors.New("velocity/auth: unsupported jwt signing method")

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

	// RSAPrivateKey / RSAPublicKey enable asymmetric signing (RS256/RS384/RS512).
	// When RSA algorithms are selected, the HMAC Secret is ignored for signing/verification.
	RSAPrivateKey interface{} // *rsa.PrivateKey — signing key for RSxxx algorithms
	RSAPublicKey  interface{} // *rsa.PublicKey  — verification key for RSxxx algorithms
}

// allowedJWTAlgorithms is the allowlist of accepted JWT signing algorithms.
// "none" is explicitly excluded.
var allowedJWTAlgorithms = map[string]struct{}{
	"HS256": {},
	"HS384": {},
	"HS512": {},
	"RS256": {},
	"RS384": {},
	"RS512": {},
}

// isHMACAlgorithm reports whether alg is one of the supported HMAC algorithms.
func isHMACAlgorithm(alg string) bool {
	switch alg {
	case "HS256", "HS384", "HS512":
		return true
	}
	return false
}

// isRSAAlgorithm reports whether alg is one of the supported RSA algorithms.
func isRSAAlgorithm(alg string) bool {
	switch alg {
	case "RS256", "RS384", "RS512":
		return true
	}
	return false
}

// Validate checks the JWTConfig for required fields and rejects unsafe defaults.
func (c JWTConfig) Validate() error {
	alg := c.Algorithm
	if alg == "" {
		alg = "HS256"
	}
	if _, ok := allowedJWTAlgorithms[alg]; !ok {
		return fmt.Errorf("velocity/auth: unsupported jwt algorithm %q", alg)
	}
	if c.TTL <= 0 {
		return errors.New("velocity/auth: jwt ttl must be positive")
	}
	if isHMACAlgorithm(alg) {
		if c.Secret == "" {
			return errors.New("velocity/auth: jwt secret must not be empty for hmac algorithms")
		}
		if len(c.Secret) < 32 {
			return errors.New("velocity/auth: jwt secret must be at least 32 bytes for hmac algorithms")
		}
	}
	if isRSAAlgorithm(alg) {
		if c.RSAPrivateKey == nil || c.RSAPublicKey == nil {
			return errors.New("velocity/auth: jwt rsa key pair is required for rsa algorithms")
		}
	}
	if c.BlacklistEnabled && c.BlacklistStore == nil {
		return errors.New("velocity/auth: jwt blacklist enabled requires a persistent blacklist store")
	}
	return nil
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
// Returns an error when the config is incomplete (missing/too-short secret,
// non-positive TTL, unsupported algorithm, or missing RSA keys for RS*).
func NewJWTManager(config JWTConfig) (*JWTManager, error) {
	if config.Algorithm == "" {
		config.Algorithm = "HS256"
	}
	if config.TTL == 0 {
		config.TTL = 60 // Default 60 minutes
	}
	if config.RefreshTTL == 0 {
		config.RefreshTTL = 20160 // Default 2 weeks
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	store := config.BlacklistStore
	if store == nil {
		store = NewInMemoryBlacklistStore()
	}

	return &JWTManager{
		config:         config,
		blacklistStore: store,
	}, nil
}

// SetBlacklistStore replaces the blacklist store (e.g., swap in a Redis-backed store).
func (j *JWTManager) SetBlacklistStore(store BlacklistStore) {
	j.blacklistStore = store
}

// signingKey returns the key to pass to SignedString for the active algorithm.
func (j *JWTManager) signingKey() interface{} {
	if isRSAAlgorithm(j.config.Algorithm) {
		return j.config.RSAPrivateKey
	}
	return []byte(j.config.Secret)
}

// verificationKey returns the key to use for signature verification.
func (j *JWTManager) verificationKey() interface{} {
	if isRSAAlgorithm(j.config.Algorithm) {
		return j.config.RSAPublicKey
	}
	return []byte(j.config.Secret)
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

	method, err := j.getSigningMethod()
	if err != nil {
		return "", err
	}
	token := jwt.NewWithClaims(method, claims)
	return token.SignedString(j.signingKey())
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

	method, err := j.getSigningMethod()
	if err != nil {
		return "", err
	}
	token := jwt.NewWithClaims(method, claims)
	return token.SignedString(j.signingKey())
}

// ValidateToken validates a JWT token.
// The algorithm allowlist is enforced BEFORE any signature verification.
// "none" is rejected unconditionally. When the configured algorithm is an
// HMAC variant, only HMAC tokens are accepted; when RSA, only RSA tokens.
func (j *JWTManager) ValidateToken(tokenString string) (*Claims, error) {
	var parserOpts []jwt.ParserOption
	if j.config.Issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(j.config.Issuer))
	}
	if j.config.Audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(j.config.Audience))
	}
	// Explicit valid method allowlist — belt-and-suspenders alongside the
	// keyFunc check below. Prevents the jwt library from ever calling the
	// keyFunc with "none" or any unexpected algorithm.
	parserOpts = append(parserOpts, jwt.WithValidMethods([]string{j.config.Algorithm}))

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		alg := token.Method.Alg()
		// Reject "none" and anything outside the global allowlist.
		if alg == "none" || alg == "" {
			return nil, fmt.Errorf("velocity/auth: jwt algorithm %q is not permitted", alg)
		}
		if _, ok := allowedJWTAlgorithms[alg]; !ok {
			return nil, fmt.Errorf("velocity/auth: jwt algorithm %q is not permitted", alg)
		}
		// Enforce the configured family: HMAC tokens only for HMAC configs,
		// RSA tokens only for RSA configs. This is what prevents the classic
		// HS256-signed-with-public-key confusion attack.
		if isHMACAlgorithm(j.config.Algorithm) && !isHMACAlgorithm(alg) {
			return nil, fmt.Errorf("velocity/auth: unexpected signing method %q (expected hmac)", alg)
		}
		if isRSAAlgorithm(j.config.Algorithm) && !isRSAAlgorithm(alg) {
			return nil, fmt.Errorf("velocity/auth: unexpected signing method %q (expected rsa)", alg)
		}
		if alg != j.config.Algorithm {
			return nil, fmt.Errorf("velocity/auth: unexpected signing method %q", alg)
		}
		return j.verificationKey(), nil
	}, parserOpts...)

	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("velocity/auth: invalid token")
	}

	// Check if token is blacklisted
	if j.config.BlacklistEnabled && j.IsBlacklisted(claims.ID) {
		return nil, errors.New("velocity/auth: token has been revoked")
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
		return "", errors.New("velocity/auth: token is not a refresh token")
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

// getSigningMethod returns the signing method for the configured algorithm.
// Unknown algorithms return (nil, ErrUnsupportedSigningMethod); callers must
// refuse to sign or verify — there is NO HS256 fallback. The default-case
// fallback previously permitted any allowlisted-but-typo'd or future
// algorithm string to silently sign with HS256.
func (j *JWTManager) getSigningMethod() (jwt.SigningMethod, error) {
	switch j.config.Algorithm {
	case "HS256":
		return jwt.SigningMethodHS256, nil
	case "HS384":
		return jwt.SigningMethodHS384, nil
	case "HS512":
		return jwt.SigningMethodHS512, nil
	case "RS256":
		return jwt.SigningMethodRS256, nil
	case "RS384":
		return jwt.SigningMethodRS384, nil
	case "RS512":
		return jwt.SigningMethodRS512, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedSigningMethod, j.config.Algorithm)
	}
}

// randReader is the entropy source used by generateJTI. Tests may override it
// temporarily to simulate crypto/rand failures; production code should never
// reassign this variable.
var randReader io.Reader = rand.Reader

// generateJTI generates a unique JWT ID from the package-level rand source.
func generateJTI() (string, error) {
	return generateJTIWithReader(randReader)
}

// generateJTIWithReader allows callers (typically tests) to supply an
// alternative reader so crypto/rand failures can be exercised deterministically.
func generateJTIWithReader(r io.Reader) (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("velocity/auth: failed to generate jwt id: %w", err)
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
		return nil, errors.New("velocity/auth: invalid claims")
	}

	return claims, nil
}
