package schemes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
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

	// jwtMaxFormBodyBytes caps how much of a POST body the form-token
	// extractor will buffer while looking for a "token" field. Mirrors
	// csrf.DefaultMaxFormBodyBytes; bodies beyond the cap are never
	// slurped into memory and yield no token.
	jwtMaxFormBodyBytes = 1 << 20 // 1 MiB
)

type cachedUser struct {
	user     auth.Authenticatable
	cachedAt time.Time
}

// JWTScheme implements JWT-based authentication for APIs
type JWTScheme struct {
	// user store and throttler are held via atomic.Pointer so concurrent
	// SetUserStore / SetLoginThrottler calls cannot tear a reader's
	// two-word interface fetch in Attempt / User / Check (H-10 fix).
	userStore atomic.Pointer[userStoreHolder]
	throttler atomic.Pointer[throttlerHolder]

	jwtManager     *auth.JWTManager
	config         auth.JWTConfig
	mu             sync.RWMutex
	userCache      map[string]cachedUser
	stopCleanup    chan struct{}
	stopOnce       sync.Once
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

// loadUserStore returns the active auth.UserStore via atomic load.
func (g *JWTScheme) loadUserStore() auth.UserStore {
	h := g.userStore.Load()
	if h == nil {
		return nil
	}
	return h.p
}

// loadThrottler returns the active contract.LoginThrottler via atomic load.
// Falls back to NoopLoginThrottler when no throttler has been installed.
func (g *JWTScheme) loadThrottler() contract.LoginThrottler {
	h := g.throttler.Load()
	if h == nil || h.t == nil {
		return auth.NoopLoginThrottler{}
	}
	return h.t
}

// SetAttemptFloor configures the wall-clock floor that Attempt blocks for.
// See auth.Config.AttemptFloor for the threat model.
func (g *JWTScheme) SetAttemptFloor(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.attemptFloor = d
}

func (g *JWTScheme) effectiveAttemptFloor() time.Duration {
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

func (g *JWTScheme) effectiveHasher() auth.Hasher {
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
func (g *JWTScheme) SetHasher(h auth.Hasher) {
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
// Manager.SetTrustedProxies propagates to every registered scheme via
// the auth.TrustedProxiesReceiver interface, so consumers normally do
// not need to call this directly.
func (g *JWTScheme) SetTrustedProxies(proxies []*net.IPNet) {
	// Deep-clone (see SessionScheme.SetTrustedProxies for rationale).
	cloned := clientip.CloneIPNets(proxies)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.trustedProxies = cloned
}

// getTrustedProxies returns the installed trusted-proxy list under a
// read lock so concurrent Attempt() calls see a consistent snapshot.
// The returned slice is a deep clone (see SessionScheme.getTrustedProxies).
func (g *JWTScheme) getTrustedProxies() []*net.IPNet {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return clientip.CloneIPNets(g.trustedProxies)
}

// SetLoginThrottler installs a rate-limiter for Attempt() calls. Passing nil
// reverts to the no-op throttler.
//
// Stored via atomic.Pointer so concurrent Attempt() readers cannot tear the
// two-word interface fetch on the throttler field (H-10 fix).
func (g *JWTScheme) SetLoginThrottler(t contract.LoginThrottler) {
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
// Manager.SetEventDispatcher propagates to every registered scheme via
// the auth.EventDispatcherReceiver interface; consumers normally do not
// need to call this directly.
func (g *JWTScheme) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.eventDispatcher = fn
}

// getEventDispatcher returns the installed dispatcher under a read lock
// so concurrent Attempt() readers observe a consistent value across a
// SetEventDispatcher swap.
func (g *JWTScheme) getEventDispatcher() func(ctx context.Context, event any) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.eventDispatcher
}

// NewJWTScheme creates a new JWT scheme.
// Call Start() to begin the background cache cleanup goroutine.
// Returns an error when the underlying JWTConfig fails validation.
func NewJWTScheme(userStore auth.UserStore, config auth.JWTConfig) (*JWTScheme, error) {
	manager, err := auth.NewJWTManager(config)
	if err != nil {
		return nil, err
	}
	g := &JWTScheme{
		jwtManager:  manager,
		config:      config,
		userCache:   make(map[string]cachedUser),
		stopCleanup: make(chan struct{}),
	}
	g.userStore.Store(&userStoreHolder{p: userStore})
	g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
	return g, nil
}

// Start begins the background cache cleanup goroutine. An optional
// context.Context controls the goroutine lifetime; if none is provided,
// use StopCleanup() to stop it.
func (g *JWTScheme) Start(ctx ...context.Context) {
	if len(ctx) > 0 && ctx[0] != nil {
		bg := ctx[0]
		async.Go(func() { g.cleanupLoopWithContext(bg) })
	} else {
		async.Go(func() { g.cleanupLoop() })
	}
}

// StopCleanup stops the background cache cleanup goroutine. Idempotent
// and safe for concurrent use: the close is guarded by a sync.Once
// because the previous select-then-close pattern let two concurrent
// callers both observe "not closed" and both reach close (panic).
func (g *JWTScheme) StopCleanup() {
	g.stopOnce.Do(func() {
		close(g.stopCleanup)
	})
}

func (g *JWTScheme) cleanupLoop() {
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

func (g *JWTScheme) cleanupLoopWithContext(ctx context.Context) {
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

func (g *JWTScheme) evictExpired() {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for token, entry := range g.userCache {
		if now.Sub(entry.cachedAt) > jwtCacheTTL {
			delete(g.userCache, token)
		}
	}
}

func (g *JWTScheme) getCachedUser(token string) (auth.Authenticatable, bool) {
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

func (g *JWTScheme) cacheUser(token string, user auth.Authenticatable) {
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
func (g *JWTScheme) Check(r *http.Request) bool {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return false
	}

	claims, err := g.jwtManager.ValidateAccessToken(token)
	if err != nil {
		return false
	}

	// Validate user still exists
	user, err := g.loadUserStore().FindByIDCtx(r.Context(), claims.UserID)
	if err != nil || user == nil {
		return false
	}

	// Cache user for this request
	g.cacheUser(token, user)
	return true
}

// User returns the authenticated user from JWT
func (g *JWTScheme) User(r *http.Request) auth.Authenticatable {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return nil
	}

	// Validate on EVERY call: a token blacklisted or expired since it was
	// first seen must stop authenticating immediately, not after the cache
	// TTL. The cache may only memoize the user store lookup, never the
	// validity decision (audit: revocation/expiry bypass window).
	claims, err := g.jwtManager.ValidateAccessToken(token)
	if err != nil {
		return nil
	}

	if user, ok := g.getCachedUser(token); ok {
		return user
	}

	user, err := g.loadUserStore().FindByIDCtx(r.Context(), claims.UserID)
	if err != nil || user == nil {
		// FindByIDCtx may return (nil, nil) for an unknown id; a nil
		// user must not be cached as an authenticated entry.
		return nil
	}

	g.cacheUser(token, user)
	return user
}

// ID returns the authenticated user ID from JWT
func (g *JWTScheme) ID(r *http.Request) interface{} {
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
func (g *JWTScheme) Login(w http.ResponseWriter, r *http.Request, user auth.Authenticatable, remember ...bool) error {
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
func (g *JWTScheme) LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
	user, err := g.loadUserStore().FindByIDCtx(r.Context(), id)
	if err != nil {
		return err
	}
	// FindByIDCtx may return (nil, nil) for an unknown id. Surface that as
	// an error here so we never pass a nil user into Login (which would
	// panic on the GenerateToken claims deref).
	if user == nil {
		return auth.ErrUserNotFound
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
func (g *JWTScheme) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	// Snapshot throttler, user store, and hasher once so the credential
	// check and the success tail below see consistent references even if
	// a concurrent Set* call swaps one mid-call.
	throttler := g.loadThrottler()
	hasher := g.effectiveHasher()
	user, keys, ok, err := attemptCredentials(r, credentials, g.loadUserStore(), hasher, throttler, g.effectiveAttemptFloor(), g.getTrustedProxies())
	if !ok {
		return false, err
	}

	// Generate token (post-timebox; success path varies by token
	// generation time, which leaks "succeeded" but not user identity).
	if err := g.Login(w, r, user, remember...); err != nil {
		return false, err
	}

	// Hash-staleness check (M-08): emit PasswordNeedsRehashEvent when the
	// stored hash no longer matches the configured Hasher parameters. A
	// dispatch error is swallowed (nil warn): a transient subscriber
	// failure must not block the already-successful login.
	maybeEmitRehashEvent(r.Context(), g.getEventDispatcher(), hasher, user, "jwt", nil)

	for _, key := range keys {
		throttler.RecordSuccess(r, key)
	}
	return true, nil
}

// RevokeAllRefreshTokensForUser implements auth.RefreshTokenRevoker. It
// bumps the per-user refresh-token generation counter so every
// outstanding refresh token issued under a lower generation is rejected
// on its next /auth/refresh call. The same mechanism as JWTScheme.Logout
// uses for the single-user single-Logout case; the difference is the
// trigger (administrative "sign out everywhere" vs. user-initiated
// logout).
//
// Without this hook, Manager.RevokeAllSessions deleted server-side
// session records and cleared session-scheme remember-me tokens but left
// outstanding refresh tokens valid until their natural expiry (default
// 14 days). A phished refresh token therefore survived the
// administrative purge and re-minted fresh access tokens for the
// attacker (audit M-10).
//
// userID is the string form to match RememberTokenClearer / DeleteAllForUser.
func (g *JWTScheme) RevokeAllRefreshTokensForUser(_ context.Context, userID string) error {
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
func (g *JWTScheme) Logout(w http.ResponseWriter, r *http.Request) error {
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

// SetUserStore sets the user store. Stored via atomic.Pointer so
// concurrent Attempt / User / Check readers cannot tear the two-word
// interface fetch on the user store field (H-10 fix). Passing nil leaves
// the previously installed user store in place.
func (g *JWTScheme) SetUserStore(userStore auth.UserStore) {
	if userStore == nil {
		return
	}
	g.userStore.Store(&userStoreHolder{p: userStore})
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
func (g *JWTScheme) SetRefreshGenerationStore(store auth.RefreshGenerationStore) {
	g.jwtManager.SetRefreshGenerationStore(store)
}

// GenerateToken generates a JWT token for a user
func (g *JWTScheme) GenerateToken(user auth.Authenticatable, claims ...map[string]interface{}) (string, error) {
	return g.jwtManager.GenerateToken(user, claims...)
}

// GenerateRefreshToken generates a refresh token
func (g *JWTScheme) GenerateRefreshToken(user auth.Authenticatable) (string, error) {
	return g.jwtManager.GenerateRefreshToken(user)
}

// RefreshToken refreshes an access token using refresh token
func (g *JWTScheme) RefreshToken(refreshToken string) (string, error) {
	return g.jwtManager.RefreshToken(refreshToken, g.loadUserStore())
}

// ValidateToken validates a JWT token
func (g *JWTScheme) ValidateToken(token string) (*auth.Claims, error) {
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
func (g *JWTScheme) getTokenFromRequest(r *http.Request) string {
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

	// Check form value (bounded; see tokenFromFormBody).
	if r.Method == http.MethodPost {
		if token := tokenFromFormBody(r); token != "" {
			return token
		}
	}

	return ""
}

// tokenFromFormBody extracts a "token" field from a POST form body
// without the unbounded read and body consumption of r.FormValue.
//
// Only application/x-www-form-urlencoded bodies are parsed; multipart
// bodies are never touched (r.FormValue would invoke ParseMultipartForm
// with its 32 MiB default memory limit before any auth decision, and
// would also consume the stream the handler needs). Query-string tokens
// are NOT consulted: outside the WebSocket AllowQueryToken opt-in,
// query-string credentials leak into access logs and Referer headers.
//
// The read is capped at jwtMaxFormBodyBytes via io.LimitReader (not
// http.MaxBytesReader: on overflow MaxBytesReader truncates the byte
// count of its final Read, so the consumed prefix could not be restored
// losslessly). Within the cap, r.Body is restored as a re-readable
// buffer so the downstream handler still sees the full body. On overflow
// or a transport error, extraction bails (no token) and r.Body is
// restored as the consumed prefix stitched to the unread remainder.
func tokenFromFormBody(r *http.Request) string {
	if r.Body == nil || r.Body == http.NoBody {
		return ""
	}

	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return ""
	}

	origBody := r.Body
	// Read at most cap+1 bytes: a full cap+1 read means the body exceeds
	// the cap. LimitReader never drops bytes, so whatever was consumed
	// can be stitched back in front of the remainder below.
	buf, readErr := io.ReadAll(io.LimitReader(origBody, jwtMaxFormBodyBytes+1)) //nolint:forbidigo // bounded by io.LimitReader above
	if readErr != nil || int64(len(buf)) > jwtMaxFormBodyBytes {
		// Oversize or failed mid-read: bail without parsing. Restore the
		// consumed prefix ahead of the unread remainder so a downstream
		// reader still observes the original stream.
		r.Body = struct {
			io.Reader
			io.Closer
		}{
			Reader: io.MultiReader(bytes.NewReader(buf), origBody),
			Closer: origBody,
		}
		return ""
	}

	// Within the cap: restore r.Body as a re-readable buffer so the
	// handler can re-parse the form.
	r.Body = struct {
		io.Reader
		io.Closer
	}{
		Reader: bytes.NewReader(buf),
		Closer: origBody,
	}

	values, err := url.ParseQuery(string(buf))
	if err != nil {
		return ""
	}
	return values.Get("token")
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
