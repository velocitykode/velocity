package guards

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/clientip"
)

const (
	jwtCacheTTL        = 5 * time.Minute
	jwtCacheMaxSize    = 10000
	jwtCleanupInterval = 1 * time.Minute
)

type cachedUser struct {
	user     auth.Authenticatable
	cachedAt time.Time
}

// JWTGuard implements JWT-based authentication for APIs
type JWTGuard struct {
	// provider and throttler are held via atomic.Pointer so concurrent
	// SetProvider / SetLoginThrottler calls cannot tear a reader's
	// two-word interface fetch in Attempt / User / Check (H-10 fix).
	provider  atomic.Pointer[providerHolder]
	throttler atomic.Pointer[throttlerHolder]

	jwtManager     *auth.JWTManager
	config         auth.JWTConfig
	mu             sync.RWMutex
	userCache      map[string]cachedUser
	stopCleanup    chan struct{}
	trustedProxies []*net.IPNet
	// attemptFloor is the wall-clock floor for Attempt (H-09 fix).
	// Zero falls back to auth.DefaultAttemptFloor.
	attemptFloor time.Duration
	// hasher is consulted on the missing-user path so CPU timing
	// matches the bcrypt-verify path; defaults to bcrypt cost 10.
	hasher auth.Hasher

	// eventDispatcher emits auth.PasswordNeedsRehashEvent after a
	// successful Attempt against a stored hash that no longer matches
	// the configured Hasher parameters (M-08). Nil disables emission.
	eventDispatcher func(ctx context.Context, event any) error
}

// loadProvider returns the active auth.UserProvider via atomic load.
func (g *JWTGuard) loadProvider() auth.UserProvider {
	h := g.provider.Load()
	if h == nil {
		return nil
	}
	return h.p
}

// loadThrottler returns the active contract.LoginThrottler via atomic load.
// Falls back to NoopLoginThrottler when no throttler has been installed.
func (g *JWTGuard) loadThrottler() contract.LoginThrottler {
	h := g.throttler.Load()
	if h == nil || h.t == nil {
		return auth.NoopLoginThrottler{}
	}
	return h.t
}

// SetAttemptFloor configures the wall-clock floor that Attempt blocks for.
// See auth.Config.AttemptFloor for the threat model.
func (g *JWTGuard) SetAttemptFloor(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.attemptFloor = d
}

func (g *JWTGuard) effectiveAttemptFloor() time.Duration {
	g.mu.RLock()
	d := g.attemptFloor
	g.mu.RUnlock()
	if d == 0 {
		return auth.DefaultAttemptFloor
	}
	if d < 0 {
		return 0
	}
	return d
}

func (g *JWTGuard) effectiveHasher() auth.Hasher {
	g.mu.RLock()
	h := g.hasher
	g.mu.RUnlock()
	if h != nil {
		return h
	}
	return auth.NewBcryptHasher(10)
}

// SetHasher installs the password hasher used by Attempt's dummy-hash
// timing defense. Passing nil leaves the previously installed hasher in
// place (effectiveHasher falls back to bcrypt cost 10 when unset).
//
// factories.go propagates the operator-configured BcryptCost via this
// setter so the dummy hash on the missing-user path runs at the same
// cost as the real verify; without this, a configured cost of 14 would
// have the dummy at cost 10 and the H-09 timing channel would reopen.
func (g *JWTGuard) SetHasher(h auth.Hasher) {
	if h == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hasher = h
}

// SetTrustedProxies installs the parsed proxy-network list used for
// client-IP resolution in the login throttler. Pass nil to revert to
// "no proxies trusted" (forwarded headers are ignored, RemoteAddr is
// used verbatim).
//
// Manager.SetTrustedProxies propagates to every registered guard via
// the auth.TrustedProxiesReceiver interface, so consumers normally do
// not need to call this directly.
func (g *JWTGuard) SetTrustedProxies(proxies []*net.IPNet) {
	// Deep-clone (see SessionGuard.SetTrustedProxies for rationale).
	cloned := clientip.CloneIPNets(proxies)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.trustedProxies = cloned
}

