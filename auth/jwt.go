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

// ErrBlacklistUnavailable is returned when the blacklist store could not
// answer: one of its methods returned an error or panicked. It wraps the
// cause. Every caller fails closed on it: ValidateToken refuses the token
// (so Check, User and ID refuse the request), RefreshToken issues nothing,
// and RevokeToken reports that the revocation is not known to have landed.
// An outage is never reported as ErrRefreshTokenUsed or as a revoked
// token: the store's answer is unknown, not "used".
var ErrBlacklistUnavailable = errors.New("velocity/auth: jwt blacklist store unavailable")

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
	// A backend failure is (false, err), never true: the manager refuses
	// the refresh with ErrBlacklistUnavailable and issues nothing.
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
	Add(jti string, expiresAt time.Time) (added bool, err error)
	// IsBlacklisted checks whether a token JTI has been blacklisted. A
	// backend failure is returned as an error; the manager refuses the
	// token on any error, whatever the bool says.
	IsBlacklisted(jti string) (bool, error)
	// Cleanup removes expired entries.
	Cleanup() error
}

// InMemoryBlacklistStore is the default in-memory blacklist (not suitable for multi-instance deployments).
type InMemoryBlacklistStore struct {
	mu      sync.RWMutex
	entries map[string]time.Time
	// now is the clock the store reads at each step; nil is time.Now. The
	// manager builds its default store with its own clock, so the two
	// agree on one source.
	now func() time.Time
}

// NewInMemoryBlacklistStore creates a new in-memory blacklist store.
func NewInMemoryBlacklistStore() *InMemoryBlacklistStore {
	return newInMemoryBlacklistStore(nil)
}

// newInMemoryBlacklistStore builds a store that reads now (nil: time.Now).
func newInMemoryBlacklistStore(now func() time.Time) *InMemoryBlacklistStore {
	return &InMemoryBlacklistStore{
		entries: make(map[string]time.Time),
		now:     now,
	}
}

// clock reads the store's clock.
func (s *InMemoryBlacklistStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Add puts jti on the blacklist until expiresAt and reports whether it was
// absent before the call (an expired entry counts as absent). The check
// and the write run under one write lock, so of N concurrent calls with
// one JTI exactly one returns true. A call that returns false never
// shortens the live entry: it keeps the later of the two expiries. An
// expiresAt that is not after the current time returns false and writes
// nothing: the clock is read once, under the lock, for both checks. The
// error is always nil.
func (s *InMemoryBlacklistStore) Add(jti string, expiresAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock() //lock-held-ok: the clock is the store's own unexported field (time.Now unless the manager or a test in this package set it), not user code; it is read under the lock so the instant belongs to the atomic step
	if !expiresAt.After(now) {
		return false, nil
	}
	if current, exists := s.entries[jti]; exists && !now.After(current) {
		if expiresAt.After(current) {
			s.entries[jti] = expiresAt
		}
		return false, nil
	}
	s.entries[jti] = expiresAt
	return true, nil
}

// IsBlacklisted reports whether jti is on the blacklist and not expired.
// The error is always nil.
func (s *InMemoryBlacklistStore) IsBlacklisted(jti string) (bool, error) {
	s.mu.RLock()
	expiresAt, exists := s.entries[jti]
	s.mu.RUnlock()
	if !exists {
		return false, nil
	}
	if s.clock().After(expiresAt) {
		// Drop the entry only if it is still the expired one read above:
		// an Add between the two locks may have put a live entry there.
		s.mu.Lock()
		if current, still := s.entries[jti]; still && current.Equal(expiresAt) {
			delete(s.entries, jti)
		}
		s.mu.Unlock()
		return false, nil
	}
	return true, nil
}

// Cleanup removes expired entries. The error is always nil.
func (s *InMemoryBlacklistStore) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock() //lock-held-ok: the clock is the store's own unexported field (time.Now unless the manager or a test in this package set it), not user code; it is read under the lock so the instant belongs to the atomic step
	for jti, expiresAt := range s.entries {
		if now.After(expiresAt) {
			delete(s.entries, jti)
		}
	}
	return nil
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
	// now is the one clock the manager reads: for minting, for the token
	// parser, for blacklist expiries and for the in-memory blacklist store
	// it builds. nil is time.Now. Set before the manager is shared.
	now func() time.Time
}

