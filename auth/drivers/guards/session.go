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
	"sync/atomic"
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
//
// All fields are protected by mu. Handlers that fan out goroutines sharing
// the parent request context (e.g. async.All over a batch of authorisation
// checks) hit these accessors concurrently, and without mu the writes from
// getSession / consultServerStore / anchorRecalledUser race the reads from
// sibling goroutines. `go test -race` catches it deterministically.
type sessionHolder struct {
	mu        sync.RWMutex
	session   auth.Session
	storeOnce bool
	storeRec  *auth.StoredSession
	storeErr  error
	// respWriter is the response writer for the in-flight request,
	// installed by SessionMiddleware. The remember-cookie revival path
	// (anchorRecalledUser → rotateRememberToken) needs it to deliver the
	// replacement cookie when rotating the remember token, because the
	// Guard read methods (User, Check) only receive the *http.Request.
	// Nil when the guard is driven outside the middleware.
	respWriter http.ResponseWriter
}

// getSession returns the cached session under a read lock.
func (h *sessionHolder) getSession() auth.Session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.session
}

// setSession installs s as the cached session under a write lock.
func (h *sessionHolder) setSession(s auth.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.session = s
}

// getStoreCache returns (cached, ok, rec, err) under a read lock so the
// fast path in consultServerStore observes a coherent snapshot.
func (h *sessionHolder) getStoreCache() (bool, *auth.StoredSession, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.storeOnce, h.storeRec, h.storeErr
}

// setStoreCache records the server-store lookup outcome under a write lock.
// rec and err are stored together; both may be nil (rec=nil + err=nil is the
// unset state, but consultServerStore always sets storeOnce=true before
// reaching here so callers do not observe the unset combination).
func (h *sessionHolder) setStoreCache(rec *auth.StoredSession, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.storeOnce = true
	h.storeRec = rec
	h.storeErr = err
}

// getResponseWriter returns the response writer installed by
// SessionMiddleware, or nil when the request is being driven outside it.
func (h *sessionHolder) getResponseWriter() http.ResponseWriter {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.respWriter
}

// setResponseWriter records the in-flight request's response writer so
// guard read paths can emit cookies (remember-token rotation).
func (h *sessionHolder) setResponseWriter(w http.ResponseWriter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.respWriter = w
}

// resetStoreCache drops the server-store lookup cache so consultServerStore
// is forced to re-query. Used after session-id rotations (Login,
// anchorRecalledUser) where any cached "no record" entry was keyed on the
// pre-rotation id.
func (h *sessionHolder) resetStoreCache() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.storeOnce = false
	h.storeRec = nil
	h.storeErr = nil
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
	return holder.getSession()
}

