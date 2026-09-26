package schemes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/clientip"
	"github.com/velocitykode/velocity/internal/sessionclock"
)

// rememberRandReader is the entropy source for remember-me tokens. Tests may
// swap this out to simulate rand.Read failures.
var rememberRandReader io.Reader = rand.Reader

// sessionCtxKey is an unexported context key type to avoid collisions.
type sessionCtxKey struct{}

// sessionHolder is a mutable container for session data stored in request context.
// It also caches the result of the server-side session store lookup so that
// multiple scheme methods invoked on the same request (Check, then User, then
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
	// Scheme read methods (User, Check) only receive the *http.Request.
	// Nil when the scheme is driven outside the middleware, so a non-nil
	// writer is also the mark that the request runs inside the save seam.
	respWriter http.ResponseWriter
	// saveScope marks a holder whose session is saved when the scope ends:
	// the session middleware's (serveWithSession) and the one a Login or
	// Logout outside it commits itself (seamHolder, sessionContext). A
	// holder WithSessionContext attached on its own caches the session but
	// nothing saves it, so state written into that session would be lost;
	// SessionFromContext answers only inside a save scope.
	saveScope bool
	// afterSave holds cookie writes that must follow the session save:
	// the XSRF-TOKEN and remember cookies of a sign-in (Login or
	// remember-me recall) and the XSRF-TOKEN a safe request bootstraps
	// are bound to the session the save persists, so the seam runs them
	// only once that save succeeded, in the order they were queued, and
	// drops them when it failed. A write carries the undo step of a change
	// made outside the session for it (a recall's remember-token
	// rotation), which the seam runs instead when the save fails.
	afterSave []afterSaveWrite
	// transition numbers the authentication transitions (Login, Logout,
	// remember-me recall) the request has begun; see afterSaveWrite.
	transition uint64

	// lifecycle serializes the request's authentication transitions:
	// remember-me recall (session id regeneration, the CSRF token and
	// remember token rotations, and their rollback), Login and Logout,
	// which hold it exclusively. A reader of the signed-in user holds it
	// shared, so it never sees the provisional identity of a transition in
	// flight, and the seam's commit holds it exclusively, so a session is
	// never saved, nor its queued writes taken, halfway through one.
	// Held across store and user store calls, so it is separate from mu.
	lifecycle sync.RWMutex

	// commitOnce makes the session middleware's commit run once per
	// request, whichever write or return fires it.
	commitOnce sync.Once
}

// afterSaveWrite is one write queued behind the session save.
type afterSaveWrite struct {
	write func(w http.ResponseWriter)
	// undo, when set, reverses a change made outside the session for
	// write; the seam runs it when the save fails.
	undo func()
	// transition is the authentication transition that queued a sign-in's
	// credential write, or 0 for a write bound to no transition (the
	// XSRF-TOKEN a safe request bootstraps). A later transition of the
	// same request supersedes it: a remember-me sign-in followed by a
	// logout, or by another sign-in, must not have its credentials
	// delivered or kept by the save that persists what came after.
	transition uint64
}

// beginTransition starts an authentication transition. The caller holds
// lifecycle exclusively. Credential writes queued by earlier transitions
// are superseded from here on.
func (h *sessionHolder) beginTransition() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.transition++
}

// queueAfterSave appends write, bound to no transition, to the writes the
// seam runs after the session save.
func (h *sessionHolder) queueAfterSave(write func(w http.ResponseWriter)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.afterSave = append(h.afterSave, afterSaveWrite{write: write})
}

// queueCredentialWrite appends write and its undo step (nil for none) as
// one entry, bound to the transition in progress: the caller holds
// lifecycle exclusively inside the transition that begun it.
func (h *sessionHolder) queueCredentialWrite(write func(w http.ResponseWriter), undo func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.afterSave = append(h.afterSave, afterSaveWrite{write: write, undo: undo, transition: h.transition})
}

// takeAfterSave empties the queue, so each entry runs at most once, and
// returns what the seam may still run: the writes and undo steps of
// entries bound to no transition or to the latest one. A superseded
// transition's writes and undo steps are dropped. When the session was
// ended (it is saved destroyed) no write runs, since none may follow a
// session that no longer exists; the undo steps are still returned.
func (h *sessionHolder) takeAfterSave(sessionEnded bool) (writes []func(w http.ResponseWriter), undo []func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.afterSave {
		if e.transition != 0 && e.transition != h.transition {
			continue
		}
		if !sessionEnded {
			writes = append(writes, e.write)
		}
		if e.undo != nil {
			undo = append(undo, e.undo)
		}
	}
	h.afterSave = nil
	return writes, undo
}

// QueueAfterSessionSave queues write to run once the session r is served
// under has been saved, and reports whether it did: false when r runs
// outside the session middleware, where there is no save to follow and
// the caller writes at once. A failed save drops write. The CSRF
// middleware defers its XSRF-TOKEN cookie through it, so the cookie never
// names a token kept in a session that was not saved.
func QueueAfterSessionSave(r *http.Request, write func(w http.ResponseWriter)) bool {
	if r == nil || write == nil {
		return false
	}
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil || holder.getResponseWriter() == nil {
		return false
	}
	holder.queueAfterSave(write)
	return true
}

// seamHolder returns r's session holder when r runs inside
// SessionMiddleware, the one place a session is saved. Otherwise (the
// scheme driven from a plain net/http handler, a script or a test, with
// no middleware around it) it returns a fresh holder for the one scheme
// operation and standalone true: the operation is its own save scope and
// commits through the same seam body when it ends, so its write is
// neither lost nor saved twice.
func seamHolder(r *http.Request) (holder *sessionHolder, standalone bool) {
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if ok && holder != nil && holder.getResponseWriter() != nil {
		return holder, false
	}
	return &sessionHolder{saveScope: true}, true
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

// markSaveScope marks the holder as belonging to a scope that saves its
// session.
func (h *sessionHolder) markSaveScope() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.saveScope = true
}

// inSaveScope reports whether the holder's session is saved when its scope
// ends.
func (h *sessionHolder) inSaveScope() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.saveScope
}

// getResponseWriter returns the response writer installed by
// SessionMiddleware, or nil when the request is being driven outside it.
func (h *sessionHolder) getResponseWriter() http.ResponseWriter {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.respWriter
}

// setResponseWriter records the in-flight request's response writer so
// scheme read paths can emit cookies (remember-token rotation).
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
// SessionScheme.getSession was never called). Test helper / middleware helper
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

// SessionFromContext returns the session the request whose context ctx
// is, or descends from, is served under, when that session is saved at the
// end of the request: the session the session middleware bound (see
// SessionFromRequest), or the session a Login or Logout outside the
// middleware passes with a CSRF token rotation or revocation and then
// commits itself. Nil when there is none, including a session cached by a
// holder WithSessionContext attached on its own, which nothing saves.
//
// The framework's CSRF token store reads it to keep the token in the
// session, so the token is saved with the session and never written into
// one that is not.
func SessionFromContext(ctx context.Context) auth.Session {
	if ctx == nil {
		return nil
	}
	holder, ok := ctx.Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil || !holder.inSaveScope() {
		return nil
	}
	return holder.getSession()
}

// sessionContext returns a context carrying session for the CSRF token
// rotator: r's context when the session middleware's holder already holds
// session, otherwise r's context with a holder of its own for session (a
// Login or Logout outside the session middleware, which commits session
// itself).
func sessionContext(r *http.Request, session auth.Session) context.Context {
	if SessionFromContext(r.Context()) == session {
		return r.Context()
	}
	return context.WithValue(r.Context(), sessionCtxKey{}, &sessionHolder{session: session, saveScope: true})
}

