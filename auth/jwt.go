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

	"github.com/velocitykode/velocity/auth/internal/identity"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/nilval"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// ErrUnsupportedSigningMethod is returned when the configured JWT algorithm
// cannot be mapped to a concrete jwt.SigningMethod. Callers must refuse to
// sign or verify tokens when this error is returned, there is no safe
// fallback because silently downgrading to HS256 would allow an attacker to
// substitute any algorithm name in config or tokens.
var ErrUnsupportedSigningMethod = errors.New("velocity/auth: unsupported jwt signing method")

// ErrRefreshGenerationStale is returned from RefreshToken when the refresh
// token carries a generation counter older than the user's current
// generation. The H-07 fix bumps the counter on Logout so a stolen refresh
// token cannot survive a sign-out: the next /auth/refresh call resolves
// against a stale generation and is rejected.
var ErrRefreshGenerationStale = errors.New("velocity/auth: refresh token generation is stale")

// ErrRefreshTokenUsed is returned from RefreshToken when the refresh
// token's JTI is already on the blacklist. Two things put it there: an
// earlier refresh consumed it (with the blacklist enabled a refresh token
// buys exactly one access token, and of N calls presenting the same token,
// at once or one after another, one succeeds and the others get this
// error), or the application revoked the JTI through RevokeToken. The
// client's action is the same in both cases: it signs in again.
//
// A refresh token whose expiry passes before it is consumed is not
// reported as used: RefreshToken refuses it with jwt.ErrTokenExpired, the
// error ValidateToken gives an expired token.
var ErrRefreshTokenUsed = errors.New("velocity/auth: refresh token has already been used")

// ErrTokenClaimMissing is returned from ValidateToken (and so from
// ValidateAccessToken and RefreshToken) for a correctly signed token that
// carries no expiry (exp) or no token id (jti). Both are required of every
// token: a token without an expiry never lapses, and the blacklist keys on
// the id, so tokens without one would share a single entry and one
// revocation or refresh would spend them all. Tokens minted by this
// manager always carry both.
var ErrTokenClaimMissing = errors.New("velocity/auth: jwt is missing a required claim")

// errTokenRevoked is ValidateToken's answer for a token whose JTI is on
// the blacklist. RefreshToken reports it as ErrRefreshTokenUsed, so a
// reused refresh token gets one error whether it lost the consume or
// arrived after it.
var errTokenRevoked = errors.New("velocity/auth: token has been revoked")