// SessionFromRequest returns the session attached to r via
// WithSessionContext + SessionMiddleware (eager-bootstrap or
// handler-resolved), or nil when no session has been bound yet.
//
// Exported so cross-package wiring (specifically the framework's
// default csrf.Config.SessionIDResolver) can read the freshly minted
// id of an anonymous visitor's just-bootstrapped session BEFORE the
// session cookie is written on the response. Without this hook, the
// CSRF middleware's safe-method bootstrap reads only the inbound
// cookie, sees nothing on the very first anonymous request, and
// never mints a token for the new id.
func SessionFromRequest(r *http.Request) auth.Session {
	return sessionFromHolder(r)
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

// providerHolder boxes an auth.UserProvider so atomic.Pointer can hold the
// two-word interface as a single addressable value (H-10 fix). Without the
// box, swaps would race on the interface itab + data pair.
type providerHolder struct{ p auth.UserProvider }

// throttlerHolder boxes a contract.LoginThrottler for the same reason.
type throttlerHolder struct{ t contract.LoginThrottler }

// SessionGuard implements session-based authentication
type SessionGuard struct {
	// provider and throttler are held via atomic.Pointer so concurrent
	// SetProvider / SetLoginThrottler calls cannot tear a reader's
	// two-word interface fetch in Attempt / Login (H-10 fix). The
	// pointers are NEVER nil after construction; helpers always wrap
	// before storing.
	provider  atomic.Pointer[providerHolder]
	throttler atomic.Pointer[throttlerHolder]

	store          auth.SessionStore
	config         auth.SessionConfig
	hasher         auth.Hasher
	encryptor      crypto.Encryptor
	mu             sync.RWMutex
	serverStore    auth.ServerSessionStore
	logger         auth.Logger
	trustedProxies []*net.IPNet
	// attemptFloor is the wall-clock floor for Attempt; zero falls back
	// to auth.DefaultAttemptFloor. Set via SetAttemptFloor or seeded
	// from auth.Config.AttemptFloor at boot.
	attemptFloor time.Duration
	// csrfRotator keeps the per-session CSRF token aligned with the
	// session lifecycle (H-02): Login rotates across Session.Regenerate,
	// Logout revokes before Session.Invalidate, and the remember-cookie
	// revival path rotates inside anchorRecalledUser. Nil disables
	// rotation (tests, JWT-only configs).
	csrfRotator contract.CSRFTokenRotator

	// eventDispatcher is the framework event dispatcher installed by
	// auth.Manager.SetEventDispatcher. Used to emit
	// auth.PasswordNeedsRehashEvent after a successful Attempt against
	// a stored hash that no longer matches the configured Hasher
	// parameters (M-08). Nil disables event emission.
	eventDispatcher func(ctx context.Context, event any) error
}

// loadProvider returns the active auth.UserProvider via atomic load.
func (g *SessionGuard) loadProvider() auth.UserProvider {
	h := g.provider.Load()
	if h == nil {
		return nil
	}
	return h.p
}

// loadThrottler returns the active contract.LoginThrottler via atomic load.
// Falls back to NoopLoginThrottler when no throttler has been installed so
// callers never need a nil check.
func (g *SessionGuard) loadThrottler() contract.LoginThrottler {
	h := g.throttler.Load()
	if h == nil || h.t == nil {
		return auth.NoopLoginThrottler{}
	}
	return h.t
}

// SetAttemptFloor configures the wall-clock floor that Attempt blocks for,
// regardless of whether the credential check resolved fast (missing user)
// or slow (bcrypt verify). Pass 0 to revert to auth.DefaultAttemptFloor.
// Negative values disable the floor (test-only).
//
// See auth.Config.AttemptFloor for the threat model.
func (g *SessionGuard) SetAttemptFloor(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.attemptFloor = d
}

// SetHasher installs the password hasher used both for ValidateCredentials
// (via the configured UserProvider, indirectly) and for the dummy-hash
// timing defense on the missing-user branch of Attempt. Passing nil
// leaves the previously installed hasher in place.
//
// factories.go propagates the operator-configured BcryptCost via this
// setter so the dummy hash on the missing-user path runs at the same
// cost as the real verify; without this, a configured cost of 14 would
// have the dummy at cost 10 (5x faster) and the timing channel from
// H-09 would reopen.
func (g *SessionGuard) SetHasher(h auth.Hasher) {
	if h == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hasher = h
}

// effectiveHasher returns the configured hasher under a read lock so a
// concurrent SetHasher swap is observed atomically. Used by Attempt's
// dummy-hash sizing path.
func (g *SessionGuard) effectiveHasher() auth.Hasher {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.hasher
}

// effectiveAttemptFloor returns the configured floor, falling back to the
// package-level default when unset.
func (g *SessionGuard) effectiveAttemptFloor() time.Duration {
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

	g := &SessionGuard{
		store:     store,
		config:    config,
		hasher:    auth.NewBcryptHasher(10),
		encryptor: enc,
	}
	g.provider.Store(&providerHolder{p: provider})
	g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
	return g, nil
}

// SetLoginThrottler installs a rate-limiter for Attempt() calls. Passing nil
// reverts to the no-op throttler.
//
// Stored via atomic.Pointer so concurrent Attempt() readers cannot tear the
// two-word interface fetch on the throttler field (H-10 fix).
func (g *SessionGuard) SetLoginThrottler(t contract.LoginThrottler) {
	if t == nil {
		g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
		return
	}
	g.throttler.Store(&throttlerHolder{t: t})
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

// SetCSRFTokenRotator wires (or removes when nil) the CSRF token
// rotator. When set, Login rotates the CSRF token alongside the session
// regenerate, Logout revokes the token before invalidating the session,
// and the remember-cookie revival path inside anchorRecalledUser rotates
// the token across the recall regenerate. Without this hook, tokens
// minted under a pre-login session id would persist as orphans in the
// CSRF store after Session.Regenerate, and tokens for the now-destroyed
// session would survive Logout for the full token-store TTL (24h default).
//
// Manager.SetCSRFTokenRotator propagates to every registered guard via
// the auth.CSRFTokenRotatorReceiver interface; consumers normally do not
// need to call this directly.
func (g *SessionGuard) SetCSRFTokenRotator(rotator contract.CSRFTokenRotator) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.csrfRotator = rotator
}

// SetEventDispatcher installs the framework event dispatcher used to
// emit auth.PasswordNeedsRehashEvent after a successful login against a
// stored hash that no longer matches the configured Hasher parameters
// (e.g. operator bumped BcryptCost from 10 to 14). Pass nil to disable
// emission; the guard otherwise becomes silent on the rehash signal.
// Safe for concurrent use.
//
// Manager.SetEventDispatcher propagates to every registered guard via
// the auth.EventDispatcherReceiver interface; consumers normally do not
// need to call this directly.
func (g *SessionGuard) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.eventDispatcher = fn
}