// getTrustedProxies returns the installed trusted-proxy list under a
// read lock so concurrent Attempt() calls see a consistent snapshot.
// The returned slice is a deep clone (see SessionGuard.getTrustedProxies).
func (g *JWTGuard) getTrustedProxies() []*net.IPNet {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return clientip.CloneIPNets(g.trustedProxies)
}

// SetLoginThrottler installs a rate-limiter for Attempt() calls. Passing nil
// reverts to the no-op throttler.
//
// Stored via atomic.Pointer so concurrent Attempt() readers cannot tear the
// two-word interface fetch on the throttler field (H-10 fix).
func (g *JWTGuard) SetLoginThrottler(t contract.LoginThrottler) {
	if t == nil {
		g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
		return
	}
	g.throttler.Store(&throttlerHolder{t: t})
}

// SetEventDispatcher installs the framework event dispatcher used to emit
// auth.PasswordNeedsRehashEvent after a successful Attempt against a
// stored hash that no longer matches the configured Hasher parameters
// (M-08). Pass nil to disable emission. Safe for concurrent use.
//
// Manager.SetEventDispatcher propagates to every registered guard via
// the auth.EventDispatcherReceiver interface; consumers normally do not
// need to call this directly.
func (g *JWTGuard) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.eventDispatcher = fn
}

// getEventDispatcher returns the installed dispatcher under a read lock
// so concurrent Attempt() readers observe a consistent value across a
// SetEventDispatcher swap.
func (g *JWTGuard) getEventDispatcher() func(ctx context.Context, event any) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.eventDispatcher
}

// NewJWTGuard creates a new JWT guard.
// Call Start() to begin the background cache cleanup goroutine.
// Returns an error when the underlying JWTConfig fails validation.
func NewJWTGuard(provider auth.UserProvider, config auth.JWTConfig) (*JWTGuard, error) {
	manager, err := auth.NewJWTManager(config)
	if err != nil {
		return nil, err
	}
	g := &JWTGuard{
		jwtManager:  manager,
		config:      config,
		userCache:   make(map[string]cachedUser),
		stopCleanup: make(chan struct{}),
	}
	g.provider.Store(&providerHolder{p: provider})
	g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
	return g, nil
}

// Start begins the background cache cleanup goroutine. An optional
// context.Context controls the goroutine lifetime; if none is provided,
// use StopCleanup() to stop it.
func (g *JWTGuard) Start(ctx ...context.Context) {
	if len(ctx) > 0 && ctx[0] != nil {
		bg := ctx[0]
		async.Go(func() { g.cleanupLoopWithContext(bg) })
	} else {
		async.Go(func() { g.cleanupLoop() })
	}
}

// StopCleanup stops the background cache cleanup goroutine.
func (g *JWTGuard) StopCleanup() {
	select {
	case <-g.stopCleanup:
		// already closed
	default:
		close(g.stopCleanup)
	}
}

func (g *JWTGuard) cleanupLoop() {
	ticker := time.NewTicker(jwtCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			g.evictExpired()
		case <-g.stopCleanup:
			return
		}
	}
}

func (g *JWTGuard) cleanupLoopWithContext(ctx context.Context) {
	ticker := time.NewTicker(jwtCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			g.evictExpired()
		case <-g.stopCleanup:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (g *JWTGuard) evictExpired() {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for token, entry := range g.userCache {
		if now.Sub(entry.cachedAt) > jwtCacheTTL {
			delete(g.userCache, token)
		}
	}
}

func (g *JWTGuard) getCachedUser(token string) (auth.Authenticatable, bool) {
	g.mu.RLock()
	entry, ok := g.userCache[token]
	g.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Since(entry.cachedAt) > jwtCacheTTL {
		g.mu.Lock()
		delete(g.userCache, token)
		g.mu.Unlock()
		return nil, false
	}
	return entry.user, true
}

func (g *JWTGuard) cacheUser(token string, user auth.Authenticatable) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// If cache is full, evict oldest entry
	if len(g.userCache) >= jwtCacheMaxSize {
		var oldestToken string
		var oldestTime time.Time
		for t, entry := range g.userCache {
			if oldestToken == "" || entry.cachedAt.Before(oldestTime) {
				oldestToken = t
				oldestTime = entry.cachedAt
			}
		}
		if oldestToken != "" {
			delete(g.userCache, oldestToken)
		}
	}

	g.userCache[token] = cachedUser{user: user, cachedAt: time.Now()}
}

// Check if user is authenticated via JWT
func (g *JWTGuard) Check(r *http.Request) bool {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return false
	}

	claims, err := g.jwtManager.ValidateAccessToken(token)
	if err != nil {
		return false
	}

	// Validate user still exists
	user, err := g.loadProvider().FindByID(claims.UserID)
	if err != nil || user == nil {
		return false
	}

	// Cache user for this request
	g.cacheUser(token, user)
	return true
}

