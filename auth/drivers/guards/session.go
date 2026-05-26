package guards

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/clientip"
)

// rememberRandReader is the entropy source for remember-me tokens. Tests may
// swap this out to simulate rand.Read failures.
var rememberRandReader io.Reader = rand.Reader

// sessionCtxKey is an unexported context key type to avoid collisions.
type sessionCtxKey struct{}

// sessionHolder is a mutable container for session data stored in request context.
// It also caches the result of the server-side session store lookup so that
// multiple guard methods invoked on the same request (Check, then User, then
// ID) only pay the Redis round-trip once.
type sessionHolder struct {
	session   auth.Session
	storeOnce bool
	storeRec  *auth.StoredSession
	storeErr  error
}

// WithSessionContext returns a new request with a session cache attached to its context.
// Call this from middleware to enable per-request session caching that is automatically
// cleaned up when the request completes.
func WithSessionContext(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionCtxKey{}, &sessionHolder{}))
}

// sessionFromHolder returns the session cached on r's holder, or nil when no
// handler in the request resolved one (the holder was attached but
// SessionGuard.getSession was never called). Test helper / middleware helper
// only; nil is a normal outcome.
func sessionFromHolder(r *http.Request) auth.Session {
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		return nil
	}
	return holder.session
}

// modifiedSession is the optional capability the save-at-end middleware uses
// to skip writing a Set-Cookie header for sessions that no handler touched.
// *auth.BaseSession (and therefore session.CookieSession via embedding)
// satisfies it; mock sessions in tests can opt in by exposing IsModified().
type modifiedSession interface {
	IsModified() bool
	IsDestroyed() bool
}

// lastSeenDebounce is the minimum interval between LastSeenAt write-backs
// for a given session. Reads happen on every authenticated request to honor
// revocation; writes are debounced so a chatty client does not generate one
// extra Redis Put per request. 60s gives the "active sessions" UI accurate
// timestamps without amplifying write volume.
const lastSeenDebounce = 60 * time.Second

// SessionGuard implements session-based authentication
type SessionGuard struct {
	provider       auth.UserProvider
	store          auth.SessionStore
	config         auth.SessionConfig
	hasher         auth.Hasher
	encryptor      crypto.Encryptor
	throttler      contract.LoginThrottler
	mu             sync.RWMutex
	serverStore    auth.ServerSessionStore
	logger         auth.Logger
	trustedProxies []*net.IPNet
}

// NewSessionGuard creates a new session guard.
// The encryptor parameter is optional — pass nil if crypto is not configured
// (session guard will still work if a non-cookie store is used later).
func NewSessionGuard(provider auth.UserProvider, config auth.SessionConfig, encryptor ...crypto.Encryptor) (*SessionGuard, error) {
	var enc crypto.Encryptor
	if len(encryptor) > 0 {
		enc = encryptor[0]
	}

	store, err := session.NewCookieStore(config, enc)
	if err != nil {
		return nil, err
	}

	return &SessionGuard{
		provider:  provider,
		store:     store,
		config:    config,
		hasher:    auth.NewBcryptHasher(10),
		encryptor: enc,
		throttler: auth.NoopLoginThrottler{},
	}, nil
}

// SetLoginThrottler installs a rate-limiter for Attempt() calls. Passing nil
// reverts to the no-op throttler.
func (g *SessionGuard) SetLoginThrottler(t contract.LoginThrottler) {
	if t == nil {
		g.throttler = auth.NoopLoginThrottler{}
		return
	}
	g.throttler = t
}

// SetServerSessionStore installs (or removes when nil) a server-side session
// store. When set, the guard records sessions on Login, looks them up on
// Check/User to honor administrative revocations, and deletes them on
// Logout. Cookie-only behavior is preserved when the store is nil.
//
// Manager.SetServerSessionStore propagates to every registered guard via
// the auth.ServerSessionStoreReceiver interface, so consumers normally do
// not need to call this directly.
func (g *SessionGuard) SetServerSessionStore(store auth.ServerSessionStore) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.serverStore = store
}