// getEventDispatcher returns the installed dispatcher under a read lock
// so concurrent Attempt() readers observe a consistent value across a
// SetEventDispatcher swap.
func (g *SessionGuard) getEventDispatcher() func(ctx context.Context, event any) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.eventDispatcher
}

// getCSRFTokenRotator returns the installed rotator under a read lock so
// concurrent Login / Logout / recall paths see a consistent snapshot.
// Returns nil when none has been configured (rotation becomes a no-op).
func (g *SessionGuard) getCSRFTokenRotator() contract.CSRFTokenRotator {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.csrfRotator
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
		// Remember-cookie fallback: treat as a full re-authentication
		// (H-08 fix). Rotate the session id, anchor user_id, and
		// consult the server store on the rotated id when one is
		// installed. Without this, an attacker holding a valid
		// remember cookie could authenticate one request even after
		// administrative revocation cleared the server-side record.
		user := g.checkRememberCookie(r)
		if user == nil {
			return false, nil
		}
		if !g.anchorRecalledUser(r, session, user) {
			return false, nil
		}
		return true, nil
	}

	user, err := g.loadProvider().FindByID(userID)
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
//
// Remember-cookie revival (H-08 fix): when the session does not yet carry
// a user_id but the remember cookie is valid, the request is treated as a
// full re-authentication: the session ID is rotated (defeats fixation),
// user_id is anchored on the new session, and the server-side session
// store (when configured) is consulted on the rotated ID. If the store is
// configured and the write/lookup fails, User returns nil.
func (g *SessionGuard) User(r *http.Request) auth.Authenticatable {
	session := g.getSession(r)
	if session == nil {
		return nil
	}

	userID := session.Get("user_id")
	if userID == nil {
		// Try remember cookie
		user := g.checkRememberCookie(r)
		if user == nil {
			return nil
		}
		// Anchor the recovered user as a fresh authenticated session.
		// The cookie itself is flushed by the save-at-end session
		// middleware (H-05); this path mutates the in-memory session
		// AND, when a server store is configured, writes a record
		// keyed on the rotated id.
		if !g.anchorRecalledUser(r, session, user) {
			return nil
		}
		return user
	}

	user, err := g.loadProvider().FindByID(userID)
	if err != nil {
		return nil
	}

	if err := g.consultServerStore(r, session); err != nil {
		return nil
	}
	return user
}