// User returns the authenticated user from JWT
func (g *JWTGuard) User(r *http.Request) auth.Authenticatable {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return nil
	}

	// Validate on EVERY call: a token blacklisted or expired since it was
	// first seen must stop authenticating immediately, not after the cache
	// TTL. The cache may only memoize the provider lookup, never the
	// validity decision (audit: revocation/expiry bypass window).
	claims, err := g.jwtManager.ValidateAccessToken(token)
	if err != nil {
		return nil
	}

	if user, ok := g.getCachedUser(token); ok {
		return user
	}

	user, err := g.loadProvider().FindByID(claims.UserID)
	if err != nil {
		return nil
	}

	g.cacheUser(token, user)
	return user
}

// ID returns the authenticated user ID from JWT
func (g *JWTGuard) ID(r *http.Request) interface{} {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return nil
	}

	claims, err := g.jwtManager.ValidateAccessToken(token)
	if err != nil {
		return nil
	}

	return claims.UserID
}

// Login generates a JWT token for the user
func (g *JWTGuard) Login(w http.ResponseWriter, r *http.Request, user auth.Authenticatable, remember ...bool) error {
	// Generate access token
	token, err := g.jwtManager.GenerateToken(user)
	if err != nil {
		return err
	}

	// For JWT, we typically return the token in response body
	// not as a cookie. The actual response handling should be
	// done by the controller/handler
	w.Header().Set("X-Auth-Token", token)

	// If remember is true, also generate refresh token
	if len(remember) > 0 && remember[0] {
		refreshToken, err := g.jwtManager.GenerateRefreshToken(user)
		if err != nil {
			return err
		}
		w.Header().Set("X-Refresh-Token", refreshToken)
	}

	return nil
}

// LoginByID logs in a user by ID and generates JWT
func (g *JWTGuard) LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
	user, err := g.loadProvider().FindByID(id)
	if err != nil {
		return err
	}

	return g.Login(w, r, user, remember...)
}

// Attempt validates credentials and generates JWT if valid.
// The configured LoginThrottler is consulted before the credential check;
// failures call RecordFailure, successes call RecordSuccess.
//
// The credential-check phase runs inside auth.Timebox so the missing-user
// fast path and the wrong-password slow path pad to the same wall-clock
// duration (H-09 fix). The dummy bcrypt run on the missing-user branch
// matches the CPU profile of the bcrypt verify branch.
func (g *JWTGuard) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	throttler := g.loadThrottler()
	key := auth.ThrottleKey(r, credentials, g.getTrustedProxies())
	if !throttler.Allow(r, key) {
		return false, auth.ErrLoginThrottled
	}

	// Snapshot the provider once so the timebox closure and the
	// post-timebox branches see a consistent reference even if a
	// concurrent SetProvider call swaps the pointer mid-call.
	provider := g.loadProvider()

	var (
		user            auth.Authenticatable
		findErr         error
		credentialsOK   bool
		invalidCredErr  error
		password        string
		passwordTypedOK bool
	)

	hasher := g.effectiveHasher()
	// Size the dummy hash to the configured bcrypt cost (F2 fix).
	dummyHash := dummyHashForHasher(hasher)
	auth.Timebox(g.effectiveAttemptFloor(), func() {
		user, findErr = provider.FindByCredentials(credentials)
		password, passwordTypedOK = credentials["password"].(string)

		if findErr != nil || user == nil {
			if passwordTypedOK {
				_ = hasher.Verify(password, string(dummyHash))
			} else {
				_ = hasher.Verify("", string(dummyHash))
			}
			return
		}
		if !passwordTypedOK {
			_ = hasher.Verify("", string(dummyHash))
			invalidCredErr = auth.ErrInvalidCredentials
			return
		}
		credentialsOK = provider.ValidateCredentials(user, map[string]interface{}{"password": password})
	})

	if findErr != nil || user == nil {
		throttler.RecordFailure(r, key)
		return false, nil // User not found
	}
	if invalidCredErr != nil {
		throttler.RecordFailure(r, key)
		return false, invalidCredErr
	}
	if !credentialsOK {
		throttler.RecordFailure(r, key)
		return false, nil // Invalid password
	}

	// Generate token (post-timebox; success path varies by token
	// generation time, which leaks "succeeded" but not user identity).
	if err := g.Login(w, r, user, remember...); err != nil {
		return false, err
	}

	// Hash-staleness check (M-08): emit PasswordNeedsRehashEvent when the
	// stored hash no longer matches the configured Hasher parameters.
	g.maybeEmitRehashEvent(r.Context(), hasher, user)

	throttler.RecordSuccess(r, key)
	return true, nil
}