// modifiedSession is the optional capability the save-at-end middleware uses
// to skip writing a Set-Cookie header for sessions that no handler touched.
// *auth.BaseSession (and therefore session.CookieSession via embedding)
// satisfies it; mock sessions in tests can opt in by exposing IsModified().
type modifiedSession interface {
	IsModified() bool
	IsDestroyed() bool
}

// lastSeenDebounce is the minimum interval between activity refreshes for a
// given session: the server record's Touch (LastSeenAt and the slid
// ExpiresAt) and the cookie's re-issue. Reads happen on every
// authenticated request to honor revocation; writes are debounced so a
// chatty client does not generate one extra store write and one cookie
// rewrite per request. 60s keeps the idle window accurate to a minute and
// gives the "active sessions" UI accurate timestamps without amplifying
// write volume.
const lastSeenDebounce = 60 * time.Second

// userStoreHolder boxes an auth.UserStore so atomic.Pointer can hold the
// two-word interface as a single addressable value (H-10 fix). Without the
// box, swaps would race on the interface itab + data pair.
type userStoreHolder struct{ p auth.UserStore }

// throttlerHolder boxes a contract.LoginThrottler for the same reason.
type throttlerHolder struct{ t contract.LoginThrottler }

// SessionScheme implements session-based authentication
type SessionScheme struct {
	// user store and throttler are held via atomic.Pointer so concurrent
	// SetUserStore / SetLoginThrottler calls cannot tear a reader's
	// two-word interface fetch in Attempt / Login (H-10 fix). The
	// pointers are NEVER nil after construction; helpers always wrap
	// before storing.
	userStore atomic.Pointer[userStoreHolder]
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
	// loginAdmitter is the per-process admission slot used for over-cap
	// identifier trials when the throttler lacks contract.LoginAdmitter.
	loginAdmitter auth.LocalLoginAdmitter
	// loginChallenge, when set, lets an over-cap identifier attempt
	// skip the delay and admission slot (guarded by mu).
	loginChallenge auth.LoginChallenge
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

// loadUserStore returns the active auth.UserStore via atomic load.
func (g *SessionScheme) loadUserStore() auth.UserStore {
	h := g.userStore.Load()
	if h == nil {
		return nil
	}
	return h.p
}

// loadThrottler returns the active contract.LoginThrottler via atomic load.
// Falls back to NoopLoginThrottler when no throttler has been installed so
// callers never need a nil check.
func (g *SessionScheme) loadThrottler() contract.LoginThrottler {
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
func (g *SessionScheme) SetAttemptFloor(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.attemptFloor = d
}

// SetHasher installs the password hasher used both for ValidateCredentials
// (via the configured UserStore, indirectly) and for the dummy-hash
// timing defense on the missing-user branch of Attempt. Passing nil
// leaves the previously installed hasher in place.
//
// factories.go propagates the operator-configured BcryptCost via this
// setter so the dummy hash on the missing-user path runs at the same
// cost as the real verify; without this, a configured cost of 14 would
// have the dummy at cost 10 (5x faster) and the timing channel from
// H-09 would reopen.
func (g *SessionScheme) SetHasher(h auth.Hasher) {
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
func (g *SessionScheme) effectiveHasher() auth.Hasher {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.hasher
}

// effectiveAttemptFloor returns the configured floor, falling back to the
// package-level default when unset.
func (g *SessionScheme) effectiveAttemptFloor() time.Duration {
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

// SessionSchemeOption configures a SessionScheme at construction.
type SessionSchemeOption func(*SessionScheme)

// WithSessionStore makes the scheme load and save sessions through store
// instead of the default session.CookieStore. Handlers read and write the
// session the same way whichever store holds it. A nil store keeps the
// default.
//
// When store keeps sessions in server records (session.ServerStore), the
// scheme takes that record store as its server session store, so sign-in
// writes the record the session is saved into and revocation reads it;
// nothing else needs installing. When store also accepts a server session
// store (session.ServerStore does, through auth.ServerSessionStoreReceiver),
// the scheme passes every later SetServerSessionStore call on to it, so the
// session's data and its revocation index stay one record.
func WithSessionStore(store auth.SessionStore) SessionSchemeOption {
	return func(g *SessionScheme) {
		if store == nil {
			return
		}
		g.store = store
		if held, ok := store.(recordHoldingStore); ok {
			g.serverStore = held.ServerSessionStore()
		}
	}
}

// recordHoldingStore is a session store that keeps sessions in server
// records and names the record store (session.ServerStore).
type recordHoldingStore interface {
	ServerSessionStore() auth.ServerSessionStore
}

// NewSessionScheme creates a new session scheme. The encryptor seals the
// cookie store's session cookie and the remember-me cookie; it may be nil
// only with WithSessionStore (and then remember-me is unavailable). Without
// WithSessionStore the scheme keeps sessions in a session.CookieStore.
func NewSessionScheme(userStore auth.UserStore, config auth.SessionConfig, encryptor crypto.Encryptor, opts ...SessionSchemeOption) (*SessionScheme, error) {
	g := &SessionScheme{
		config:    config,
		hasher:    auth.NewBcryptHasher(10),
		encryptor: encryptor,
	}
	for _, opt := range opts {
		opt(g)
	}
	if g.store == nil {
		store, err := session.NewCookieStore(config, encryptor)
		if err != nil {
			return nil, err
		}
		g.store = store
	}
	g.userStore.Store(&userStoreHolder{p: userStore})
	g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
	return g, nil
}

// SessionStore returns the store the scheme loads and saves sessions
// through.
func (g *SessionScheme) SessionStore() auth.SessionStore {
	return g.store
}

// SetLoginThrottler installs a rate-limiter for Attempt() calls. Passing nil
// reverts to the no-op throttler.
//
// Stored via atomic.Pointer so concurrent Attempt() readers cannot tear the
// two-word interface fetch on the throttler field (H-10 fix).
func (g *SessionScheme) SetLoginThrottler(t contract.LoginThrottler) {
	if t == nil {
		g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
		return
	}
	g.throttler.Store(&throttlerHolder{t: t})
}

// SetLoginChallenge installs (or clears when nil) the interactive
// challenge predicate consulted for over-cap identifier attempts. See
// auth.LoginChallenge. Normally propagated by Manager.SetLoginChallenge.
func (g *SessionScheme) SetLoginChallenge(fn auth.LoginChallenge) {
	g.mu.Lock()
	g.loginChallenge = fn
	g.mu.Unlock()
}

func (g *SessionScheme) getLoginChallenge() auth.LoginChallenge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.loginChallenge
}

// SetServerSessionStore installs (or removes when nil) a server-side session
// store. When set, the scheme records sessions on Login, looks them up on
// Check/User to honor administrative revocations, and deletes them on
// Logout. Cookie-only behavior is preserved when the store is nil. When the
// scheme's session store keeps sessions in server records
// (session.ServerStore), it is pointed at the same store, so each session
// has one record.
//
// Manager.SetServerSessionStore propagates to every registered scheme via
// the auth.ServerSessionStoreReceiver interface, so consumers normally do
// not need to call this directly.
func (g *SessionScheme) SetServerSessionStore(store auth.ServerSessionStore) {
	g.mu.Lock()
	g.serverStore = store
	g.mu.Unlock()
	if recv, ok := g.store.(auth.ServerSessionStoreReceiver); ok {
		recv.SetServerSessionStore(store)
	}
}

// SetTrustedProxies installs the parsed proxy-network list used for
// client-IP resolution in the login throttler and the audit-trail IP
// recorded on Login. Pass nil to revert to "no proxies trusted"
// (forwarded headers are ignored, RemoteAddr is used verbatim).
//
// Manager.SetTrustedProxies propagates to every registered scheme via
// the auth.TrustedProxiesReceiver interface, so consumers normally do
// not need to call this directly.
func (g *SessionScheme) SetTrustedProxies(proxies []*net.IPNet) {
	// Deep-clone so caller mutation of any *net.IPNet's IP / Mask
	// (or the slice header) cannot flip the scheme's trust decisions
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
// scheme's state by editing the returned slice or its IPNet elements.
// Returns nil when none has been configured.
func (g *SessionScheme) getTrustedProxies() []*net.IPNet {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return clientip.CloneIPNets(g.trustedProxies)
}

// SetLogger installs a logger used for non-fatal store errors (e.g. Redis
// transient failure on Put). Nil disables logging.
func (g *SessionScheme) SetLogger(l auth.Logger) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.logger = l
}

// getServerStore returns the installed server-side session store, or nil
// when none has been configured.
func (g *SessionScheme) getServerStore() auth.ServerSessionStore {
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
// session would survive Logout in a store that keeps them apart from the
// session. Each call passes a context carrying the session, so the
// framework's store, which keeps the token in the session, changes it
// there and the session save persists it.
//
// Manager.SetCSRFTokenRotator propagates to every registered scheme via
// the auth.CSRFTokenRotatorReceiver interface; consumers normally do not
// need to call this directly.
func (g *SessionScheme) SetCSRFTokenRotator(rotator contract.CSRFTokenRotator) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.csrfRotator = rotator
}

// SetEventDispatcher installs the framework event dispatcher used to
// emit auth.PasswordNeedsRehashEvent after a successful login against a
// stored hash that no longer matches the configured Hasher parameters
// (e.g. operator bumped BcryptCost from 10 to 14). Pass nil to disable
// emission; the scheme otherwise becomes silent on the rehash signal.
// Safe for concurrent use.
//
// Manager.SetEventDispatcher propagates to every registered scheme via
// the auth.EventDispatcherReceiver interface; consumers normally do not
// need to call this directly.
func (g *SessionScheme) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.eventDispatcher = fn
}

// getEventDispatcher returns the installed dispatcher under a read lock
// so concurrent Attempt() readers observe a consistent value across a
// SetEventDispatcher swap.
func (g *SessionScheme) getEventDispatcher() func(ctx context.Context, event any) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.eventDispatcher
}

// getCSRFTokenRotator returns the installed rotator under a read lock so
// concurrent Login / Logout / recall paths see a consistent snapshot.
// Returns nil when none has been configured (rotation becomes a no-op).
func (g *SessionScheme) getCSRFTokenRotator() contract.CSRFTokenRotator {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.csrfRotator
}

// logWarn emits a warn event when a logger is configured. Safe to call
// when no logger has been installed.
func (g *SessionScheme) logWarn(msg string, kvs ...any) {
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
func (g *SessionScheme) Check(r *http.Request) bool {
	ok, _ := g.CheckWithError(r)
	return ok
}

// CheckWithError reports whether the request is authenticated and, when not,
// returns the reason. The returned error is one of:
//
//   - nil: request is unauthenticated for ordinary reasons (no cookie, bad
//     cookie, missing user_id, user no longer exists)
//   - auth.ErrSessionExpired: the signed-in session ended under the
//     lifetime policy: idle for longer than SessionConfig.IdleLifetime or
//     older than SessionConfig.AbsoluteLifetime, on the cookie or on the
//     server record, and no valid remember cookie signed the user back in
//     (a valid one revives the user on a new session instead)
//   - auth.ErrSessionRevoked: cookie is live but the matching server-side
//     session record was deleted (e.g. via Manager.RevokeSession), or, for
//     a session held in the record (session.ServerStore), the record is
//     gone for any reason; a remember cookie never revives it and the
//     remember credential it presents is burned
//   - any other error: server-side store lookup failed; fail-closed
//     (returns false). The underlying error is logged when a logger is
//     configured.
//
// Use this from middleware to deliver a "your session was signed out
// remotely" UX without breaking the Scheme interface.
func (g *SessionScheme) CheckWithError(r *http.Request) (bool, error) {
	// Surface the consultServerStore error to the caller (asymmetric
	// with User, which swallows it to nil).
	_, ok, err := g.resolveAuthenticatedUser(r)
	return ok, err
}

// resolveAuthenticatedUser walks the authentication ladder shared by
// CheckWithError and User:
//
//  1. Session lookup; no session means unauthenticated.
//  2. No user_id in the session:
//     a. A server-held session whose record was deleted is revoked, and
//     revocation is authoritative over remember-me: the request is never
//     revived, and the remember credential it presents is burned
//     (burnPresentedRememberToken), so it cannot sign the device back in
//     once the dead session cookie is gone.
//     b. Otherwise the remember-cookie fallback, treated as a full
//     re-authentication (H-08 fix): rotate the session id, anchor
//     user_id, and consult the server store on the rotated id when one
//     is installed (checkRememberCookie -> anchorRecalledUser).
//     c. Without a valid remember cookie, a session the lifetime policy
//     ended reports auth.ErrSessionExpired.
//  3. user_id present: resolve the user via the user store; a lookup error
//     or vanished user means unauthenticated.
//  4. Consult the server-side store (when installed); a store failure or
//     revoked record fails closed. A revoked record also burns the remember
//     credential the request presents. A record the lifetime policy
//     expired drops the stale user_id and falls back to the remember
//     cookie as in step 2b; without a valid remember cookie the expiry is
//     returned.
//
// A signed-in session its record vouches for resolves under the request
// holder's lifecycle lock held shared, so readers run together but never
// while a transition (a recall, Login, Logout) is in flight: the identity
// a recall writes before its remember-token swap is provisional, and a
// reader waits for the swap to decide it. Every other outcome may change
// the session (recall, burn, expiry fall-through), so it is decided under
// the lock held exclusively, after re-reading the session: goroutines of
// one request that read the user together recall once, and the others see
// the recalled user.
//
// Returns the resolved user, whether the request is authenticated, and
// the reason it is not (nil on the ordinary unauthenticated paths). Error
// policy is owned by the callers: CheckWithError surfaces err while User
// swallows everything to nil.
func (g *SessionScheme) resolveAuthenticatedUser(r *http.Request) (auth.Authenticatable, bool, error) {
	holder, _ := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if user, ok, decided, err := g.resolveVouchedSession(r, holder); decided {
		return user, ok, err
	}

	if holder != nil {
		holder.lifecycle.Lock()
		defer holder.lifecycle.Unlock()
	}
	session := g.getSession(r)
	if session == nil {
		return nil, false, nil
	}
	return g.resolveAuthenticationChange(r, session)
}

// resolveVouchedSession is resolveAuthenticatedUser's shared-lock step: it
// decides a request with no session, a signed-in session whose user is
// gone, and a signed-in session its record vouches for (or whose record
// lookup failed), and reports decided false for everything that may
// change the session.
func (g *SessionScheme) resolveVouchedSession(r *http.Request, holder *sessionHolder) (user auth.Authenticatable, ok, decided bool, err error) {
	if holder != nil {
		holder.lifecycle.RLock()
		defer holder.lifecycle.RUnlock()
	}
	session := g.getSession(r)
	if session == nil {
		return nil, false, true, nil
	}
	userID := session.Get(auth.UserIDSessionKey)
	if userID == nil {
		return nil, false, false, nil
	}
	user, err = g.loadUserStore().FindByIDCtx(r.Context(), userID)
	if err != nil || user == nil {
		return nil, false, true, nil
	}
	err = g.consultServerStore(r, session)
	if err == nil {
		return user, true, true, nil
	}
	if !errors.Is(err, auth.ErrSessionExpired) && !errors.Is(err, auth.ErrSessionRevoked) {
		return nil, false, true, err
	}
	return nil, false, false, nil
}

// resolveAuthenticationChange is resolveAuthenticatedUser's ladder for a
// session that is not a vouched-for signed-in session. The caller holds
// the request holder's lifecycle lock.
func (g *SessionScheme) resolveAuthenticationChange(r *http.Request, session auth.Session) (auth.Authenticatable, bool, error) {
	userID := session.Get(auth.UserIDSessionKey)
	if userID == nil {
		// A server-held session whose record was deleted arrives as an
		// empty replacement session. Its data went with the record, so
		// the revocation is reported here rather than by
		// consultServerStore, and before any recall.
		if rd, ok := session.(interface{ RecordDeleted() bool }); ok && rd.RecordDeleted() {
			g.burnPresentedRememberToken(r)
			return nil, false, auth.ErrSessionRevoked
		}
		// Try remember cookie. On success, anchor the recovered user
		// as a fresh authenticated session: the cookie itself is
		// flushed by the save-at-end session middleware (H-05); this
		// path mutates the in-memory session AND, when a server store
		// is configured, writes a record keyed on the rotated id.
		if user := g.checkRememberCookie(r); user != nil {
			if !g.anchorRecalledUser(r, session, user) {
				return nil, false, nil
			}
			return user, true, nil
		}
		// A signed-in cookie the lifetime policy ended arrives as an
		// empty replacement session; say so, so the caller can tell an
		// expired session from one that was never signed in.
		if ex, ok := session.(interface{ AuthenticationExpired() bool }); ok && ex.AuthenticationExpired() {
			return nil, false, auth.ErrSessionExpired
		}
		return nil, false, nil
	}

	user, err := g.loadUserStore().FindByIDCtx(r.Context(), userID)
	if err != nil || user == nil {
		return nil, false, nil
	}

	if err := g.consultServerStore(r, session); err != nil {
		switch {
		case errors.Is(err, auth.ErrSessionExpired):
			// The lifetime policy ended the session on its server record
			// while the cookie is still live: the identity it carries is
			// stale. A valid remember cookie signs the user back in on a
			// new session, exactly as when the cookie itself expired.
			if recalled := g.checkRememberCookie(r); recalled != nil {
				session.Remove(auth.UserIDSessionKey)
				if g.anchorRecalledUser(r, session, recalled) {
					return recalled, true, nil
				}
			}
		case errors.Is(err, auth.ErrSessionRevoked):
			// Revocation is authoritative: never revive, and burn the
			// remember credential this revoked session presents so it
			// cannot sign the device back in once the session cookie
			// is gone.
			g.burnPresentedRememberToken(r)
		}
		return nil, false, err
	}
	return user, true, nil
}

// burnPresentedRememberToken ends the remember credential a revoked
// request presents: the remember cookie is deleted on the response, and
// when it still validates the stored remember token is cleared too. The
// stored token is a single per-user hash, so a validating cookie is the
// one live remember credential: clearing it signs out exactly the device
// (or copy) that holds it. The clear is a compare-and-swap from the
// matched hash when the user store supports it, so a credential a
// concurrent Login just minted elsewhere survives.
func (g *SessionScheme) burnPresentedRememberToken(r *http.Request) {
	if _, err := r.Cookie("remember_" + g.config.Name); err != nil {
		return
	}
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
		if w := holder.getResponseWriter(); w != nil {
			g.clearRememberCookie(w)
		}
	}
	user := g.checkRememberCookie(r)
	if user == nil {
		return
	}
	matched := user.GetRememberToken()
	userStore := g.loadUserStore()
	var err error
	if cas, ok := userStore.(auth.RememberTokenCompareAndSwapper); ok {
		_, err = cas.CompareAndSwapRememberToken(r.Context(), user, matched, "")
	} else {
		err = userStore.UpdateRememberTokenCtx(r.Context(), user, "")
	}
	if err != nil {
		g.logWarn("velocity/auth: clear remember token (revoked session) failed", "error", err)
	}
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
func (g *SessionScheme) User(r *http.Request) auth.Authenticatable {
	// Swallow the consultServerStore error to nil (asymmetric with
	// CheckWithError, which surfaces it).
	user, ok, _ := g.resolveAuthenticatedUser(r)
	if !ok {
		return nil
	}
	return user
}

// anchorRecalledUser performs the in-memory equivalent of a fresh Login
// for a user recovered via the remember-cookie fallback (H-08 fix). Like
// Login it is a sign-in: the regenerated session and its server record
// start a new absolute lifetime.
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
func (g *SessionScheme) anchorRecalledUser(r *http.Request, session auth.Session, user auth.Authenticatable) bool {
	// Capture the pre-rotation id so the CSRF rotator (when wired) can
	// drop any token bound to the planted id. Required to keep the
	// session-fixation defense complete: H-02 says the CSRF token MUST
	// follow Session.Regenerate, and this is the revival entry point
	// reached from both User() and CheckWithError() (G2's H-08).
	oldSessionID := session.ID()

	// The recall is a transition of its own: credential writes an earlier
	// transition of this request queued are superseded.
	holder, _ := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if holder != nil {
		holder.beginTransition()
	}

	// Rotate the session id BEFORE writing user_id so an attacker who
	// planted the prior id can no longer inherit authenticated state.
	if err := session.Regenerate(); err != nil {
		g.logWarn("velocity/auth: remember-cookie revival: session regenerate failed", "error", err)
		return false
	}

	// Rotate the CSRF token alongside the session id (H-02). Without
	// this, a token an attacker minted under the pre-revival id remains
	// a valid orphan in the CSRF store,
	// and the post-revival session has no token bound to its
	// new id. A rotate failure fails the revival closed: continuing
	// with a stale CSRF store would leave the now-authenticated session
	// with no valid token and the orphan still reachable.
	if rotator := g.getCSRFTokenRotator(); rotator != nil {
		rotateCtx := sessionContext(r, session)
		if err := rotator.RotateToken(rotateCtx, oldSessionID, session.ID()); err != nil {
			g.logWarn("velocity/auth: remember-cookie revival: csrf token rotate failed", "old_id", oldSessionID, "new_id", session.ID(), "error", err)
			return false
		}
		// Write the fresh XSRF-TOKEN cookie alongside the rotation, as
		// the rotator contract requires on the revival path too. The
		// rotation above deleted the token bound to the old session id,
		// so without this write the client keeps echoing a stale cookie
		// and its very next state-changing request 419s (until a later
		// safe-method response happens to re-sync it). Mirrors Login:
		// the write is queued on the seam and runs after the session
		// save, so the cookie is never bound to an id that was not
		// persisted.
		if holder != nil && holder.getResponseWriter() != nil {
			newID := session.ID()
			holder.queueCredentialWrite(func(w http.ResponseWriter) {
				rotator.WriteXSRFCookie(rotateCtx, w, newID)
			}, nil)
		}
	}

	session.Put(auth.UserIDSessionKey, user.GetAuthIdentifier())

	// Write the new session to the server-side store on revival so
	// administrative revocation surfaces actually have a record to
	// delete, and so consultServerStore below has something to find.
	// recordServerSession is a no-op when no store has been installed.
	g.recordServerSession(r, session, user)

	// Reset the per-request store cache; the holder may have cached
	// "no record" against the pre-rotation id earlier in the request.
	if holder != nil {
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
	// be minted, persisted, or delivered (no response writer, user store
	// failure, or a concurrent rotation already burned the presented
	// token), the recall fails closed. user_id is removed again so the
	// save-at-end middleware does not persist an authenticated session
	// that would bypass rotation on the next request.
	if err := g.rotateRememberToken(r, user); err != nil {
		g.logWarn("velocity/auth: remember-cookie revival: remember-token rotation failed; rejecting recall", "error", err)
		session.Remove(auth.UserIDSessionKey)
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
// credential through mintRememberCookie, the same mint-encrypt-persist
// path used at login: a fresh random token is generated and its SHA-256
// hash replaces the old one on the user record. The presented (old) token
// dies with the overwritten hash.
//
// The replacement cookie is bound to the recalled session, so it is
// queued behind the session save like Login's cookies: the seam writes it
// only once the session is saved. When the save fails, the seam rolls the
// stored hash back to the presented token instead (compare-and-swap from
// the replacement), so the visitor's remember cookie keeps working and the
// next request can recall again; the recall is never left with a moved
// hash and no cookie to match it.
//
// Rotation is mandatory for a recall to succeed. A non-nil error means
// the replacement credential was not issued and the caller
// (anchorRecalledUser) must reject the recall:
//
//   - no response writer is available (bare scheme reads outside
//     SessionMiddleware have nowhere to deliver the replacement cookie),
//   - minting or encrypting the replacement failed,
//   - persisting the new hash failed,
//   - the user store does not implement auth.RememberTokenCompareAndSwapper, or
//   - the stored hash no longer matches the presented token.
//
// The compare-and-swap is what closes the parallel-recall race: two
// requests presenting the same old cookie both validate before either
// write, but only one swap can land; the loser fails here instead of
// minting a second valid credential via last-writer-wins. An unconditional
// UpdateRememberTokenCtx cannot give that guarantee, so a user store without
// the capability fails the recall closed rather than silently downgrading
// to last-writer-wins; the unconditional update remains in use only on the
// login path, where no previously issued token is being consumed.
//
// Rotation is strict; there is no grace window for the previous token.
// The storage shape (a single remember_token hash on the user record)
// offers no durable slot for a previous-token grace entry, and scheme-local
// memory would not survive multi-host deployments, so we fail secure: at
// worst the user signs in again.
func (g *SessionScheme) rotateRememberToken(r *http.Request, user auth.Authenticatable) error {
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		return errors.New("velocity/auth: no session holder on request; cannot deliver rotated remember cookie")
	}
	if holder.getResponseWriter() == nil {
		return errors.New("velocity/auth: no response writer on request; cannot deliver rotated remember cookie")
	}
	cas, ok := g.loadUserStore().(auth.RememberTokenCompareAndSwapper)
	if !ok {
		return errors.New("velocity/auth: user store does not implement RememberTokenCompareAndSwapper; cannot rotate remember token atomically")
	}

	// The stored hash the presented token matched in checkRememberCookie;
	// the compare-and-swap below anchors on it.
	oldToken := user.GetRememberToken()

	var newToken string
	cookie, err := g.mintRememberCookie(user, func(hashed string) error {
		swapped, err := cas.CompareAndSwapRememberToken(r.Context(), user, oldToken, hashed)
		if err != nil {
			return err
		}
		if !swapped {
			return errRememberTokenStale
		}
		newToken = hashed
		return nil
	})
	if err != nil {
		return err
	}
	// The cookie and its undo are queued as one entry, so the seam's
	// commit (which waits for this transition anyway) can never take one
	// without the other.
	ctx := context.WithoutCancel(r.Context())
	holder.queueCredentialWrite(func(w http.ResponseWriter) {
		http.SetCookie(w, cookie)
	}, func() {
		swapped, err := cas.CompareAndSwapRememberToken(ctx, user, newToken, oldToken)
		if err != nil || !swapped {
			g.logWarn("velocity/auth: remember-cookie revival: session not saved and the remember token could not be restored; the visitor signs in again", "swapped", swapped, "error", err)
			return
		}
		user.SetRememberToken(oldToken)
	})
	return nil
}

// ID returns the authenticated user ID. It enforces the same server-side
// revocation, user-existence, and remember-cookie revival checks as User and
// CheckWithError, so a revoked session or deleted user is not trusted for
// authorization.
func (g *SessionScheme) ID(r *http.Request) interface{} {
	user := g.User(r)
	if user == nil {
		return nil
	}
	return user.GetAuthIdentifier()
}

// Login logs in a user
func (g *SessionScheme) Login(w http.ResponseWriter, r *http.Request, user auth.Authenticatable, remember ...bool) error {
	// Guard the nil user before any session work. user is deref'd below
	// (session.Put(auth.UserIDSessionKey, ...)), so a nil here would
	// panic. UserStore.FindByID is contractually allowed to return
	// (nil, nil) for a not-found id, so LoginByID and any external caller can
	// reach this with a nil user. Return a normal error instead of panicking
	// on a runtime condition.
	if user == nil {
		return auth.ErrUserNotFound
	}

	// The session middleware saves the session and then writes the
	// cookies bound to it. Outside it, this login is its own save scope
	// and commits the same way before returning.
	holder, standalone := seamHolder(r)
	holder.lifecycle.Lock()
	defer holder.lifecycle.Unlock()
	// A sign-in supersedes the credential writes an earlier transition of
	// this request queued (an earlier sign-in's remember cookie).
	holder.beginTransition()

	session := g.getSession(r)
	if session == nil {
		var err error
		session, err = g.store.Create("")
		if err != nil {
			return err
		}
		// Cache in request context if available
		if cached, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && cached != nil {
			cached.setSession(session)
		}
	}

	// Capture the pre-regenerate session ID so the CSRF rotator can
	// drop any token bound to it. Required for the session-fixation
	// defense: a token an attacker minted under a planted session id
	// must not outlive the regenerate boundary.
	oldSessionID := session.ID()

	// Retire the session this sign-in rotates away from, with everything
	// it carried (a signed-in identity, the CSRF token in its bag): a
	// captured copy of its cookie must not stay usable next to the new
	// session. The server record goes first, and a failure aborts the
	// login before anything changed; the cookie store's revocation follows
	// the regenerate below.
	if err := g.retireServerRecord(r, oldSessionID); err != nil {
		return fmt.Errorf("velocity/auth: login aborted: previous session not retired: %w", err)
	}

	// Regenerate session ID for security. A failure here must abort the
	// login: proceeding with the old session ID opens a session-fixation
	// window (an attacker who planted the cookie keeps access).
	if err := session.Regenerate(); err != nil {
		return fmt.Errorf("velocity/auth: login aborted: session regenerate failed: %w", err)
	}
	if rev, ok := g.store.(sessionRevoker); ok && oldSessionID != "" {
		rev.Revoke(oldSessionID)
	}

	// Rotate the CSRF token alongside the session ID (H-02). Without
	// this hook, a token bound to the pre-regenerate id would remain a
	// valid orphan (the regenerated session keeps its bag, token
	// included), and the post-login
	// session would have no token until something explicitly minted one.
	// A rotation failure aborts the login: continuing with a stale
	// token store would leave the post-login session without a valid
	// CSRF token and the orphan still reachable.
	//
	// After rotation, the XSRF-TOKEN cookie is written so the SPA's
	// first POST after login has a token to echo (M-04). Without it the
	// per-session token lives in the server store but the SPA has no way
	// to read it; the very next state-changing request 419's.
	//
	// The cookie is queued behind the session save: the session
	// middleware saves the regenerated session first and writes the
	// queued cookies only when that save succeeded. A client must never
	// hold an XSRF-TOKEN or remember cookie bound to a session id that
	// was never persisted, or its very next request would 419.
	sessionID := session.ID()
	if rotator := g.getCSRFTokenRotator(); rotator != nil {
		rotateCtx := sessionContext(r, session)
		if err := rotator.RotateToken(rotateCtx, oldSessionID, sessionID); err != nil {
			return fmt.Errorf("velocity/auth: login aborted: csrf token rotate failed: %w", err)
		}
		holder.queueCredentialWrite(func(w http.ResponseWriter) {
			rotator.WriteXSRFCookie(rotateCtx, w, sessionID)
		}, nil)
	}

	// Store user ID in session
	session.Put(auth.UserIDSessionKey, user.GetAuthIdentifier())

	// Handle remember me as best-effort, after the session save. A
	// failure here (e.g. the users table lacks a remember_token column,
	// the user store cannot persist, or the identifier cannot be
	// encoded) must NOT fail an otherwise-successful login, and must not
	// undo the committed session and CSRF rotation. Log and continue:
	// the user is authenticated for this session, just not recalled
	// across a new one.
	if len(remember) > 0 && remember[0] {
		ctx := r.Context()
		holder.queueCredentialWrite(func(w http.ResponseWriter) {
			if err := g.setRememberCookie(ctx, w, user); err != nil {
				g.logWarn("velocity/auth: remember-me cookie not set; login still succeeded", "error", err)
			}
		}, nil)
	}

	// The sign-in record is written before the session is saved, inside
	// the middleware and outside it alike: a session store that keeps the
	// session in that record (session.ServerStore) saves into it and never
	// creates a signed-in record itself. When the save then fails, the
	// record names an id no client received and ends with its TTL.
	g.recordServerSession(r, session, user)

	if standalone {
		holder.setSession(session)
		if err := commitSessionHeld(g, r, w, holder); err != nil {
			return err
		}
	}
	return nil
}

// LoginByID logs in a user by ID
func (g *SessionScheme) LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
	user, err := g.loadUserStore().FindByIDCtx(r.Context(), id)
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
// scheme still runs the configured hasher against a dummy bcrypt hash so
// the CPU cost also matches; without this an attacker can probe valid
// emails by measuring response time even with a constant-time floor.
func (g *SessionScheme) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	// Snapshot throttler, user store, and hasher once so the credential
	// check and the success tail below see consistent references even if
	// a concurrent Set* call swaps one mid-call.
	throttler := g.loadThrottler()
	hasher := g.effectiveHasher()
	user, keys, ok, err := attemptCredentials(r, credentials, g.loadUserStore(), hasher, throttler, g.effectiveAttemptFloor(), g.getTrustedProxies(), &g.loginAdmitter, g.getLoginChallenge())
	if !ok {
		return false, err
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
	maybeEmitRehashEvent(r.Context(), g.getEventDispatcher(), hasher, user, "session", g.logWarn)

	recordAttemptSuccess(r, keys, throttler, &g.loginAdmitter)
	return true, nil
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
func (g *SessionScheme) Logout(w http.ResponseWriter, r *http.Request) error {
	// The session middleware writes the delete cookie for the
	// invalidated session. Outside it, this logout is its own save scope
	// and commits the same way below.
	holder, standalone := seamHolder(r)
	holder.lifecycle.Lock()
	defer holder.lifecycle.Unlock()
	// A logout supersedes the credential writes an earlier transition of
	// this request queued: a remember-me sign-in earlier in the request
	// must not mint its remember credential after the logout cleared it.
	holder.beginTransition()
	session := g.getSession(r)
	if session == nil {
		return nil
	}

	// Capture the session ID before Invalidate so we can also tear down
	// the server-side record. BaseSession.Invalidate currently leaves id
	// intact, but capturing here is robust against future changes.
	sessionID := session.ID()

	// Revoke the CSRF token for this session BEFORE Invalidate clears
	// the session bag (H-02). Without this, a token kept apart from the
	// session would survive in the CSRF store and a
	// captured cookie+token pair would remain valid against the now-
	// logged-out session id. A revoke failure is logged and swallowed:
	// logout must not refuse to clear the cookie because a downstream
	// store is unavailable.
	if rotator := g.getCSRFTokenRotator(); rotator != nil {
		if sessionID != "" {
			if err := rotator.RevokeToken(sessionContext(r, session), sessionID); err != nil {
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

	// Cycle the persisted remember-me token (H-06 fix). Every individual
	// logout cycles the stored token precisely so a stolen remember
	// cookie is not later replayable against the user's account.
	// Velocity used to clear the remember cookie on the client only
	// (via clearRememberCookie below);
	// the server-side users.remember_token survived intact, so a captured
	// remember-cookie + the post-logout request could re-authenticate.
	//
	// Best-effort: failure to clear the token does not block Logout
	// because a transient DB blip should not strand the user in a
	// half-logged-out state. Failures are logged so operators can
	// reconcile.
	if userID := session.Get(auth.UserIDSessionKey); userID != nil {
		userStore := g.loadUserStore()
		user, err := userStore.FindByIDCtx(r.Context(), userID)
		switch {
		case err == nil && user != nil:
			if err := userStore.UpdateRememberTokenCtx(r.Context(), user, ""); err != nil {
				g.logWarn("velocity/auth: clear remember token (logout) failed", "user_id", userID, "error", err)
			}
		case err != nil && !errors.Is(err, auth.ErrUserNotFound):
			g.logWarn("velocity/auth: clear remember token (logout) failed: user lookup failed", "user_id", userID, "error", err)
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

	// The session middleware saves the invalidated session, which
	// writes the delete cookie because the session is now marked
	// destroyed. A standalone logout commits it here; a failure also
	// continues to the revocation + server-store delete path, since
	// without revoke the still-decrypting captured cookie would
	// re-authenticate against a live server-side record.
	var saveErr error
	if standalone {
		holder.setSession(session)
		saveErr = commitSessionHeld(g, r, w, holder)
	}

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
	return saveErr
}

// SetUserStore sets the user store. Stored via atomic.Pointer so
// concurrent Attempt() readers cannot tear the two-word interface fetch
// on the user store field (H-10 fix). Passing nil leaves the previously
// installed user store in place; SessionScheme must always have a non-nil
// user store so nil swaps are silently ignored.
func (g *SessionScheme) SetUserStore(userStore auth.UserStore) {
	if userStore == nil {
		return
	}
	g.userStore.Store(&userStoreHolder{p: userStore})
}

// Session returns the request-scoped Session, loading from the cookie store
// on first call and caching it in the request context for subsequent calls
// when WithSessionContext has been applied to the request.
//
// Implements the auth.SessionAware capability so auth.Manager.Session(r)
// can surface the session bag (including Flash / GetFlash / FlushFlash)
// without consumers reaching into the scheme directly.
func (g *SessionScheme) Session(r *http.Request) auth.Session {
	return g.getSession(r)
}

// ResolveSession returns the session r is served under when the session
// scheme accepts it, for state bound to a session rather than to a signed-in
// user (the framework's CSRF token resolver). The session is:
//
//   - the session of a save scope (the session middleware, or a Login or
//     Logout outside it), which covers an anonymous visitor's first
//     request, before its cookie is written: the scope saves that session,
//     so a freshly created one is the session the response persists; or
//   - otherwise, the session the session store loads from r's cookie. The
//     store answers a missing, undecryptable, revoked or expired cookie
//     with a freshly created session, which is born modified; that answer,
//     and a session that cannot report whether it is fresh, return
//     auth.ErrSessionNotFound. A holder WithSessionContext attached on its
//     own caches the session without saving it, so a session it holds is
//     judged the same way on every call: a fresh (or since modified) one
//     stays refused.
//
// A session that carries a signed-in user must also be accepted by the
// server session store when one is installed, the same check
// authentication makes (consultServerStore): a deleted record returns
// auth.ErrSessionRevoked and an expired one auth.ErrSessionExpired, so a
// session signed out or revoked on another instance is refused here too.
// Remember-me recall is not attempted.
func (g *SessionScheme) ResolveSession(r *http.Request) (auth.Session, error) {
	sess := sessionFromHolder(r)
	if sess == nil || sess.ID() == "" {
		sess = g.getSession(r)
	}
	if sess == nil || sess.ID() == "" {
		return nil, auth.ErrSessionNotFound
	}
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); !ok || holder == nil || !holder.inSaveScope() {
		fresh, ok := sess.(interface{ IsModified() bool })
		if !ok || fresh.IsModified() {
			return nil, auth.ErrSessionNotFound
		}
	}
	if sess.Get(auth.UserIDSessionKey) != nil {
		if err := g.consultServerStore(r, sess); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

// getSession gets or creates session for request
func (g *SessionScheme) getSession(r *http.Request) auth.Session {
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

// consultServerStore enforces server-side session revocation and the
// lifetime policy on the server record. When a store has been installed,
// every authenticated request looks up the session by id: a missing record
// returns ErrSessionRevoked, and a record past its ExpiresAt or its
// absolute cap (CreatedAt plus SessionConfig.AbsoluteLifetime) returns
// ErrSessionExpired. The Get result is cached on the request-scoped
// sessionHolder so multiple scheme methods in the same request only pay one
// round-trip. The record slides (LastSeenAt and ExpiresAt) at most once per
// lastSeenDebounce interval.
//
// Returns nil when no store is configured (cookie-only mode preserved).
func (g *SessionScheme) consultServerStore(r *http.Request, session auth.Session) error {
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
	if err == nil && rec.UserID == "" {
		// A signed-out visitor's record never vouches for a session that
		// carries a user: only a sign-in writes a record with its owner.
		rec, err = nil, auth.ErrSessionNotFound
	}
	if err == nil && g.pastAbsoluteCap(rec) {
		// Records written before the cap existed, or by another writer,
		// may carry an ExpiresAt past the cap: the cap is enforced on
		// CreatedAt directly. The record is reaped best-effort.
		_ = store.Delete(r.Context(), sessionID)
		rec, err = nil, auth.ErrSessionExpired
	}
	if err != nil {
		var resolved error
		if errors.Is(err, auth.ErrSessionExpired) {
			resolved = auth.ErrSessionExpired
		} else if errors.Is(err, auth.ErrSessionNotFound) {
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

	if err := g.maybeRefreshLastSeen(r.Context(), store, rec); err != nil {
		// The record vanished between Get and Touch: a revocation won the
		// race. Deny this request rather than let it ride on the stale read.
		if holder != nil {
			holder.setStoreCache(nil, err)
		}
		return err
	}

	if holder != nil {
		holder.setStoreCache(rec, nil)
	}
	return nil
}

// maybeRefreshLastSeen is the server record's debounced activity refresh:
// it slides the record's LastSeenAt to now and its ExpiresAt to the
// lifetime policy's end (recordExpiry), so an active session's record
// keeps up with its idle window until the absolute cap. The debounce keeps
// the read on every request (mandatory for revocation) without doubling
// the round-trips.
//
// The write goes through ServerSessionStore.Touch, never Put: Put is
// create-or-replace and would recreate a record deleted between the Get
// above and this write. A not-found result from Touch means the session
// was revoked mid-request and is returned as auth.ErrSessionRevoked; an
// expired result is returned as auth.ErrSessionExpired. Either way the
// caller denies the request. Any other store error is logged and
// swallowed: the refresh is best-effort and the Get already proved the
// session live.
func (g *SessionScheme) maybeRefreshLastSeen(ctx context.Context, store auth.ServerSessionStore, rec *auth.StoredSession) error {
	if rec == nil {
		return nil
	}
	now := sessionclock.Now()
	if now.Sub(rec.LastSeenAt) < lastSeenDebounce {
		return nil
	}
	err := store.Touch(ctx, rec.ID, now, g.recordExpiry(rec.CreatedAt, now))
	if err == nil {
		return nil
	}
	if errors.Is(err, auth.ErrSessionExpired) {
		return auth.ErrSessionExpired
	}
	if errors.Is(err, auth.ErrSessionNotFound) {
		return auth.ErrSessionRevoked
	}
	g.logWarn("velocity/auth: server session store touch (lastseen) failed", "session_id", rec.ID, "error", err)
	return nil
}

// recordExpiry is the server record's ExpiresAt for a session created at
// createdAt and last active at lastActive: the lifetime policy's end plus
// its grace (auth.SessionConfig.RecordExpiresAt), or the zero time (no
// expiry) when the policy has neither an idle timeout nor an absolute cap.
func (g *SessionScheme) recordExpiry(createdAt, lastActive time.Time) time.Time {
	return g.config.RecordExpiresAt(createdAt, lastActive)
}

// pastAbsoluteCap reports whether rec is older than the absolute lifetime.
func (g *SessionScheme) pastAbsoluteCap(rec *auth.StoredSession) bool {
	abs := g.config.AbsoluteTimeout()
	return rec != nil && abs > 0 && !rec.CreatedAt.IsZero() && sessionclock.Now().After(rec.CreatedAt.Add(abs))
}

// recordServerSession writes the freshly-issued session to the server-side
// store on Login. Failures are logged and swallowed so a transient store
// outage does not break login (the user is authenticated for this request
// and subsequent reads will fail-closed).
//
// session.Regenerate() inside Login already produced a fresh id, so this
// writes a brand-new record; Login removed the previous id's record before
// the regenerate (retireServerRecord).
func (g *SessionScheme) recordServerSession(r *http.Request, session auth.Session, user auth.Authenticatable) {
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
	now := sessionclock.Now()
	rec := &auth.StoredSession{
		ID:         sessionID,
		UserID:     userID,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  g.recordExpiry(now, now),
		IPAddress:  g.clientIP(r),
		UserAgent:  r.Header.Get("User-Agent"),
	}
	if err := store.Put(r.Context(), rec); err != nil {
		g.logWarn("velocity/auth: server session store put (login) failed", "session_id", sessionID, "error", err)
	}
}

// retireServerRecord removes the server record of session id, the session a
// sign-in rotates away from, so a captured copy of its cookie is refused on
// every instance that consults the record store. No store, an empty id and
// an id with no record are nothing to retire; any other store failure is
// returned so the sign-in fails closed.
func (g *SessionScheme) retireServerRecord(r *http.Request, id string) error {
	store := g.getServerStore()
	if store == nil || id == "" {
		return nil
	}
	if err := store.Delete(r.Context(), id); err != nil && !errors.Is(err, auth.ErrSessionNotFound) {
		return err
	}
	return nil
}

// ClearRememberTokensForUser implements auth.RememberTokenClearer. It
// resets the user's persistent remember-me token via the configured
// UserStore so a "sign out everywhere" admin action also invalidates
// the remember cookie path. A missing user (auth.ErrUserNotFound, or no
// user and no error) is a no-op: the remember credential cannot resurrect
// what does not exist. Any other lookup failure is returned: the
// credential may still be valid, and the caller (Manager.RevokeSession,
// Manager.RevokeAllSessions) must not report it ended.
//
// Note: remember tokens are per-user, not per-session. Manager.RevokeSession
// calls this too: a remember credential cannot be told apart per session,
// so ending the revoked session's remember-me ends the one the user holds.
func (g *SessionScheme) ClearRememberTokensForUser(ctx context.Context, userID string) error {
	userStore := g.loadUserStore()
	user, err := userStore.FindByIDCtx(ctx, userID)
	if errors.Is(err, auth.ErrUserNotFound) || (err == nil && user == nil) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("velocity/auth: remember token not cleared: user lookup failed: %w", err)
	}
	return userStore.UpdateRememberTokenCtx(ctx, user, "")
}

// clientIP returns the originating client IP for r, honouring the
// scheme's configured trusted-proxy list. When no proxies are trusted
// (the default), the result is the host portion of r.RemoteAddr with
// the ephemeral TCP port stripped. When the request arrives from a
// trusted proxy, RFC 7239 Forwarded / X-Forwarded-For / X-Real-IP
// resolution kicks in (see internal/clientip).
//
// The string is recorded on auth.StoredSession.IPAddress so audit
// listings show the real client, not the load balancer, and so the
// administrative "Sign out everywhere" UX surfaces meaningful IPs.
// Returns "" when r.RemoteAddr is unparseable.
func (g *SessionScheme) clientIP(r *http.Request) string {
	return clientip.ExtractString(r, g.getTrustedProxies())
}

// checkRememberCookie checks and validates remember cookie.
// Returns the authenticated user if the cookie is valid, nil otherwise.
//
// Validation only: rotate-on-use (V2-08) happens in anchorRecalledUser,
// which calls rotateRememberToken once the revival fully anchors, so a
// recall that fails fixation/store checks does not burn the token.
func (g *SessionScheme) checkRememberCookie(r *http.Request) auth.Authenticatable {
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

	// The payload is userID|issuedAt|token (see mintRememberCookie). The
	// credential ends RememberTimeout after it was issued, on the server:
	// the cookie's Max-Age only tells the browser when to drop it, and a
	// captured copy replayed by hand must not outlive it.
	userID, issuedAt, token, ok := parseRememberPayload(decrypted)
	if !ok {
		return nil
	}
	if sessionclock.Now().After(issuedAt.Add(g.config.RememberTimeout())) {
		return nil
	}

	// Look up user by ID
	user, err := g.loadUserStore().FindByIDCtx(r.Context(), userID)
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
	if crypto.EqualString(storedToken, candidateHash) {
		return user
	}
	if crypto.EqualString(storedToken, token) {
		return user
	}
	return nil
}

// setRememberCookie sets the remember me cookie at login, persisting the
// new token hash unconditionally through the user store (there is no prior
// credential to guard against; login may always overwrite). ctx is the
// request context so a client disconnect aborts the user store write.
func (g *SessionScheme) setRememberCookie(ctx context.Context, w http.ResponseWriter, user auth.Authenticatable) error {
	cookie, err := g.mintRememberCookie(user, func(hashed string) error {
		return g.loadUserStore().UpdateRememberTokenCtx(ctx, user, hashed)
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, cookie)
	return nil
}

// mintRememberCookie mints a fresh remember token, encrypts the cookie
// payload, persists the token's SHA-256 hash through persist, and returns
// the cookie for the caller to write. The raw token is encrypted into the
// cookie with the user id and the issue time; only its hash reaches the
// user record. The credential lives SessionConfig.RememberTimeout from its
// issue, enforced on recall (checkRememberCookie) and told to the browser
// as the cookie's Max-Age, independent of the session lifetime.
//
// Encryption runs BEFORE persist so an encryptor failure cannot strand
// the user: overwriting the stored hash while unable to deliver the
// replacement cookie would silently sign the device out.
func (g *SessionScheme) mintRememberCookie(user auth.Authenticatable, persist func(hashed string) error) (*http.Cookie, error) {
	ttl := g.config.RememberTimeout()

	// Generate remember token.
	token, err := generateRememberToken()
	if err != nil {
		return nil, err
	}

	// GetAuthIdentifier returns interface{}: a uint for the default
	// integer primary key (auth.NormalizeID) and a string for
	// UUID keys. Encode whatever it is as a string so both round-trip;
	// checkRememberCookie reads it back and hands it to FindByID, which
	// accepts either form. A bare .(string) assertion here silently broke
	// remember-me for every integer-PK app (the default shape).
	issuedAt := sessionclock.Now()
	value := rememberPayload(fmt.Sprint(user.GetAuthIdentifier()), issuedAt, token)

	// Encrypt value. The encryptor authenticates the payload, so the
	// issue time cannot be altered.
	if g.encryptor == nil {
		return nil, errors.New("velocity/auth: encryptor not configured, cannot set remember cookie")
	}
	encrypted, err := g.encryptor.Encrypt(value)
	if err != nil {
		return nil, err
	}

	// Store only the hash of the token on the user record. The in-memory
	// user is mutated only after a successful persist so a failed (or
	// lost-race) write leaves the object holding the hash that is still
	// authoritative in the store.
	hashed := hashRememberToken(token)
	if err := persist(hashed); err != nil {
		return nil, err
	}
	user.SetRememberToken(hashed)

	cookie := g.config.CookiePolicy().Cookie("remember_"+g.config.Name, encrypted, int(ttl.Seconds()), true)
	cookie.Expires = issuedAt.Add(ttl)
	return cookie, nil
}

// rememberPayload is the plaintext of a remember cookie:
// userID|issuedAt|token, the issue time in Unix seconds. The token is
// base64url and the time decimal, so neither holds the separator.
func rememberPayload(userID string, issuedAt time.Time, token string) string {
	return userID + "|" + strconv.FormatInt(issuedAt.Unix(), 10) + "|" + token
}

// parseRememberPayload splits a remember cookie plaintext written by
// rememberPayload. The token and the issue time are taken from the right,
// so the user id is everything before them.
func parseRememberPayload(payload string) (userID string, issuedAt time.Time, token string, ok bool) {
	i := strings.LastIndexByte(payload, '|')
	if i < 0 {
		return "", time.Time{}, "", false
	}
	rest, token := payload[:i], payload[i+1:]
	j := strings.LastIndexByte(rest, '|')
	if j < 0 {
		return "", time.Time{}, "", false
	}
	userID = rest[:j]
	unix, err := strconv.ParseInt(rest[j+1:], 10, 64)
	if err != nil || userID == "" || token == "" {
		return "", time.Time{}, "", false
	}
	return userID, time.Unix(unix, 0), token, true
}

// clearRememberCookie clears remember me cookie
func (g *SessionScheme) clearRememberCookie(w http.ResponseWriter) {
	http.SetCookie(w, g.config.CookiePolicy().Cookie("remember_"+g.config.Name, "", -1, true))
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