// anchorRecalledUser performs the in-memory equivalent of a fresh Login
// for a user recovered via the remember-cookie fallback (H-08 fix).
// Rotates the session id (defeats fixation against attacker-planted
// cookies), writes user_id into the now-fresh bag, and, when a server-
// side store is configured, records the new id there and re-consults.
//
// Returns true when the request may proceed authenticated. Returns false
// (forcing User to return nil) when:
//   - session ID regeneration failed, OR
//   - server-side store write failed AND a store is configured, OR
//   - the remember-token rotation could not complete (V2-08; see
//     rotateRememberToken). Recall success is conditional on the
//     presented credential being burned and a replacement delivered.
func (g *SessionGuard) anchorRecalledUser(r *http.Request, session auth.Session, user auth.Authenticatable) bool {
	// Capture the pre-rotation id so the CSRF rotator (when wired) can
	// drop any token bound to the planted id. Required to keep the
	// session-fixation defense complete: H-02 says the CSRF token MUST
	// follow Session.Regenerate, and this is the revival entry point
	// reached from both User() and CheckWithError() (G2's H-08).
	oldSessionID := session.ID()

	// Rotate the session id BEFORE writing user_id so an attacker who
	// planted the prior id can no longer inherit authenticated state.
	if err := session.Regenerate(); err != nil {
		g.logWarn("velocity/auth: remember-cookie revival: session regenerate failed", "error", err)
		return false
	}

	// Rotate the CSRF token alongside the session id (H-02). Without
	// this, a token an attacker minted under the pre-revival id remains
	// a valid orphan in the CSRF store for the token-store TTL (24h
	// default), and the post-revival session has no token bound to its
	// new id. A rotate failure fails the revival closed: continuing
	// with a stale CSRF store would leave the now-authenticated session
	// with no valid token and the orphan still reachable.
	if rotator := g.getCSRFTokenRotator(); rotator != nil {
		if err := rotator.RotateToken(oldSessionID, session.ID()); err != nil {
			g.logWarn("velocity/auth: remember-cookie revival: csrf token rotate failed", "old_id", oldSessionID, "new_id", session.ID(), "error", err)
			return false
		}
	}

	session.Put("user_id", user.GetAuthIdentifier())

	// Write the new session to the server-side store on revival so
	// administrative revocation surfaces actually have a record to
	// delete, and so consultServerStore below has something to find.
	// recordServerSession is a no-op when no store has been installed.
	g.recordServerSession(r, session, user)

	// Reset the per-request store cache; the holder may have cached
	// "no record" against the pre-rotation id earlier in the request.
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
		holder.resetStoreCache()
	}

	// If a store is wired, fail-closed when the just-written record is
	// not retrievable. Without this check the H-08 attack model holds:
	// a remember-cookie can authenticate one request even when the
	// store is unhealthy.
	if g.getServerStore() != nil {
		if err := g.consultServerStore(r, session); err != nil {
			g.logWarn("velocity/auth: remember-cookie revival: store consult failed", "error", err)
			return false
		}
	}

	// Rotate the remember token now that the revival is fully anchored
	// (V2-08). The presented token authenticated this request; minting a
	// replacement here makes the remember credential single-use, so a
	// stolen cookie cannot replay silently for its full 30-day lifetime.
	// Rotation is part of the recall contract: when the replacement cannot
	// be minted, persisted, or delivered (no response writer, provider
	// failure, or a concurrent rotation already burned the presented
	// token), the recall fails closed. user_id is removed again so the
	// save-at-end middleware does not persist an authenticated session
	// that would bypass rotation on the next request.
	if err := g.rotateRememberToken(r, user); err != nil {
		g.logWarn("velocity/auth: remember-cookie revival: remember-token rotation failed; rejecting recall", "error", err)
		session.Remove("user_id")
		return false
	}

	return true
}

// errRememberTokenStale reports a lost rotate-on-use race: the presented
// remember token validated, but its stored hash was replaced by a
// concurrent rotation before this request's compare-and-swap landed.
var errRememberTokenStale = errors.New("velocity/auth: remember token rotated concurrently; presented credential is stale")

// rotateRememberToken implements rotate-on-use for the remember-me
// credential (V2-08). Each successful remember-cookie recall reissues the
// credential through issueRememberCookie, the same mint-encrypt-persist-set
// path used at login: a fresh random token is generated, its SHA-256 hash
// replaces the old one on the user record, and the new cookie is written
// to the response. The presented (old) token dies with the overwritten
// hash.
//
// Rotation is mandatory for a recall to succeed. A non-nil error means
// the replacement credential was not fully issued and the caller
// (anchorRecalledUser) must reject the recall:
//
//   - no response writer is available (bare guard reads outside
//     SessionMiddleware have nowhere to deliver the replacement cookie),
//   - minting or encrypting the replacement failed,
//   - persisting the new hash failed,
//   - the provider does not implement auth.RememberTokenCompareAndSwapper, or
//   - the stored hash no longer matches the presented token.
//
// The compare-and-swap is what closes the parallel-recall race: two
// requests presenting the same old cookie both validate before either
// write, but only one swap can land; the loser fails here instead of
// minting a second valid credential via last-writer-wins. An unconditional
// UpdateRememberTokenCtx cannot give that guarantee, so a provider without
// the capability fails the recall closed rather than silently downgrading
// to last-writer-wins; the unconditional update remains in use only on the
// login path, where no previously issued token is being consumed.
//
// Rotation is strict; there is no grace window for the previous token.
// The storage shape (a single remember_token hash on the user record)
// offers no durable slot for a previous-token grace entry, and guard-local
// memory would not survive multi-host deployments, so we fail secure: at
// worst the user signs in again.
func (g *SessionGuard) rotateRememberToken(r *http.Request, user auth.Authenticatable) error {
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		return errors.New("velocity/auth: no session holder on request; cannot deliver rotated remember cookie")
	}
	w := holder.getResponseWriter()
	if w == nil {
		return errors.New("velocity/auth: no response writer on request; cannot deliver rotated remember cookie")
	}

	// The stored hash the presented token matched in checkRememberCookie;
	// the compare-and-swap below anchors on it.
	oldToken := user.GetRememberToken()

	return g.issueRememberCookie(w, user, func(hashed string) error {
		cas, ok := g.loadProvider().(auth.RememberTokenCompareAndSwapper)
		if !ok {
			return errors.New("velocity/auth: user provider does not implement RememberTokenCompareAndSwapper; cannot rotate remember token atomically")
		}
		swapped, err := cas.CompareAndSwapRememberToken(r.Context(), user, oldToken, hashed)
		if err != nil {
			return err
		}
		if !swapped {
			return errRememberTokenStale
		}
		return nil
	})
}