// maybeEmitRehashEvent fires auth.PasswordNeedsRehashEvent through the
// installed dispatcher when hasher.NeedsRehash reports the stored hash
// is out of date. No-op when no dispatcher has been wired. A dispatcher
// error is swallowed: a transient subscriber failure must not block the
// already-successful login.
func (g *JWTGuard) maybeEmitRehashEvent(ctx context.Context, hasher auth.Hasher, user auth.Authenticatable) {
	dispatcher := g.getEventDispatcher()
	if dispatcher == nil || hasher == nil || user == nil {
		return
	}
	if !hasher.NeedsRehash(user.GetAuthPassword()) {
		return
	}
	_ = dispatcher(ctx, auth.PasswordNeedsRehashEvent{
		UserID:    user.GetAuthIdentifier(),
		GuardName: "jwt",
	})
}

// RevokeAllRefreshTokensForUser implements auth.RefreshTokenRevoker. It
// bumps the per-user refresh-token generation counter so every
// outstanding refresh token issued under a lower generation is rejected
// on its next /auth/refresh call. The same mechanism as JWTGuard.Logout
// uses for the single-user single-Logout case; the difference is the
// trigger (administrative "sign out everywhere" vs. user-initiated
// logout).
//
// Without this hook, Manager.RevokeAllSessions deleted server-side
// session records and cleared session-guard remember-me tokens but left
// outstanding refresh tokens valid until their natural expiry (default
// 14 days). A phished refresh token therefore survived the
// administrative purge and re-minted fresh access tokens for the
// attacker (audit M-10).
//
// userID is the string form to match RememberTokenClearer / DeleteAllForUser.
func (g *JWTGuard) RevokeAllRefreshTokensForUser(_ context.Context, userID string) error {
	if userID == "" {
		return nil
	}
	if g.jwtManager == nil {
		return nil
	}
	_, err := g.jwtManager.BumpRefreshGeneration(userID)
	return err
}

// Logout revokes the JWT token
//
// In addition to blacklisting the access token's JTI, Logout bumps the
// user's refresh-token generation so every outstanding refresh token for
// the user is rejected on the next /auth/refresh call (audit H-07).
// Without the bump a phished refresh token would survive Logout for up
// to RefreshTTL (default 14 days).
func (g *JWTGuard) Logout(w http.ResponseWriter, r *http.Request) error {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return nil
	}

	// Parse token to get JTI
	claims, err := g.jwtManager.ValidateToken(token)
	if err != nil {
		// Even if token is invalid, we proceed with logout
		return nil
	}

	// Revoke token using its actual expiry for blacklist duration
	g.jwtManager.RevokeToken(claims.ID, claims.ExpiresAt.Time)

	// Bump the user's refresh-token generation so every outstanding
	// refresh token for this user is invalidated on its next use.
	// Best-effort: a counter-store transport error is swallowed so
	// Logout still completes; the access JTI is already blacklisted
	// above, so the immediate logout still has effect.
	userIDStr, _ := claims.UserID.(string)
	if userIDStr == "" && claims.UserID != nil {
		userIDStr = fmt.Sprintf("%v", claims.UserID)
	}
	if userIDStr != "" {
		_, _ = g.jwtManager.BumpRefreshGeneration(userIDStr)
	}

	// Clear cache
	g.mu.Lock()
	delete(g.userCache, token)
	g.mu.Unlock()

	return nil
}