// clock reads the manager's clock.
func (j *JWTManager) clock() time.Time {
	if j.now != nil {
		return j.now()
	}
	return time.Now()
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
		store = newInMemoryBlacklistStore(nil)
	}

	refreshStore := config.RefreshGenerationStore
	if refreshStore == nil {
		refreshStore = NewInMemoryRefreshGenerationStore()
	}

	// The manager holds the store in one place, the field blStore hands
	// out. Its config copy keeps none, so nothing can reach the store
	// around the contained calls.
	config.BlacklistStore = nil

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
	if nilval.Is(store) {
		j.blacklistStore = newInMemoryBlacklistStore(j.now)
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
		j.blacklistStore = newInMemoryBlacklistStore(j.now)
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
	if nilval.Is(store) {
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
	id, subject, err := identity.Snapshot(user)
	if err != nil {
		return "", err
	}
	return j.signAccessToken(id, subject, customClaims...)
}

// signAccessToken mints an access token for an identity snapshot
// (identity.Snapshot): the subject text and an id that later code cannot
// change. RefreshToken takes the snapshot once, before its final decision,
// and signs from it: neither the user value nor its identifier object is
// called again between the decision and the signature, so both claims
// name the identity the decision was taken for.
func (j *JWTManager) signAccessToken(id any, subject string, customClaims ...map[string]interface{}) (string, error) {
	now := j.clock()
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
	id, userID, err := identity.Snapshot(user)
	if err != nil {
		return "", err
	}
	now := j.clock()
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
	claims, err := j.verifyToken(tokenString)
	if err != nil {
		return nil, err
	}

	// Check if token is blacklisted. A store that cannot answer refuses
	// the token: an unknown membership is never read as "not revoked".
	if j.config.BlacklistEnabled {
		listed, err := j.IsBlacklisted(claims.ID)
		if err != nil {
			return nil, err
		}
		if listed {
			return nil, errTokenRevoked
		}
		// The store read is user code and may have blocked past the
		// expiry the parser accepted: judge the expiry again on a fresh
		// reading, so validity is the token's state when validation ends.
		if err := j.checkExpiry(claims); err != nil {
			return nil, err
		}
	}

	return claims, nil
}

// verifyToken is everything ValidateToken decides from the token itself:
// algorithm, signature, issuer, audience, expiry and the required claims.
// It does not ask the blacklist, so it answers during a blacklist outage.
// ValidateToken adds the blacklist read; SignOutToken uses it alone,
// because a sign-out must not depend on the store it is about to write.
func (j *JWTManager) verifyToken(tokenString string) (*Claims, error) {
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
	// The parser judges exp and nbf on the manager's clock. Unset, the
	// parser's own default is time.Now, the same source.
	if j.now != nil {
		parserOpts = append(parserOpts, jwt.WithTimeFunc(j.now))
	}

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

	return claims, nil
}

// checkExpiry refuses claims whose expiry is not after a fresh reading of
// the manager's clock, with jwt.ErrTokenExpired as ValidateToken gives.
func (j *JWTManager) checkExpiry(claims *Claims) error {
	if !j.clock().Before(claims.ExpiresAt.Time) {
		return errchain.Errorf("velocity/auth: token expired during validation: %w", jwt.ErrTokenExpired)
	}
	return nil
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
		return nil, errNotAccessToken
	}
	return claims, nil
}

// errNotAccessToken refuses a token of another type where an access token
// is required.
var errNotAccessToken = errors.New("velocity/auth: token is not an access token")