// ID returns the authenticated user ID. It enforces the same server-side
// revocation, user-existence, and remember-cookie revival checks as User and
// CheckWithError, so a revoked session or deleted user is not trusted for
// authorization.
func (g *SessionGuard) ID(r *http.Request) interface{} {
	user := g.User(r)
	if user == nil {
		return nil
	}
	return user.GetAuthIdentifier()
}

// Login logs in a user
func (g *SessionGuard) Login(w http.ResponseWriter, r *http.Request, user auth.Authenticatable, remember ...bool) error {
	// Guard the nil user before any session work. user is deref'd below
	// (session.Put("user_id", user.GetAuthIdentifier())), so a nil here would
	// panic. UserProvider.FindByID is contractually allowed to return
	// (nil, nil) for a not-found id, so LoginByID and any external caller can
	// reach this with a nil user. Return a normal error instead of panicking
	// on a runtime condition.
	if user == nil {
		return auth.ErrUserNotFound
	}

	session := g.getSession(r)
	if session == nil {
		var err error
		session, err = g.store.Create("")
		if err != nil {
			return err
		}
		// Cache in request context if available
		if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok {
			holder.setSession(session)
		}
	}

	// Capture the pre-regenerate session ID so the CSRF rotator can
	// drop any token bound to it. Required for the session-fixation
	// defense: a token an attacker minted under a planted session id
	// must not outlive the regenerate boundary.
	oldSessionID := session.ID()

	// Regenerate session ID for security. A failure here must abort the
	// login: proceeding with the old session ID opens a session-fixation
	// window (an attacker who planted the cookie keeps access).
	if err := session.Regenerate(); err != nil {
		return fmt.Errorf("velocity/auth: login aborted: session regenerate failed: %w", err)
	}

	// Rotate the CSRF token alongside the session ID (H-02). The CSRF
	// token store is keyed by session id; without this hook, a token
	// bound to the pre-regenerate id would remain a valid orphan in the
	// store until the (default 24h) TTL expired, and the post-login
	// session would have no token until something explicitly minted one.
	// A rotation failure aborts the login: continuing with a stale
	// token store would leave the post-login session without a valid
	// CSRF token and the orphan still reachable.
	//
	// After rotation, write the XSRF-TOKEN cookie so the SPA's first
	// POST after login has a token to echo (M-04). Without this hook
	// the per-session token lives in the server store but the SPA has
	// no way to read it; the very next state-changing request 419's.
	if rotator := g.getCSRFTokenRotator(); rotator != nil {
		if err := rotator.RotateToken(oldSessionID, session.ID()); err != nil {
			return fmt.Errorf("velocity/auth: login aborted: csrf token rotate failed: %w", err)
		}
		rotator.WriteXSRFCookie(w, session.ID())
	}

	// Store user ID in session
	session.Put("user_id", user.GetAuthIdentifier())

	// Save the session BEFORE handling remember-me. The session id has
	// already been regenerated and the CSRF token rotated (with the new
	// XSRF-TOKEN cookie written) above. If a remember-cookie write failed
	// before Save, the client would be left holding an XSRF-TOKEN cookie
	// bound to a session id that was never persisted and a CSRF store
	// rotated off the live session, so the very next request would 419.
	// Persisting first keeps login atomic with respect to the rotation.
	if err := session.Save(w); err != nil {
		return err
	}

	// Handle remember me as best-effort. A failure here (e.g. the users
	// table lacks a remember_token column, the provider cannot persist,
	// or the identifier cannot be encoded) must NOT fail an otherwise-
	// successful login, and must not undo the already-committed session
	// and CSRF rotation. Log and continue: the user is authenticated for
	// this session, just not recalled across a new one.
	if len(remember) > 0 && remember[0] {
		if err := g.setRememberCookie(w, user); err != nil {
			g.logWarn("velocity/auth: remember-me cookie not set; login still succeeded", "error", err)
		}
	}

	g.recordServerSession(r, session, user)
	return nil
}

// LoginByID logs in a user by ID
func (g *SessionGuard) LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
	user, err := g.loadProvider().FindByID(id)
	if err != nil {
		return err
	}
	// FindByID may return (nil, nil) for an unknown id. Surface that as an
	// error here so we never pass a nil user into Login (which would panic
	// on the user_id deref).
	if user == nil {
		return auth.ErrUserNotFound
	}

	return g.Login(w, r, user, remember...)
}