// BlacklistStore defines the interface for JWT token blacklist storage.
// Implement with Redis or another persistent store for production use.
//
// Implementations MUST be safe for concurrent use.
type BlacklistStore interface {
	// Add puts a token JTI on the blacklist until expiresAt and reports
	// whether this call consumed it: true when the JTI was not on the
	// blacklist before the call (an entry past its expiry counts as
	// absent), false when it already was. The check and the write are one
	// atomic step in the store (a set-if-absent, e.g. Redis SET NX with
	// the expiry): of N concurrent calls with one JTI exactly one returns
	// true. RefreshToken relies on this to spend a refresh token once.
	//
	// The step is atomic across every process sharing the backend, not
	// only within one: a Redis store uses SET NX with the expiry or one
	// Lua script, never a get followed by a set.
	//
	// A backend failure fails closed: the store returns false (the refresh
	// is refused), never true.
	//
	// An Add that returns false never shortens the existing entry: the
	// entry keeps the later of its own expiry and expiresAt, so a
	// revocation repeated with a longer expiry holds for the longer one.
	//
	// A deadline that has already passed consumes nothing: when expiresAt
	// is not after the store's current time, Add returns false and writes
	// no entry, whatever the blacklist holds. The store compares against
	// its own clock inside the same atomic step (a Redis store does both
	// in one Lua script; one that turns expiresAt into a relative TTL
	// returns false when the TTL is not positive). Without the
	// rule the entry such a call wrote would be absent on arrival, so every
	// later call with that JTI would also report true: a refresh token
	// that expires between its validation and its consume would buy one
	// access token per caller. With it, a call that returns true leaves an
	// entry that stays live until expiresAt, and every call after
	// expiresAt returns false.
	Add(jti string, expiresAt time.Time) bool
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

// Add puts jti on the blacklist until expiresAt and reports whether it was
// absent before the call (an expired entry counts as absent). The check
// and the write run under one write lock, so of N concurrent calls with
// one JTI exactly one returns true. A call that returns false never
// shortens the live entry: it keeps the later of the two expiries. An
// expiresAt that is not after the current time returns false and writes
// nothing: the clock is read once, under the lock, for both checks.
func (s *InMemoryBlacklistStore) Add(jti string, expiresAt time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if !expiresAt.After(now) {
		return false
	}
	if current, exists := s.entries[jti]; exists && !now.After(current) {
		if expiresAt.After(current) {
			s.entries[jti] = expiresAt
		}
		return false
	}
	s.entries[jti] = expiresAt
	return true
}

func (s *InMemoryBlacklistStore) IsBlacklisted(jti string) bool {
	s.mu.RLock()
	expiresAt, exists := s.entries[jti]
	s.mu.RUnlock()
	if !exists {
		return false
	}
	if time.Now().After(expiresAt) {
		// Drop the entry only if it is still the expired one read above:
		// an Add between the two locks may have put a live entry there.
		s.mu.Lock()
		if current, still := s.entries[jti]; still && current.Equal(expiresAt) {
			delete(s.entries, jti)
		}
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
	Secret     string
	Algorithm  string
	TTL        int    // Minutes
	RefreshTTL int    // Minutes
	Issuer     string // Optional JWT issuer (iss claim)
	Audience   string // Optional JWT audience (aud claim)
	// BlacklistEnabled turns on JTI revocation: Logout blacklists the access
	// token, ValidateToken refuses a blacklisted JTI, and RefreshToken
	// spends each refresh token once (a second use gets
	// ErrRefreshTokenUsed). When false there is no one-shot refresh: a
	// refresh token mints access tokens as often as it is presented until
	// it expires or its generation is revoked (Logout and RevokeAll bump
	// the user's refresh generation; that check runs in either mode). The
	// mode offers no replay exclusion and no reuse signal: a copied
	// refresh token works alongside the original and nothing reports it.
	BlacklistEnabled bool
	BlacklistStore   BlacklistStore // Optional persistent store; defaults to in-memory

	// AllowQueryToken opts into accepting the access token from the
	// "?token=<jwt>" query parameter on WebSocket upgrade requests. It
	// defaults to false (off): query-string credentials leak into load
	// balancer / proxy / access logs, browser history, and Referer headers.
	// Prefer the Sec-WebSocket-Protocol "bearer.<token>" transport,
	// which is always accepted. Enable this only for legacy clients that
	// cannot set the subprotocol header.
	AllowQueryToken bool

	// RefreshGenerationStore lets the operator install a shared
	// (typically Redis-backed) per-user refresh-generation counter so
	// Logout-driven bumps from H-07 propagate across hosts. Without
	// this, multi-host deployments would each carry their own in-memory
	// counter and a stolen refresh token would still refresh on hosts
	// that did not see the Logout. Nil falls back to the in-process
	// InMemoryRefreshGenerationStore.
	RefreshGenerationStore RefreshGenerationStore

	// RSAPrivateKey / RSAPublicKey enable asymmetric signing (RS256/RS384/RS512).
	// When RSA algorithms are selected, the HMAC Secret is ignored for signing/verification.
	RSAPrivateKey interface{} // *rsa.PrivateKey, signing key for RSxxx algorithms
	RSAPublicKey  interface{} // *rsa.PublicKey, verification key for RSxxx algorithms

	// PreviousSecrets lists HMAC secrets retired from minting but still
	// accepted for verification (E-02). Lets operators rotate Secret on
	// the standard cadence without invalidating every outstanding access
	// AND refresh token in lock-step. Tokens signed under any entry here
	// verify successfully until they expire on their own. Order is the
	// order tried after the active Secret fails verification.
	//
	// MINTING never uses these: GenerateToken / GenerateRefreshToken
	// always sign with the current Secret. Drop a retired secret from
	// this slice once its longest-lived token (typically the refresh TTL)
	// has expired.
	PreviousSecrets []string

	// PreviousRSAPublicKeys lists RSA public keys retired from minting
	// but still accepted for verification (E-02). Same lifecycle and
	// semantics as PreviousSecrets but for RSxxx algorithms. Entries
	// MUST be *rsa.PublicKey values (mirrors the type stored in
	// RSAPublicKey).
	PreviousRSAPublicKeys []interface{}
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
		// Previous secrets enable verify-only key rotation (E-02). The
		// length guard mirrors the active secret so a retired weak key
		// never re-enters service via this slot.
		for i, prev := range c.PreviousSecrets {
			if prev == "" {
				return fmt.Errorf("velocity/auth: jwt previous secret at index %d must not be empty", i)
			}
			if len(prev) < 32 {
				return fmt.Errorf("velocity/auth: jwt previous secret at index %d must be at least 32 bytes", i)
			}
			if prev == c.Secret {
				return fmt.Errorf("velocity/auth: jwt previous secret at index %d duplicates active secret", i)
			}
		}
		if len(c.PreviousRSAPublicKeys) > 0 {
			return errors.New("velocity/auth: jwt previous rsa public keys are only valid for rsa algorithms")
		}
	}
	if isRSAAlgorithm(alg) {
		if c.RSAPrivateKey == nil || c.RSAPublicKey == nil {
			return errors.New("velocity/auth: jwt rsa key pair is required for rsa algorithms")
		}
		// Previous public keys enable verify-only key rotation (E-02).
		// Mirror the active RSAPublicKey type to keep the signature loop
		// in ValidateToken simple.
		for i, prev := range c.PreviousRSAPublicKeys {
			if prev == nil {
				return fmt.Errorf("velocity/auth: jwt previous rsa public key at index %d must not be nil", i)
			}
		}
		if len(c.PreviousSecrets) > 0 {
			return errors.New("velocity/auth: jwt previous secrets are only valid for hmac algorithms")
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

	// RefreshGeneration is the per-user generation counter at the time
	// the refresh token was issued. RefreshToken rejects any refresh
	// token whose RefreshGeneration is less than the user's current
	// generation, so JWTScheme.Logout can revoke every refresh token
	// outstanding for the user by bumping the counter. Access tokens
	// leave this zero. See audit H-07.
	RefreshGeneration int64 `json:"rgn,omitempty"`
}

// RefreshGenerationStore is the per-user generation counter the H-07 fix
// uses to revoke refresh tokens on Logout. Refresh tokens carry the user's
// generation at issue time; bumping the counter (Logout) invalidates every
// outstanding refresh token for that user without writing each JTI to a
// blacklist.
//
// In a multi-host deployment the store SHOULD be backed by Redis or
// another shared cache so the bump propagates across the fleet. The
// default in-process implementation is single-host only; multi-host
// deployments need to wire SetRefreshGenerationStore from boot.
//
// Implementations MUST be safe for concurrent use.
type RefreshGenerationStore interface {
	// Current returns the active generation for userID. Implementations
	// must return 0 (not an error) when no record exists; callers treat
	// 0 as "never bumped". Errors should be reserved for transport-level
	// failures.
	Current(userID string) (int64, error)

	// Bump increments and returns the new generation for userID. Used
	// by JWTScheme.Logout to invalidate every refresh token outstanding
	// for the user.
	Bump(userID string) (int64, error)
}

// InMemoryRefreshGenerationStore is the default RefreshGenerationStore.
// In-process scope only: counter resets on restart and does NOT propagate
// across hosts. Suitable for single-host deployments and tests.
type InMemoryRefreshGenerationStore struct {
	mu     sync.RWMutex
	counts map[string]int64
}

// NewInMemoryRefreshGenerationStore returns an empty in-process store.
func NewInMemoryRefreshGenerationStore() *InMemoryRefreshGenerationStore {
	return &InMemoryRefreshGenerationStore{counts: make(map[string]int64)}
}

// Current returns the generation for userID. Empty userID yields 0 so
// tokens that lack a subject never look stale; the Validate path rejects
// them on other grounds.
func (s *InMemoryRefreshGenerationStore) Current(userID string) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.counts[userID], nil
}

// Bump increments and returns the new generation for userID.
func (s *InMemoryRefreshGenerationStore) Bump(userID string) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[userID]++
	return s.counts[userID], nil
}

// JWTManager handles JWT operations
type JWTManager struct {
	config             JWTConfig
	blacklistStore     BlacklistStore
	blMu               sync.RWMutex // protects blacklistStore swaps
	refreshGenerations RefreshGenerationStore
	rgMu               sync.RWMutex // protects refreshGenerations swaps
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

	refreshStore := config.RefreshGenerationStore
	if refreshStore == nil {
		refreshStore = NewInMemoryRefreshGenerationStore()
	}

	return &JWTManager{
		config:             config,
		blacklistStore:     store,
		refreshGenerations: refreshStore,
	}, nil
}

// SetBlacklistStore replaces the blacklist store (e.g., swap in a
// Redis-backed store). Passing nil reverts to the in-process
// InMemoryBlacklistStore. Safe for concurrent use: the swap is
// mutex-guarded and readers go through blStore so a concurrent
// RevokeToken / IsBlacklisted cannot tear the interface read (same
// pattern as SetRefreshGenerationStore below).
func (j *JWTManager) SetBlacklistStore(store BlacklistStore) {
	j.blMu.Lock()
	defer j.blMu.Unlock()
	if store == nil {
		j.blacklistStore = NewInMemoryBlacklistStore()
		return
	}
	j.blacklistStore = store
}

// blStore returns the active blacklist store under a read lock so a
// concurrent SetBlacklistStore call cannot tear the underlying interface
// read. Defensively lazy-initialises on first read so callers that
// construct *JWTManager by literal struct (test helpers in the existing
// suite) do not nil-deref.
func (j *JWTManager) blStore() BlacklistStore {
	j.blMu.RLock()
	store := j.blacklistStore
	j.blMu.RUnlock()
	if store != nil {
		return store
	}
	j.blMu.Lock()
	defer j.blMu.Unlock()
	if j.blacklistStore == nil {
		j.blacklistStore = NewInMemoryBlacklistStore()
	}
	return j.blacklistStore
}

// SetRefreshGenerationStore installs a refresh-generation counter store.
// Pass a cache/Redis-backed implementation in multi-host deployments so
// Logout-driven generation bumps propagate across the fleet. Passing nil
// reverts to the in-process default.
//
// Safe for concurrent use.
func (j *JWTManager) SetRefreshGenerationStore(store RefreshGenerationStore) {
	j.rgMu.Lock()
	defer j.rgMu.Unlock()
	if store == nil {
		j.refreshGenerations = NewInMemoryRefreshGenerationStore()
		return
	}
	j.refreshGenerations = store
}

// refreshGenStore returns the active refresh-generation store under a
// read lock so a concurrent SetRefreshGenerationStore call cannot tear the
// underlying interface read. Defensively lazy-initialises on first read
// so callers that construct *JWTManager by literal struct (test helpers
// in the existing suite) do not nil-deref.
func (j *JWTManager) refreshGenStore() RefreshGenerationStore {
	j.rgMu.RLock()
	store := j.refreshGenerations
	j.rgMu.RUnlock()
	if store != nil {
		return store
	}
	j.rgMu.Lock()
	defer j.rgMu.Unlock()
	if j.refreshGenerations == nil {
		j.refreshGenerations = NewInMemoryRefreshGenerationStore()
	}
	return j.refreshGenerations
}

// BumpRefreshGeneration invalidates every refresh token outstanding for
// userID by bumping the per-user counter; refresh-token validation rejects
// any token whose embedded generation is less than the current value. Used
// by JWTScheme.Logout (H-07 fix).
//
// Returns the new generation value. Best-effort: implementations are
// permitted to return an error on transport failure; the caller decides
// whether to surface or swallow.
func (j *JWTManager) BumpRefreshGeneration(userID string) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	return j.refreshGenStore().Bump(userID)
}