// SetTrustedProxies installs the parsed proxy-network list used for
// client-IP resolution in the login throttler and the audit-trail IP
// recorded on Login. Pass nil to revert to "no proxies trusted"
// (forwarded headers are ignored, RemoteAddr is used verbatim).
//
// Manager.SetTrustedProxies propagates to every registered guard via
// the auth.TrustedProxiesReceiver interface, so consumers normally do
// not need to call this directly.
func (g *SessionGuard) SetTrustedProxies(proxies []*net.IPNet) {
	// Deep-clone so caller mutation of any *net.IPNet's IP / Mask
	// (or the slice header) cannot flip the guard's trust decisions
	// at runtime. A shallow []*net.IPNet copy would reuse the same
	// IPNet pointers and re-expose the audit-finding hole.
	cloned := clientip.CloneIPNets(proxies)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.trustedProxies = cloned
}

// getTrustedProxies returns the installed trusted-proxy list under a
// read lock so concurrent Attempt() / Login() calls see a consistent
// snapshot. Returns a deep clone so the caller cannot mutate the
// guard's state by editing the returned slice or its IPNet elements.
// Returns nil when none has been configured.
func (g *SessionGuard) getTrustedProxies() []*net.IPNet {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return clientip.CloneIPNets(g.trustedProxies)
}

// SetLogger installs a logger used for non-fatal store errors (e.g. Redis
// transient failure on Put). Nil disables logging.
func (g *SessionGuard) SetLogger(l auth.Logger) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.logger = l
}

// getServerStore returns the installed server-side session store, or nil
// when none has been configured.
func (g *SessionGuard) getServerStore() auth.ServerSessionStore {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.serverStore
}

// logWarn emits a warn event when a logger is configured. Safe to call
// when no logger has been installed.
func (g *SessionGuard) logWarn(msg string, kvs ...any) {
	g.mu.RLock()
	l := g.logger
	g.mu.RUnlock()
	if l != nil {
		l.Warn(msg, kvs...)
	}
}

// Check reports whether the request is authenticated. When a server-side
// session store has been installed, it is consulted on every call: a
// revoked or expired record causes Check to return false even though the
// cookie itself is still valid. Errors (including ErrSessionRevoked) are
// swallowed; callers that need to distinguish causes should use
// CheckWithError instead.
func (g *SessionGuard) Check(r *http.Request) bool {
	ok, _ := g.CheckWithError(r)
	return ok
}

// CheckWithError reports whether the request is authenticated and, when not,
// returns the reason. The returned error is one of:
//
//   - nil: request is unauthenticated for ordinary reasons (no cookie, bad
//     cookie, missing user_id, user no longer exists)
//   - auth.ErrSessionRevoked: cookie is valid but the matching server-side
//     session record was deleted or expired (e.g. via Manager.RevokeSession)
//   - any other error: server-side store lookup failed; fail-closed
//     (returns false). The underlying error is logged when a logger is
//     configured.
//
// Use this from middleware to deliver a "your session was signed out
// remotely" UX without breaking the Guard interface.
func (g *SessionGuard) CheckWithError(r *http.Request) (bool, error) {
	session := g.getSession(r)
	if session == nil {
		return false, nil
	}

	userID := session.Get("user_id")
	if userID == nil {
		// Remember-cookie fallback bypasses the server store: the
		// derived session is not registered there, and the path is
		// already considered a re-auth (a fresh Login should follow
		// to anchor the session in the store).
		return g.checkRememberCookie(r) != nil, nil
	}

	user, err := g.provider.FindByID(userID)
	if err != nil || user == nil {
		return false, nil
	}

	if err := g.consultServerStore(r, session); err != nil {
		return false, err
	}
	return true, nil
}