// Attempt attempts to log in with credentials. The configured LoginThrottler
// is consulted before the credential check; failed attempts call
// RecordFailure and successes call RecordSuccess.
//
// The entire credential-check phase runs inside auth.Timebox so the
// missing-user fast path and the wrong-password slow path both pad to the
// same wall-clock duration (H-09 fix). When the user does not exist the
// guard still runs the configured hasher against a dummy bcrypt hash so
// the CPU cost also matches; without this an attacker can probe valid
// emails by measuring response time even with a constant-time floor.
func (g *SessionGuard) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	throttler := g.loadThrottler()
	// One key per throttle dimension (pair / identifier / IP, see
	// auth.ThrottleKeys). The attempt is rejected when ANY dimension is
	// over its cap; the error is the same regardless of which dimension
	// tripped so a caller cannot tell a per-IP lockout from a
	// per-account one.
	keys := auth.ThrottleKeys(r, credentials, g.getTrustedProxies())
	// Consult every dimension even after one denies, so the denial path
	// does the same number of Allow lookups regardless of which dimension
	// tripped (no per-dimension oracle).
	denied := false
	for _, key := range keys {
		if !throttler.Allow(r, key) {
			denied = true
		}
	}
	if denied {
		return false, auth.ErrLoginThrottled
	}
	recordFailure := func() {
		for _, key := range keys {
			throttler.RecordFailure(r, key)
		}
	}

	// Snapshot the provider once so the timebox closure and the
	// post-timebox branches see a consistent reference even if a
	// concurrent SetProvider call swaps the pointer mid-call.
	provider := g.loadProvider()

	var (
		user            auth.Authenticatable
		credentialsOK   bool
		invalidCredErr  error
		findErr         error
		password        string
		passwordTypedOK bool
	)

	// Snapshot the hasher once so the closure sees a consistent value
	// across a concurrent SetHasher call. Size the dummy hash to the
	// configured bcrypt cost so the missing-user CPU profile matches
	// the wrong-password CPU profile: a cost-10 dummy against a
	// cost-14 real verify would leak ~400ms of timing difference even
	// with the AttemptFloor in place (F2 fix).
	hasher := g.effectiveHasher()
	dummyHash := dummyHashForHasher(hasher)

	auth.Timebox(g.effectiveAttemptFloor(), func() {
		user, findErr = provider.FindByCredentials(credentials)
		password, passwordTypedOK = credentials["password"].(string)

		if findErr != nil || user == nil {
			// User does not exist: still run the hasher against
			// a dummy hash so the CPU profile matches the
			// wrong-password branch. The result is discarded.
			if passwordTypedOK {
				_ = hasher.Verify(password, string(dummyHash))
			} else {
				_ = hasher.Verify("", string(dummyHash))
			}
			return
		}
		if !passwordTypedOK {
			// Credential dict lacked a "password" string. Treat
			// as invalid; still run the dummy hash so timing
			// stays uniform across the branch.
			_ = hasher.Verify("", string(dummyHash))
			invalidCredErr = auth.ErrInvalidCredentials
			return
		}
		credentialsOK = provider.ValidateCredentials(user, map[string]interface{}{"password": password})
	})

	if findErr != nil || user == nil {
		recordFailure()
		return false, nil // User not found
	}
	if invalidCredErr != nil {
		recordFailure()
		return false, invalidCredErr
	}
	if !credentialsOK {
		recordFailure()
		return false, nil // Invalid password
	}

	// Login user (post-timebox; the success path's residual delay is
	// the login pipeline itself, which is the same on every successful
	// auth so timing here is not a privacy concern).
	if err := g.Login(w, r, user, remember...); err != nil {
		return false, err
	}

	// Hash-staleness check (M-08): when the stored hash no longer
	// matches the configured Hasher parameters (e.g. operator bumped
	// BcryptCost from 10 to 14), emit a PasswordNeedsRehashEvent so
	// listeners can re-hash on the next login. The event carries the
	// user identifier only; the plaintext stays inside this stack
	// frame and is not surfaced to subscribers.
	g.maybeEmitRehashEvent(r.Context(), hasher, user)

	for _, key := range keys {
		throttler.RecordSuccess(r, key)
	}
	return true, nil
}