// SetProvider sets the user provider. Stored via atomic.Pointer so
// concurrent Attempt / User / Check readers cannot tear the two-word
// interface fetch on the provider field (H-10 fix). Passing nil leaves
// the previously installed provider in place.
func (g *JWTGuard) SetProvider(provider auth.UserProvider) {
	if provider == nil {
		return
	}
	g.provider.Store(&providerHolder{p: provider})
}

// SetRefreshGenerationStore installs a shared per-user refresh-generation
// counter store on the underlying JWT manager. Use a Redis-backed (or
// otherwise cross-host) implementation in multi-host deployments so the
// H-07 Logout bump propagates: without a shared store, a stolen refresh
// token will still refresh successfully on hosts that did not handle the
// Logout call.
//
// Passing nil reverts to the in-process InMemoryRefreshGenerationStore
// (single-host scope, lost on restart). Safe for concurrent use.
//
// Forwards to JWTManager.SetRefreshGenerationStore, which itself stores
// the value under a mutex so concurrent Refresh / Logout callers cannot
// tear the interface read.
func (g *JWTGuard) SetRefreshGenerationStore(store auth.RefreshGenerationStore) {
	g.jwtManager.SetRefreshGenerationStore(store)
}

// GenerateToken generates a JWT token for a user
func (g *JWTGuard) GenerateToken(user auth.Authenticatable, claims ...map[string]interface{}) (string, error) {
	return g.jwtManager.GenerateToken(user, claims...)
}

// GenerateRefreshToken generates a refresh token
func (g *JWTGuard) GenerateRefreshToken(user auth.Authenticatable) (string, error) {
	return g.jwtManager.GenerateRefreshToken(user)
}

// RefreshToken refreshes an access token using refresh token
func (g *JWTGuard) RefreshToken(refreshToken string) (string, error) {
	return g.jwtManager.RefreshToken(refreshToken, g.loadProvider())
}

// ValidateToken validates a JWT token
func (g *JWTGuard) ValidateToken(token string) (*auth.Claims, error) {
	return g.jwtManager.ValidateToken(token)
}

// getTokenFromRequest extracts JWT token from request.
//
// For WebSocket upgrade requests, clients should carry the access token in the
// Sec-WebSocket-Protocol header as a comma-separated subprotocol value with the
// form "bearer.<token>". The legacy "?token=" query parameter is accepted for
// WebSocket upgrades ONLY when JWTConfig.AllowQueryToken is explicitly enabled;
// it is off by default because query-string credentials can leak through access
// logs, proxy logs, browser history, and Referer headers.
func (g *JWTGuard) getTokenFromRequest(r *http.Request) string {
	// Check Authorization header — only accept "Bearer <token>" format
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		if !strings.HasPrefix(authHeader, "Bearer ") {
			return ""
		}
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	// Check X-Auth-Token header
	if token := r.Header.Get("X-Auth-Token"); token != "" {
		return token
	}

	// Check WebSocket-specific token sources. Sec-WebSocket-Protocol
	// ("bearer.<token>") is always accepted. The "?token=" query parameter is
	// accepted ONLY when JWTConfig.AllowQueryToken is explicitly enabled
	// (default off): query-string credentials leak into LB/proxy/access logs,
	// browser history, and Referer headers.
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		if token := tokenFromWebSocketSubprotocol(r.Header.Values("Sec-WebSocket-Protocol")); token != "" {
			return token
		}
		if g.config.AllowQueryToken {
			if token := r.URL.Query().Get("token"); token != "" {
				return token
			}
		}
	}

	// Check form value
	if r.Method == "POST" {
		if token := r.FormValue("token"); token != "" {
			return token
		}
	}

	return ""
}

func tokenFromWebSocketSubprotocol(values []string) string {
	const bearerPrefix = "bearer."

	for _, headerValue := range values {
		for _, value := range strings.Split(headerValue, ",") {
			value = strings.TrimSpace(value)
			if strings.HasPrefix(value, bearerPrefix) {
				token := strings.TrimPrefix(value, bearerPrefix)
				if token != "" {
					return token
				}
			}
		}
	}

	return ""
}