// CurrentRefreshGeneration returns the active generation for userID. Used
// by the refresh-token validation path; exposed publicly so callers can
// build administrative listings.
func (j *JWTManager) CurrentRefreshGeneration(userID string) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	return j.refreshGenStore().Current(userID)
}

// signingKey returns the key to pass to SignedString for the active algorithm.
func (j *JWTManager) signingKey() interface{} {
	if isRSAAlgorithm(j.config.Algorithm) {
		return j.config.RSAPrivateKey
	}
	return []byte(j.config.Secret)
}

// verificationKey returns the key (or keys) to use for signature verification.
//
// When PreviousSecrets / PreviousRSAPublicKeys (E-02) are populated, the
// returned value is a jwt.VerificationKeySet whose Keys are tried in order
// by the underlying parser: current key first, then each retired key.
// This enables verify-only key rotation. Operators can rotate the active
// minting key without invalidating every outstanding access AND refresh
// token in lock-step. Retired keys stay accepted only until their tokens
// naturally expire.
//
// Minting (signingKey) is unaffected, it always returns the active key.
func (j *JWTManager) verificationKey() interface{} {
	if isRSAAlgorithm(j.config.Algorithm) {
		if len(j.config.PreviousRSAPublicKeys) == 0 {
			return j.config.RSAPublicKey
		}
		keys := make([]jwt.VerificationKey, 0, 1+len(j.config.PreviousRSAPublicKeys))
		keys = append(keys, j.config.RSAPublicKey)
		for _, prev := range j.config.PreviousRSAPublicKeys {
			keys = append(keys, prev)
		}
		return jwt.VerificationKeySet{Keys: keys}
	}
	if len(j.config.PreviousSecrets) == 0 {
		return []byte(j.config.Secret)
	}
	keys := make([]jwt.VerificationKey, 0, 1+len(j.config.PreviousSecrets))
	keys = append(keys, []byte(j.config.Secret))
	for _, prev := range j.config.PreviousSecrets {
		keys = append(keys, []byte(prev))
	}
	return jwt.VerificationKeySet{Keys: keys}
}