// maybeEmitRehashEvent fires auth.PasswordNeedsRehashEvent through the
// installed dispatcher when hasher.NeedsRehash reports the stored hash
// is out of date. No-op when no dispatcher has been wired. A dispatcher
// error is swallowed: a transient subscriber failure must not block the
// already-successful login.
func (g *SessionGuard) maybeEmitRehashEvent(ctx context.Context, hasher auth.Hasher, user auth.Authenticatable) {
	dispatcher := g.getEventDispatcher()
	if dispatcher == nil || hasher == nil || user == nil {
		return
	}
	if !hasher.NeedsRehash(user.GetAuthPassword()) {
		return
	}
	if err := dispatcher(ctx, auth.PasswordNeedsRehashEvent{
		UserID:    user.GetAuthIdentifier(),
		GuardName: "session",
	}); err != nil {
		g.logWarn("velocity/auth: password needs-rehash event dispatch failed", "user_id", user.GetAuthIdentifier(), "error", err)
	}
}

// sessionRevoker is the optional capability the H-04 fix relies on when no
// server-side ServerSessionStore is installed: the cookie store accepts a
// Revoke(id) call so subsequent Get calls for that id return a fresh empty
// session even though the cookie value still decrypts. Implemented by
// *session.CookieStore.
type sessionRevoker interface {
	Revoke(sessionID string)
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

	// Revoke the CSRF token for this session BEFORE Invalidate clears
	// the session bag (H-02). Without this, the token would survive in
	// the CSRF store for the full token-store TTL (24h default) and a
	// captured cookie+token pair would remain valid against the now-
	// logged-out session id. A revoke failure is logged and swallowed:
	// logout must not refuse to clear the cookie because a downstream
	// store is unavailable.
	if rotator := g.getCSRFTokenRotator(); rotator != nil {
		if sessionID != "" {
			if err := rotator.RevokeToken(sessionID); err != nil {
				g.logWarn("velocity/auth: csrf token revoke (logout) failed", "session_id", sessionID, "error", err)
			}
		}
		// Clear the client-side XSRF-TOKEN cookie too. Without this the
		// browser keeps the stale value bound to the just-revoked
		// session; the next POST after logout (typically the follow-up
		// login) echoes it as X-XSRF-TOKEN and the server returns 419
		// because no token bound to the new (anonymous) session id
		// matches. Mirrors Login's WriteXSRFCookie symmetry: each side
		// of the session lifecycle teardown owns the cookie it minted.
		rotator.ClearXSRFCookie(w, r)
	}

	// Cycle the persisted remember-me token (H-06 fix). Laravel's
	// SessionGuard::logout calls cycleRememberToken on every individual
	// logout precisely so a stolen remember cookie is not later
	// replayable against the user's account. Velocity used to clear the
	// remember cookie on the client only (via clearRememberCookie below);
	// the server-side users.remember_token survived intact, so a captured
	// remember-cookie + the post-logout request could re-authenticate.
	//
	// Best-effort: failure to clear the token does not block Logout
	// because a transient DB blip should not strand the user in a
	// half-logged-out state. Failures are logged so operators can
	// reconcile.
	if userID := session.Get("user_id"); userID != nil {
		provider := g.loadProvider()
		if user, err := provider.FindByID(userID); err == nil && user != nil {
			if err := provider.UpdateRememberToken(user, ""); err != nil {
				g.logWarn("velocity/auth: clear remember token (logout) failed", "user_id", userID, "error", err)
			}
		}
	}

	// Clear remember cookie
	g.clearRememberCookie(w)

	// Invalidate session. BaseSession.Invalidate marks the session
	// destroyed, clears bags, zeroes the id, and marks the session
	// modified even when its rand source errors. Capture the error
	// but continue with the rest of the teardown: returning early
	// would skip the delete-cookie write, CookieStore.Revoke for the
	// pre-invalidate session id, and the server-store Delete, leaving
	// the client cookie valid and the server-side record live until
	// natural expiry.
	invalidateErr := session.Invalidate()

	// Save invalidated session (writes the delete-cookie because the
	// session is now marked destroyed). A Save failure also continues
	// to the revocation + server-store delete path; without revoke,
	// the still-decrypting captured cookie would re-authenticate
	// against a live server-side record.
	saveErr := session.Save(w)

	// Revoke in the underlying SessionStore when it supports the
	// revocation capability (CookieStore). The cookie value still
	// decrypts post-logout, so without this call a captured cookie
	// remains valid until its IssuedAt window elapses. Best-effort:
	// the store may not implement the interface (other drivers,
	// future stores), and Revoke has no failure mode.
	if rev, ok := g.store.(sessionRevoker); ok && sessionID != "" {
		rev.Revoke(sessionID)
	}

	if store := g.getServerStore(); store != nil && sessionID != "" {
		if err := store.Delete(r.Context(), sessionID); err != nil {
			g.logWarn("velocity/auth: server session store delete (logout) failed", "session_id", sessionID, "error", err)
		}
	}

	// Surface the earliest hard error: invalidate first (the
	// upstream entropy failure callers most care about), then save.
	// Server-side teardown above is best-effort with its own logging.
	if invalidateErr != nil {
		g.logWarn("velocity/auth: session invalidate (logout) failed; teardown completed best-effort", "session_id", sessionID, "error", invalidateErr)
		return invalidateErr
	}
	if saveErr != nil {
		return saveErr
	}
	return nil
}