// User returns the authenticated user, or nil when the request is not
// authenticated. When a server-side session store is configured, a revoked
// or missing record causes User to return nil even when the cookie is
// otherwise valid.
func (g *SessionGuard) User(r *http.Request) auth.Authenticatable {
	session := g.getSession(r)
	if session == nil {
		return nil
	}

	userID := session.Get("user_id")
	if userID == nil {
		// Try remember cookie
		user := g.checkRememberCookie(r)
		if user != nil {
			// Re-establish session for the remembered user
			session.Put("user_id", user.GetAuthIdentifier())
			return user
		}
		return nil
	}

	user, err := g.provider.FindByID(userID)
	if err != nil {
		return nil
	}

	if err := g.consultServerStore(r, session); err != nil {
		return nil
	}
	return user
}

// ID returns the authenticated user ID
func (g *SessionGuard) ID(r *http.Request) interface{} {
	session := g.getSession(r)
	if session == nil {
		return nil
	}

	return session.Get("user_id")
}

// Login logs in a user
func (g *SessionGuard) Login(w http.ResponseWriter, r *http.Request, user auth.Authenticatable, remember ...bool) error {
	session := g.getSession(r)
	if session == nil {
		var err error
		session, err = g.store.Create("")
		if err != nil {
			return err
		}
		// Cache in request context if available
		if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok {
			holder.session = session
		}
	}

	// Regenerate session ID for security. A failure here must abort the
	// login: proceeding with the old session ID opens a session-fixation
	// window (an attacker who planted the cookie keeps access).
	if err := session.Regenerate(); err != nil {
		return fmt.Errorf("velocity/auth: login aborted: session regenerate failed: %w", err)
	}

	// Store user ID in session
	session.Put("user_id", user.GetAuthIdentifier())

	// Handle remember me
	if len(remember) > 0 && remember[0] {
		if err := g.setRememberCookie(w, user); err != nil {
			return err
		}
	}

	// Save session
	if err := session.Save(w); err != nil {
		return err
	}

	g.recordServerSession(r, session, user)
	return nil
}

// LoginByID logs in a user by ID
func (g *SessionGuard) LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
	user, err := g.provider.FindByID(id)
	if err != nil {
		return err
	}

	return g.Login(w, r, user, remember...)
}

// Attempt attempts to log in with credentials. The configured LoginThrottler
// is consulted before the credential check; failed attempts call
// RecordFailure and successes call RecordSuccess.
func (g *SessionGuard) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	throttler := g.throttler
	if throttler == nil {
		throttler = auth.NoopLoginThrottler{}
	}
	key := auth.ThrottleKey(r, credentials, g.getTrustedProxies())
	if !throttler.Allow(r, key) {
		return false, auth.ErrLoginThrottled
	}

	// Find user by credentials
	user, err := g.provider.FindByCredentials(credentials)
	if err != nil {
		throttler.RecordFailure(r, key)
		return false, nil // User not found
	}

	// Validate password
	password, ok := credentials["password"].(string)
	if !ok {
		throttler.RecordFailure(r, key)
		return false, auth.ErrInvalidCredentials
	}

	if !g.provider.ValidateCredentials(user, map[string]interface{}{"password": password}) {
		throttler.RecordFailure(r, key)
		return false, nil // Invalid password
	}

	// Login user
	if err := g.Login(w, r, user, remember...); err != nil {
		return false, err
	}

	throttler.RecordSuccess(r, key)
	return true, nil
}

// Logout logs out the user
func (g *SessionGuard) Logout(w http.ResponseWriter, r *http.Request) error {
	session := g.getSession(r)
	if session == nil {
		return nil
	}

	// Capture the session ID before Invalidate so we can also tear down
	// the server-side record. BaseSession.Invalidate currently leaves id
	// intact, but capturing here is robust against future changes.
	sessionID := session.ID()

	// Clear remember cookie
	g.clearRememberCookie(w)

	// Invalidate session
	if err := session.Invalidate(); err != nil {
		return err
	}

	// Save invalidated session (will delete cookie)
	if err := session.Save(w); err != nil {
		return err
	}

	if store := g.getServerStore(); store != nil && sessionID != "" {
		if err := store.Delete(r.Context(), sessionID); err != nil {
			g.logWarn("velocity/auth: server session store delete (logout) failed", "session_id", sessionID, "error", err)
		}
	}
	return nil
}