// GenerateToken generates a JWT token for a user
func (j *JWTManager) GenerateToken(user contract.Authenticatable, customClaims ...map[string]interface{}) (string, error) {
	id, subject, err := identity.Of(user)
	if err != nil {
		return "", err
	}
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
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    j.config.Issuer,
		},
		UserID:    id,
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

// GenerateRefreshToken generates a refresh token.
//
// Embeds the user's current refresh-generation counter so Logout can
// invalidate the token by bumping the counter (see RefreshToken /
// BumpRefreshGeneration). Counter-store transport failures are logged
// through the absence of an error path: GenerateRefreshToken still issues
// the token with generation 0 because failing here would block Login on
// transient cache flaps. The trade-off: a generation lookup error degrades
// gracefully to "act as if user has no prior generation"; subsequent
// Logout-driven bumps still invalidate the token.
func (j *JWTManager) GenerateRefreshToken(user contract.Authenticatable) (string, error) {
	id, userID, err := identity.Of(user)
	if err != nil {
		return "", err
	}
	now := time.Now()
	expiresAt := now.Add(time.Duration(j.config.RefreshTTL) * time.Minute)

	jti, err := generateJTI()
	if err != nil {
		return "", err
	}

	generation, _ := j.refreshGenStore().Current(userID)

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    j.config.Issuer,
		},
		UserID:            id,
		TokenType:         "refresh",
		RefreshGeneration: generation,
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

	// Every token carries an expiry and an id. Without the expiry the
	// revocation and refresh paths have no time to key a blacklist entry
	// on; without the id every such token would share one blacklist key.
	if claims.ExpiresAt == nil {
		return nil, errchain.Errorf("%w: exp", ErrTokenClaimMissing)
	}
	if claims.ID == "" {
		return nil, errchain.Errorf("%w: jti", ErrTokenClaimMissing)
	}

	// Check if token is blacklisted
	if j.config.BlacklistEnabled && j.IsBlacklisted(claims.ID) {
		return nil, errTokenRevoked
	}

	return claims, nil
}