// SetProvider sets the user provider. Stored via atomic.Pointer so
// concurrent Attempt() readers cannot tear the two-word interface fetch
// on the provider field (H-10 fix). Passing nil leaves the previously
// installed provider in place; SessionGuard must always have a non-nil
// provider so nil swaps are silently ignored.
func (g *SessionGuard) SetProvider(provider auth.UserProvider) {
	if provider == nil {
		return
	}
	g.provider.Store(&providerHolder{p: provider})
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
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok {
		if cached := holder.getSession(); cached != nil {
			return cached
		}
	}

	// Get from store
	session, err := auth.GetSessionFromRequest(r, g.store, g.config.Name)
	if err != nil {
		return nil
	}

	// Cache in request context if available
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok {
		holder.setSession(session)
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
	if holder != nil {
		if once, _, err := holder.getStoreCache(); once {
			return err
		}
	}

	sessionID := session.ID()
	if sessionID == "" {
		// A session with no id cannot be looked up; treat as revoked
		// (the cookie cannot have come from a successful Login).
		if holder != nil {
			holder.setStoreCache(nil, auth.ErrSessionRevoked)
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
			holder.setStoreCache(nil, resolved)
		}
		return resolved
	}

	if holder != nil {
		holder.setStoreCache(rec, nil)
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
	provider := g.loadProvider()
	user, err := provider.FindByID(userID)
	if err != nil || user == nil {
		return nil
	}
	return provider.UpdateRememberToken(user, "")
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
//
// Validation only: rotate-on-use (V2-08) happens in anchorRecalledUser,
// which calls rotateRememberToken once the revival fully anchors, so a
// recall that fails fixation/store checks does not burn the token.
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
	user, err := g.loadProvider().FindByID(userID)
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

// setRememberCookie sets the remember me cookie at login, persisting the
// new token hash unconditionally through the provider (there is no prior
// credential to guard against; login may always overwrite).
func (g *SessionGuard) setRememberCookie(w http.ResponseWriter, user auth.Authenticatable) error {
	return g.issueRememberCookie(w, user, func(hashed string) error {
		return g.loadProvider().UpdateRememberToken(user, hashed)
	})
}

// issueRememberCookie mints a fresh remember token, encrypts the cookie
// payload, persists the token's SHA-256 hash through persist, and writes
// the cookie. The raw token is encrypted into the cookie; only its hash
// reaches the user record. Cookie TTL is min(session lifetime, 30 days).
// Refuses to issue a cookie when the session lifetime is zero.
//
// Encryption runs BEFORE persist so an encryptor failure cannot strand
// the user: overwriting the stored hash while unable to deliver the
// replacement cookie would silently sign the device out.
func (g *SessionGuard) issueRememberCookie(w http.ResponseWriter, user auth.Authenticatable, persist func(hashed string) error) error {
	ttl, err := g.rememberCookieLifetime()
	if err != nil {
		return err
	}

	// Generate remember token.
	token, err := generateRememberToken()
	if err != nil {
		return err
	}

	// Create cookie value: userID|token (raw token; cookie is encrypted).
	// GetAuthIdentifier returns interface{}: a uint for the default
	// integer primary key (ORMUserProvider.normalizeID) and a string for
	// UUID keys. Encode whatever it is as a string so both round-trip;
	// checkRememberCookie reads it back and hands it to FindByID, which
	// accepts either form. A bare .(string) assertion here silently broke
	// remember-me for every integer-PK app (the default shape).
	userID := fmt.Sprint(user.GetAuthIdentifier())
	value := userID + "|" + token

	// Encrypt value
	if g.encryptor == nil {
		return errors.New("velocity/auth: encryptor not configured, cannot set remember cookie")
	}
	encrypted, err := g.encryptor.Encrypt(value)
	if err != nil {
		return err
	}

	// Store only the hash of the token on the user record. The in-memory
	// user is mutated only after a successful persist so a failed (or
	// lost-race) write leaves the object holding the hash that is still
	// authoritative in the store.
	hashed := hashRememberToken(token)
	if err := persist(hashed); err != nil {
		return err
	}
	user.SetRememberToken(hashed)

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