// SetProvider sets the user provider
func (g *SessionGuard) SetProvider(provider auth.UserProvider) {
	g.provider = provider
}

// Session returns the request-scoped Session, loading from the cookie store
// on first call and caching it in the request context for subsequent calls
// when WithSessionContext has been applied to the request.
//
// Implements the auth.SessionAware capability so auth.Manager.Session(r)
// can surface the session bag (including Flash / GetFlash / FlushFlash)
// without consumers reaching into the guard directly.
func (g *SessionGuard) Session(r *http.Request) auth.Session {
	return g.getSession(r)
}

// getSession gets or creates session for request
func (g *SessionGuard) getSession(r *http.Request) auth.Session {
	// Check request context cache first
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder.session != nil {
		return holder.session
	}

	// Get from store
	session, err := auth.GetSessionFromRequest(r, g.store, g.config.Name)
	if err != nil {
		return nil
	}

	// Cache in request context if available
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok {
		holder.session = session
	}

	return session
}

// consultServerStore enforces server-side session revocation. When a store
// has been installed, every authenticated request looks up the session by
// id; a missing or expired record returns ErrSessionRevoked. The Get result
// is cached on the request-scoped sessionHolder so multiple guard methods
// in the same request only pay one round-trip. LastSeenAt is refreshed on
// the underlying store at most once per lastSeenDebounce interval.
//
// Returns nil when no store is configured (cookie-only mode preserved).
func (g *SessionGuard) consultServerStore(r *http.Request, session auth.Session) error {
	store := g.getServerStore()
	if store == nil {
		return nil
	}

	holder, _ := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if holder != nil && holder.storeOnce {
		return holder.storeErr
	}

	sessionID := session.ID()
	if sessionID == "" {
		// A session with no id cannot be looked up; treat as revoked
		// (the cookie cannot have come from a successful Login).
		if holder != nil {
			holder.storeOnce = true
			holder.storeErr = auth.ErrSessionRevoked
		}
		return auth.ErrSessionRevoked
	}

	rec, err := store.Get(r.Context(), sessionID)
	if err != nil {
		var resolved error
		if errors.Is(err, auth.ErrSessionNotFound) || errors.Is(err, auth.ErrSessionExpired) {
			resolved = auth.ErrSessionRevoked
		} else {
			g.logWarn("velocity/auth: server session store get failed", "session_id", sessionID, "error", err)
			resolved = fmt.Errorf("velocity/auth: server session store get: %w", err)
		}
		if holder != nil {
			holder.storeOnce = true
			holder.storeErr = resolved
		}
		return resolved
	}

	if holder != nil {
		holder.storeOnce = true
		holder.storeRec = rec
	}

	g.maybeRefreshLastSeen(r.Context(), store, rec)
	return nil
}

// maybeRefreshLastSeen writes a debounced LastSeenAt update back to the
// store. The debounce keeps the read on every request (mandatory for
// revocation) without doubling the round-trips.
func (g *SessionGuard) maybeRefreshLastSeen(ctx context.Context, store auth.ServerSessionStore, rec *auth.StoredSession) {
	if rec == nil {
		return
	}
	if time.Since(rec.LastSeenAt) < lastSeenDebounce {
		return
	}
	updated := *rec
	updated.LastSeenAt = time.Now()
	if err := store.Put(ctx, &updated); err != nil {
		g.logWarn("velocity/auth: server session store put (lastseen) failed", "session_id", rec.ID, "error", err)
	}
}