// RefreshToken creates a new token from a refresh token.
//
// Returns ErrRefreshGenerationStale when the refresh token's embedded
// generation is less than the user's current generation counter. The
// H-07 fix uses this to invalidate every outstanding refresh token on
// Logout: bumping the counter immediately stales all prior refresh
// tokens for that user, without writing each JTI to a blacklist.
//
// A refresh mints a credential, so its decision is taken again inside the
// minting step, not once at validation. The steps run in this order:
// validate, type check, generation check, user lookup, identity snapshot,
// then the final decision (generation and expiry, both read fresh), the
// consume (BlacklistStore.Add, when the blacklist is enabled) and the
// issue. The user lookup and the identity read are user code and may take
// any time: a token that expires, or whose generation Logout or RevokeAll
// bumps, while they run buys nothing, with the blacklist enabled or not.
// The access token is signed from the identity snapshot, so the user value
// is not called again after the final decision.
//
// With JWTConfig.BlacklistEnabled a refresh token is spent once. The
// consume is the store's one atomic step, so of N calls presenting one
// token, concurrent or not, exactly one gets an access token and the
// others get ErrRefreshTokenUsed. The store refuses a consume whose
// deadline has passed, so a token that expires while the consume itself is
// pending buys nothing either: the caller is refused with
// jwt.ErrTokenExpired, as ValidateToken refuses an expired token.
// A failure before the consume (a stale generation, a user-store error, an
// unreadable user identity) leaves the token usable for a retry. A store
// that cannot answer (its Add returns an error or panics) refuses the
// refresh with ErrBlacklistUnavailable and issues nothing; whether the
// token was spent is then unknown. Signing the new token failing after the
// consume burns the token: the client signs in again.
//
// With BlacklistEnabled false nothing is consumed: the refresh token stays
// usable until it expires or its generation is revoked, with no replay
// exclusion and no reuse signal.
func (j *JWTManager) RefreshToken(refreshTokenString string, userStore UserStore) (string, error) {
	// Validate refresh token
	claims, err := j.ValidateToken(refreshTokenString)
	if err != nil {
		// Compared by identity, not through the chain: an outage wraps the
		// store's own error, and a store error whose Is claims every
		// target must stay an outage.
		if err == errTokenRevoked {
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
	// deployments propagating their counter via Redis see the bump. This
	// first check refuses a stale token before the user store is asked.
	userIDStr, _ := claims.UserID.(string)
	if userIDStr == "" {
		userIDStr = errchain.Sprintf("%v", claims.UserID)
	}
	if err := j.checkRefreshGeneration(userIDStr, claims); err != nil {
		return "", err
	}

	// Get user
	user, err := userStore.FindByID(claims.UserID)
	if err != nil {
		return "", err
	}
	// FindByID may return (nil, nil) for an unknown id (user deleted since
	// the refresh token was minted). Surface that as an error so the
	// identity read below never sees a nil user. A typed nil is the same
	// answer: refused here, before the consume, so the token is not spent
	// on a user that cannot be issued for.
	if nilval.Is(user) {
		return "", ErrUserNotFound
	}

	// Snapshot the identity the new token is signed for now, before the
	// final decision: the read is user code, and nothing slow may sit
	// between the decision and the signature. The snapshot is immutable,
	// so the generation store call below (user code too) cannot change
	// what is signed.
	id, subject, err := identity.Snapshot(user)
	if err != nil {
		return "", err
	}

	// Final decision. The lookup and the identity read above may have
	// taken any time, so the generation and the expiry are read again
	// here: a Logout that bumped the generation meanwhile, or an expiry
	// that passed, refuses the refresh. The generation is read first
	// because its store may block; the expiry is judged after it.
	if err := j.checkRefreshGeneration(userIDStr, claims); err != nil {
		return "", err
	}
	if !j.clock().Before(claims.ExpiresAt.Time) {
		return "", errRefreshExpired()
	}

	// Consume the refresh token: last, so a failure above leaves it usable,
	// and before issuance, so only the call that consumed it issues.
	if j.config.BlacklistEnabled {
		consumed, err := j.blacklistAdd(claims.ID, j.blacklistExpiry(claims.ExpiresAt.Time))
		if err != nil {
			return "", err
		}
		if !consumed {
			// The store refuses for two reasons: the JTI is on the
			// blacklist, or the token's expiry passed while the consume
			// was pending. This clock read only names the error; the
			// refusal itself was decided inside the store's atomic step.
			if !j.clock().Before(claims.ExpiresAt.Time) {
				return "", errRefreshExpired()
			}
			return "", ErrRefreshTokenUsed
		}
	}

	// Generate new access token from the identity snapshot.
	return j.signAccessToken(id, subject)
}

// errRefreshExpired is RefreshToken's answer for a refresh token whose
// expiry passed after ValidateToken accepted it.
func errRefreshExpired() error {
	return errchain.Errorf("velocity/auth: refresh token expired before it was consumed: %w", jwt.ErrTokenExpired)
}

// checkRefreshGeneration refuses claims whose refresh generation is older
// than the user's current one. A generation store that cannot answer
// refuses too: an outage must not re-enable refresh tokens Logout or
// RevokeAll revoked.
func (j *JWTManager) checkRefreshGeneration(userID string, claims *Claims) error {
	current, err := j.refreshGenStore().Current(userID)
	if err != nil {
		return errors.New("velocity/auth: refresh generation store unavailable")
	}
	if claims.RefreshGeneration < current {
		return ErrRefreshGenerationStale
	}
	return nil
}

// containBlacklist is the one boundary between the manager and the
// blacklist store's code. Deferred by each of the three calls below, it
// turns a panic in the store into an error and wraps every store failure
// in ErrBlacklistUnavailable, keeping the cause. The store is user code:
// it is called under no manager lock (blStore copies the interface out
// first), so a store that blocks or calls back holds nothing of the
// manager's.
func containBlacklist(err *error) {
	if r := recover(); r != nil {
		*err = errchain.Errorf("%w: %w", ErrBlacklistUnavailable, panicerr.FromRecovered(r))
		return
	}
	if *err != nil {
		*err = errchain.Errorf("%w: %w", ErrBlacklistUnavailable, *err)
	}
}

// blacklistAdd is the store's Add behind containBlacklist. A failed Add is
// (false, err) whatever the store returned beside the error.
func (j *JWTManager) blacklistAdd(jti string, expiresAt time.Time) (added bool, err error) {
	store := j.blStore()
	defer containBlacklist(&err)
	added, err = store.Add(jti, expiresAt)
	return added && err == nil, err
}

// blacklistHas is the store's IsBlacklisted behind containBlacklist.
func (j *JWTManager) blacklistHas(jti string) (listed bool, err error) {
	store := j.blStore()
	defer containBlacklist(&err)
	return store.IsBlacklisted(jti)
}

// blacklistCleanup is the store's Cleanup behind containBlacklist.
func (j *JWTManager) blacklistCleanup() (err error) {
	store := j.blStore()
	defer containBlacklist(&err)
	return store.Cleanup()
}

// blacklistExpiry is the expiry a blacklist entry gets: the token's own
// when known, else the access token TTL from now.
func (j *JWTManager) blacklistExpiry(expiresAt time.Time) time.Time {
	if !expiresAt.IsZero() {
		return expiresAt
	}
	return j.clock().Add(time.Duration(j.config.TTL) * time.Minute)
}

// RevokeToken adds token to blacklist. If expiresAt is provided, use it as the
// blacklist expiry; otherwise falls back to the access token TTL. Revoking
// a JTI that is already on the blacklist never shortens its entry: the
// later of the two expiries holds. An expiresAt that has already passed
// writes nothing: the token it names no longer validates.
//
// It returns ErrBlacklistUnavailable (wrapping the cause) when the store
// could not take the entry: the token is then not known to be revoked, and
// the caller must not report it revoked. With BlacklistEnabled false it
// does nothing and returns nil.
func (j *JWTManager) RevokeToken(jti string, expiresAt ...time.Time) error {
	if !j.config.BlacklistEnabled {
		return nil
	}
	var expiry time.Time
	if len(expiresAt) > 0 {
		expiry = expiresAt[0]
	}
	// Revocation is idempotent: whether the JTI was already on the
	// blacklist does not matter here, only whether the store answered.
	_, err := j.blacklistAdd(jti, j.blacklistExpiry(expiry))
	return err
}

// SignOutToken signs the token's user out: it verifies the token, puts its
// id on the blacklist until the token's expiry, and bumps the user's
// refresh generation so every refresh token issued to the user before
// stops minting. It takes an access token or a refresh token.
//
// The token is verified from itself (signature, issuer, audience, expiry,
// required claims) without reading the blacklist, so a sign-out works
// while the blacklist store is down. A token that does not verify returns
// (nil, err) and changes nothing.
//
// A refresh token is also checked against the user's current refresh
// generation, before either write, with the check RefreshToken uses. One
// from before a bump (an earlier sign-out or RevokeAll) is refused: it
// returns (nil, ErrRefreshGenerationStale) and changes nothing, so an old
// refresh token cannot end the refresh tokens the user holds now. The
// refusal is not a revocation: the token's id is not put on the blacklist,
// and only the generation counter keeps it from minting. A generation
// store that loses its counters (the in-process default does on restart)
// makes such a token current again. When
// the generation store cannot answer that check the claims are returned
// with the store's error and nothing is written: nil claims always mean
// "this token signs nobody out", never a store failure. An access token
// takes no generation check.
//
// Past those checks the claims are returned whatever happens next, and
// the two writes run in this order: the blacklist add, then the generation
// bump, which is always attempted whatever the add answered (added,
// already present, or an error). So a sign-out that failed part-way is
// completed by calling again with the same token. The error is the
// blacklist store's (wrapping ErrBlacklistUnavailable), the generation
// store's, or both joined; the sign-out is complete only when it is nil.
// With BlacklistEnabled false only the generation is bumped.
//
// What remains open, by design of this sequence:
//   - a holder of a revoked access token that has not yet expired can
//     still end the user's refresh tokens with it, until it expires: the
//     bump does not depend on the blacklist's answer;
//   - a refresh token of the current generation that was already spent by
//     a refresh, or revoked by id, can do the same until the next bump
//     makes it stale or it expires;
//   - two callers presenting the same refresh token at once can both pass
//     the generation check, and both bump;
//   - the two writes are not one step: one can land without the other,
//     which is what the error and the retry are for.
//
// Known limit: signing out with only a refresh token does not end access
// tokens already issued to the user; they live until they expire. Present
// the access token to end it.
//
// RevokeToken remains the single-token step: it revokes one id and leaves
// the user's refresh tokens alone.
func (j *JWTManager) SignOutToken(token string) (*Claims, error) {
	claims, err := j.verifyToken(token)
	if err != nil {
		return nil, err
	}

	userID, _ := claims.UserID.(string)
	if userID == "" && claims.UserID != nil {
		userID = errchain.Sprintf("%v", claims.UserID)
	}

	if claims.TokenType == "refresh" {
		if err := j.checkRefreshGeneration(userID, claims); err != nil {
			// checkRefreshGeneration returns the stale sentinel itself,
			// so it is told from a store failure by identity.
			if err == ErrRefreshGenerationStale {
				return nil, err
			}
			return claims, err
		}
	}

	revokeErr := j.RevokeToken(claims.ID, claims.ExpiresAt.Time)

	var bumpErr error
	if userID != "" {
		if _, err := j.BumpRefreshGeneration(userID); err != nil {
			bumpErr = errchain.Errorf("velocity/auth: refresh generation not bumped: %w", err)
		}
	}

	switch {
	case revokeErr != nil && bumpErr != nil:
		return claims, errchain.Errorf("%w; %w", revokeErr, bumpErr)
	case revokeErr != nil:
		return claims, revokeErr
	default:
		return claims, bumpErr
	}
}

// IsBlacklisted checks if token is blacklisted.
//
// When the store cannot answer it returns (true, err) with err wrapping
// ErrBlacklistUnavailable: refuse the token, its membership is unknown.
// The true is there so a caller that drops the error still refuses.
func (j *JWTManager) IsBlacklisted(jti string) (bool, error) {
	if !j.config.BlacklistEnabled {
		return false, nil
	}
	listed, err := j.blacklistHas(jti)
	if err != nil {
		return true, err
	}
	return listed, nil
}

// CleanupBlacklist removes expired entries from blacklist. It returns
// ErrBlacklistUnavailable (wrapping the cause) when the store failed.
func (j *JWTManager) CleanupBlacklist() error {
	return j.blacklistCleanup()
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