// ValidateAccessToken validates a token AND asserts it is an access
// token. The authentication accessors (Check/User/ID) must use this so a
// refresh token cannot be replayed as a Bearer access credential
// (audit finding: refresh-as-access). RefreshToken keeps using
// ValidateToken because it intentionally consumes refresh tokens.
func (j *JWTManager) ValidateAccessToken(tokenString string) (*Claims, error) {
	claims, err := j.ValidateToken(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != "access" {
		return nil, errors.New("velocity/auth: token is not an access token")
	}
	return claims, nil
}

// RefreshToken creates a new token from a refresh token.
//
// Returns ErrRefreshGenerationStale when the refresh token's embedded
// generation is less than the user's current generation counter. The
// H-07 fix uses this to invalidate every outstanding refresh token on
// Logout: bumping the counter immediately stales all prior refresh
// tokens for that user, without writing each JTI to a blacklist.
//
// With JWTConfig.BlacklistEnabled a refresh token is spent once. The steps
// run in this order: validate, type check, generation check, user lookup,
// consume (BlacklistStore.Add), issue. The consume is the store's one
// atomic step, so of N calls presenting one token, concurrent or not,
// exactly one gets an access token and the others get ErrRefreshTokenUsed.
// The store refuses a consume whose deadline has passed, so a token that
// was valid at the first step and expired before the consume (a slow
// generation store or user lookup) buys nothing: every caller is refused
// with jwt.ErrTokenExpired, as ValidateToken refuses an expired token.
// A failure before the consume (a stale generation, a user-store error)
// leaves the token usable for a retry. A failure after it (the store's Add
// panicking, or signing the new token failing) burns the token and issues
// nothing: the refresh fails closed and the client signs in again.
//
// With BlacklistEnabled false nothing is consumed: the refresh token stays
// usable until it expires or its generation is revoked, with no replay
// exclusion and no reuse signal.
func (j *JWTManager) RefreshToken(refreshTokenString string, userStore UserStore) (string, error) {
	// Validate refresh token
	claims, err := j.ValidateToken(refreshTokenString)
	if err != nil {
		if errchain.Is(err, errTokenRevoked) {
			// The JTI is on the blacklist: for a refresh token that is a
			// consume that already landed.
			return "", ErrRefreshTokenUsed
		}
		return "", err
	}

	// Ensure this is actually a refresh token
	if claims.TokenType != "refresh" {
		return "", errors.New("velocity/auth: token is not a refresh token")
	}

	// Generation check (H-07): reject tokens whose embedded generation
	// is older than the user's current generation. The counter resolves
	// against the configured RefreshGenerationStore, so multi-host
	// deployments propagating their counter via Redis see the bump.
	userIDStr, _ := claims.UserID.(string)
	if userIDStr == "" {
		userIDStr = errchain.Sprintf("%v", claims.UserID)
	}
	current, cgErr := j.refreshGenStore().Current(userIDStr)
	if cgErr != nil {
		// Fail closed: a store outage must not silently re-enable refresh
		// tokens administratively revoked by Logout/RevokeAll.
		return "", errors.New("velocity/auth: refresh generation store unavailable")
	}
	if claims.RefreshGeneration < current {
		return "", ErrRefreshGenerationStale
	}

	// Get user
	user, err := userStore.FindByID(claims.UserID)
	if err != nil {
		return "", err
	}
	// FindByID may return (nil, nil) for an unknown id (user deleted since
	// the refresh token was minted). Surface that as an error so the
	// GenerateToken claims deref below never sees a nil user. A typed nil
	// is the same answer: refused here, before the consume, so the token is
	// not spent on a user that cannot be issued for.
	if nilval.Is(user) {
		return "", ErrUserNotFound
	}

	// Consume the refresh token: last, so a failure above leaves it usable,
	// and before issuance, so only the call that consumed it issues.
	if j.config.BlacklistEnabled {
		consumed, err := j.consumeJTI(claims.ID, claims.ExpiresAt.Time)
		if err != nil {
			return "", err
		}
		if !consumed {
			// The store refuses for two reasons: the JTI is on the
			// blacklist, or the token's expiry passed since ValidateToken
			// accepted it. This clock read only names the error; the
			// refusal itself was decided inside the store's atomic step.
			if !time.Now().Before(claims.ExpiresAt.Time) {
				return "", errchain.Errorf("velocity/auth: refresh token expired before it was consumed: %w", jwt.ErrTokenExpired)
			}
			return "", ErrRefreshTokenUsed
		}
	}

	// Generate new access token
	return j.GenerateToken(user)
}

// consumeJTI spends jti on the blacklist store and reports whether this
// call was the one that consumed it. The store is user code: it is called
// under no framework lock (blStore copies the interface out first), and a
// panic in its Add is returned as an error, so the caller issues nothing.
func (j *JWTManager) consumeJTI(jti string, expiresAt time.Time) (consumed bool, err error) {
	store := j.blStore()
	defer func() {
		if r := recover(); r != nil {
			consumed = false
			err = errchain.Errorf("velocity/auth: jwt blacklist store failed: %w", panicerr.FromRecovered(r))
		}
	}()
	return store.Add(jti, j.blacklistExpiry(expiresAt)), nil
}

// blacklistExpiry is the expiry a blacklist entry gets: the token's own
// when known, else the access token TTL from now.
func (j *JWTManager) blacklistExpiry(expiresAt time.Time) time.Time {
	if !expiresAt.IsZero() {
		return expiresAt
	}
	return time.Now().Add(time.Duration(j.config.TTL) * time.Minute)
}

// RevokeToken adds token to blacklist. If expiresAt is provided, use it as the
// blacklist expiry; otherwise falls back to the access token TTL. Revoking
// a JTI that is already on the blacklist never shortens its entry: the
// later of the two expiries holds. An expiresAt that has already passed
// writes nothing: the token it names no longer validates.
func (j *JWTManager) RevokeToken(jti string, expiresAt ...time.Time) {
	if j.config.BlacklistEnabled {
		var expiry time.Time
		if len(expiresAt) > 0 {
			expiry = expiresAt[0]
		}
		// Revocation is idempotent: whether the JTI was already on the
		// blacklist does not matter here.
		_ = j.blStore().Add(jti, j.blacklistExpiry(expiry))
	}
}

// IsBlacklisted checks if token is blacklisted
func (j *JWTManager) IsBlacklisted(jti string) bool {
	if !j.config.BlacklistEnabled {
		return false
	}
	return j.blStore().IsBlacklisted(jti)
}

// CleanupBlacklist removes expired entries from blacklist
func (j *JWTManager) CleanupBlacklist() {
	j.blStore().Cleanup()
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
		return nil, errchain.Errorf("%w: %q", ErrUnsupportedSigningMethod, j.config.Algorithm)
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
		return "", errchain.Errorf("velocity/auth: failed to generate jwt id: %w", err)
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