// recordServerSession writes the freshly-issued session to the server-side
// store on Login. Failures are logged and swallowed so a transient store
// outage does not break login (the cookie is already issued; the user is
// authenticated for this request and subsequent reads will fail-closed).
//
// Note on re-Login (e.g. password change followed by re-issue on the same
// request): session.Regenerate() inside Login already produced a fresh id,
// so this writes a brand-new record. The previous row is left for the
// MemoryStore sweep / Redis TTL to reap; the "active sessions" listing
// may briefly show two rows for the same user. Acceptable trade-off vs.
// tracking the prior id across the regenerate boundary.
func (g *SessionGuard) recordServerSession(r *http.Request, session auth.Session, user auth.Authenticatable) {
	store := g.getServerStore()
	if store == nil {
		return
	}
	sessionID := session.ID()
	if sessionID == "" {
		g.logWarn("velocity/auth: server session store skipped (empty session id)")
		return
	}
	userID, ok := user.GetAuthIdentifier().(string)
	if !ok {
		userID = fmt.Sprintf("%v", user.GetAuthIdentifier())
	}
	now := time.Now()
	ttl := time.Duration(g.config.Lifetime) * time.Minute
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	rec := &auth.StoredSession{
		ID:         sessionID,
		UserID:     userID,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(ttl),
		IPAddress:  g.clientIP(r),
		UserAgent:  r.Header.Get("User-Agent"),
	}
	if err := store.Put(r.Context(), rec); err != nil {
		g.logWarn("velocity/auth: server session store put (login) failed", "session_id", sessionID, "error", err)
	}
}

// ClearRememberTokensForUser implements auth.RememberTokenClearer. It
// resets the user's persistent remember-me token via the configured
// UserProvider so a "sign out everywhere" admin action also invalidates
// the remember cookie path. A missing user is treated as a no-op (the
// remember credential cannot resurrect what does not exist), so the
// caller (Manager.RevokeAllSessions) does not surface a confusing error
// for already-deleted accounts.
//
// Note: remember tokens are per-user, not per-session. This nukes the
// token across every device, which is the intended behavior for
// RevokeAllSessions but is why Manager.RevokeSession (single-session)
// deliberately does NOT call this method.
func (g *SessionGuard) ClearRememberTokensForUser(ctx context.Context, userID string) error {
	user, err := g.provider.FindByID(userID)
	if err != nil || user == nil {
		return nil
	}
	return g.provider.UpdateRememberToken(user, "")
}

// clientIP returns the originating client IP for r, honouring the
// guard's configured trusted-proxy list. When no proxies are trusted
// (the default), the result is the host portion of r.RemoteAddr with
// the ephemeral TCP port stripped. When the request arrives from a
// trusted proxy, RFC 7239 Forwarded / X-Forwarded-For / X-Real-IP
// resolution kicks in (see internal/clientip).
//
// The string is recorded on auth.StoredSession.IPAddress so audit
// listings show the real client, not the load balancer, and so the
// administrative "Sign out everywhere" UX surfaces meaningful IPs.
// Returns "" when r.RemoteAddr is unparseable.
func (g *SessionGuard) clientIP(r *http.Request) string {
	return clientip.ExtractString(r, g.getTrustedProxies())
}

// checkRememberCookie checks and validates remember cookie.
// Returns the authenticated user if the cookie is valid, nil otherwise.
func (g *SessionGuard) checkRememberCookie(r *http.Request) auth.Authenticatable {
	cookie, err := r.Cookie("remember_" + g.config.Name)
	if err != nil {
		return nil
	}

	// Decrypt cookie value
	if g.encryptor == nil {
		return nil
	}
	decrypted, err := g.encryptor.Decrypt(cookie.Value)
	if err != nil {
		return nil
	}

	// Parse remember token format: userID|token
	parts := strings.SplitN(decrypted, "|", 2)
	if len(parts) != 2 {
		return nil
	}

	userID := parts[0]
	token := parts[1]

	// Look up user by ID
	user, err := g.provider.FindByID(userID)
	if err != nil || user == nil {
		return nil
	}

	// Verify remember token with constant-time comparison.
	// We hash the incoming token with SHA-256 and compare against the stored
	// hash. Legacy rows that still hold a raw token continue to work because
	// we fall through to a direct compare.
	storedToken := user.GetRememberToken()
	if storedToken == "" {
		return nil
	}
	candidateHash := hashRememberToken(token)
	if subtle.ConstantTimeCompare([]byte(storedToken), []byte(candidateHash)) == 1 {
		return user
	}
	if subtle.ConstantTimeCompare([]byte(storedToken), []byte(token)) == 1 {
		return user
	}
	return nil
}

// rememberCookieLifetime returns the cookie TTL for remember-me:
// min(session lifetime, remember-me default). Returns an error when the
// session lifetime is zero so callers refuse to create the cookie.
func (g *SessionGuard) rememberCookieLifetime() (time.Duration, error) {
	const defaultRememberDuration = 30 * 24 * time.Hour
	if g.config.Lifetime <= 0 {
		return 0, errors.New("velocity/auth: session lifetime must be positive to enable remember-me")
	}
	sessionLifetime := time.Duration(g.config.Lifetime) * time.Minute
	if sessionLifetime < defaultRememberDuration {
		return sessionLifetime, nil
	}
	return defaultRememberDuration, nil
}

// setRememberCookie sets remember me cookie.
// The raw token is encrypted into the cookie; only its SHA-256 hash is
// persisted on the user record. Cookie TTL is min(session lifetime,
// 30 days). Refuses to issue a cookie when the session lifetime is zero.
func (g *SessionGuard) setRememberCookie(w http.ResponseWriter, user auth.Authenticatable) error {
	ttl, err := g.rememberCookieLifetime()
	if err != nil {
		return err
	}

	// Generate remember token.
	token, err := generateRememberToken()
	if err != nil {
		return err
	}

	// Store only the hash of the token on the user record.
	hashed := hashRememberToken(token)
	user.SetRememberToken(hashed)
	if err := g.provider.UpdateRememberToken(user, hashed); err != nil {
		return err
	}

	// Create cookie value: userID|token (raw token; cookie is encrypted).
	userID, ok := user.GetAuthIdentifier().(string)
	if !ok {
		return fmt.Errorf("velocity/auth: expected string user identifier, got %T", user.GetAuthIdentifier())
	}
	value := userID + "|" + token

	// Encrypt value
	if g.encryptor == nil {
		return errors.New("velocity/auth: encryptor not configured, cannot set remember cookie")
	}
	encrypted, err := g.encryptor.Encrypt(value)
	if err != nil {
		return err
	}

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "remember_" + g.config.Name,
		Value:    encrypted,
		Path:     g.config.Path,
		Domain:   g.config.Domain,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   g.config.Secure,
		SameSite: g.config.SameSite,
		Expires:  time.Now().Add(ttl),
	})

	return nil
}

// clearRememberCookie clears remember me cookie
func (g *SessionGuard) clearRememberCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "remember_" + g.config.Name,
		Value:    "",
		Path:     g.config.Path,
		Domain:   g.config.Domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   g.config.Secure,
		SameSite: g.config.SameSite,
	})
}

// generateRememberToken generates a random remember token.
// Returns an error rather than panicking when the entropy source fails.
func generateRememberToken() (string, error) {
	token := make([]byte, 32)
	if _, err := io.ReadFull(rememberRandReader, token); err != nil {
		return "", fmt.Errorf("velocity/auth: failed to generate remember token: %w", err)
	}
	return base64.URLEncoding.EncodeToString(token), nil
}

// hashRememberToken returns the hex-encoded SHA-256 digest of the raw
// remember-me token. Only the hash is stored server-side; the raw token
// lives in the user's cookie. This limits the blast radius if the users
// table leaks.
func hashRememberToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
