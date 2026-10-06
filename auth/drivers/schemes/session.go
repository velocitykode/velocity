package schemes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
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
	"github.com/velocitykode/velocity/auth/internal/identity"
	"github.com/velocitykode/velocity/auth/internal/sessionref"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/clientip"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/nilval"
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
	session   contract.Session
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
	// remember-me recall) that changed the request's session; see
	// afterSaveWrite.
	transition uint64
	// sealed is set by the seam's commit, in the step that reserves the
	// gate for it (reserveCommit), before it attempts the save: the
	// request's one commit is taken (whether or not its save then
	// succeeds), so a sign-in or recall from here on could change nothing
	// that is saved and is refused.
	sealed bool
	// committedID is the id of the live session the commit saved (or
	// found unchanged), the one the browser holds once the response is
	// delivered; empty before the commit and when it ended the session.
	// A session ended after the commit (a Logout, or a deletion of the
	// session cookie, during delivery) is retired under this id, whatever
	// the session object reports by then.
	committedID string
	// queueClosed is set once the commit delivered the queued writes (or
	// dropped them): QueueAfterSessionSave refuses from then on, since
	// nothing would run a write queued later. A write queued while the
	// delivery runs is delivered with it.
	queueClosed bool

	// busy is the request's authentication gate (see gate.go): set while
	// an operation (a read of the user resolving it, ResolveSession,
	// Login, LoginByID, Attempt, Logout, the commit) holds it. Another
	// operation fails closed while it is set, and a read of the user waits
	// only for a read on another goroutine, so none sees the provisional
	// identity of a transition in flight, and the commit never saves a
	// session halfway through one. The commit frees it before it delivers
	// the queued writes, so a write may read the signed-in user.
	busy bool
	// torn is set when an operation was unwound by a panic after it began
	// changing the session: no read uses the session and the commit does
	// not save it until a later sign-in or logout publishes a whole state.
	torn bool
	// ident is the request's published identity (see gate.go): the
	// outcome of the read of the signed-in user that resolved it, returned
	// by every later read. Nil when none is published; cleared whenever
	// an operation that may change the session reserves the gate, and by
	// an operation torn by a panic.
	ident *resolvedIdentity
	// resolving marks the gate as held by the request's resolver (a read
	// of the user, or ResolveSession) for its turn resolution. Another
	// goroutine's read waits for the turn to end; waiters counts the
	// reads doing so.
	resolving  bool
	resolution *resolution
	waiters    int
	// ended is set when a Logout of the request published: the holder's
	// session is ended, whatever the session object reports (a custom
	// contract.Session may not say it was invalidated). It is never saved as
	// live nor reused for a sign-in: a Login that follows starts from a
	// fresh session, and publishing it clears the mark.
	ended bool

	// commitOnce makes the session middleware's commit run once per
	// request, whichever write or return fires it.
	commitOnce sync.Once
}

// afterSaveWrite is one write queued behind the session save.
type afterSaveWrite struct {
	write func(w http.ResponseWriter)
	// settle, when set, is the part of write that changes state outside
	// the response (a sign-in's remember token reaching the user store).
	// The seam runs it once the save succeeded, while it still holds the
	// request's gate, so a transition that follows the commit (a logout
	// run while the writes are delivered) comes after it.
	settle func()
	// undo, when set, reverses a change made outside the session for
	// write; the seam runs it when the save fails.
	undo func()
	// transition is the authentication transition that queued a sign-in's
	// credential write, the transition in effect when a session-bound
	// write was queued (see sessionBound), or 0 for a write bound to no
	// transition. A later transition of the same request supersedes a
	// credential or session-bound write: a remember-me sign-in followed by
	// a logout, or by another sign-in, must not have its credentials
	// delivered by the save that persists what came after.
	transition uint64
	// sessionBound marks a write queued through QueueSessionBoundWrite
	// (the XSRF-TOKEN a safe request bootstraps): it names state of the
	// session the request was served under when it was queued, so a later
	// transition that replaced that session supersedes it even when it
	// was queued before any transition (transition 0). Its id is never
	// saved, and the transition queues the writes of the session that
	// replaced it.
	sessionBound bool
}

// errSessionSaved refuses a sign-in the request attempts after its session
// was saved: the one save ran, so nothing the sign-in changed would be
// saved or delivered.
var errSessionSaved = errors.New("velocity/auth: sign-in refused: the request's session was already saved")

// isSealed reports whether the request's one commit was taken already.
func (h *sessionHolder) isSealed() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sealed
}

// isEnded reports whether a Logout of the request ended the holder's
// session (see ended).
func (h *sessionHolder) isEnded() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ended
}

// queueAfterSave appends write to the writes the seam runs after the
// session save, and reports whether it did: false once the queue is closed
// (see queueClosed). A sessionBound write is bound to the transition in
// effect, so a later one supersedes it; any other is bound to no
// transition.
func (h *sessionHolder) queueAfterSave(write func(w http.ResponseWriter), sessionBound bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.queueClosed {
		return false
	}
	e := afterSaveWrite{write: write, sessionBound: sessionBound}
	if sessionBound {
		e.transition = h.transition
	}
	h.afterSave = append(h.afterSave, e)
	return true
}

// takeDeliveredLate returns the writes queued while the seam delivered the
// queue, for the delivery to run too, or closes the queue and returns nil
// when there are none: the check and the closure are one step, so a write
// is either delivered or refused at registration.
func (h *sessionHolder) takeDeliveredLate() []func(w http.ResponseWriter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.afterSave) == 0 {
		h.queueClosed = true
		return nil
	}
	writes := make([]func(w http.ResponseWriter), 0, len(h.afterSave))
	for _, e := range h.afterSave {
		writes = append(writes, e.write)
	}
	h.afterSave = nil
	return writes
}

// closeQueue closes the queue and drops what it holds: the save failed or
// ended the session, so nothing queued behind it runs.
func (h *sessionHolder) closeQueue() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queueClosed = true
	h.afterSave = nil
}

// setCommittedID records id as the session the commit issued (see
// committedID).
func (h *sessionHolder) setCommittedID(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.committedID = id
}

// committed returns the id of the session the commit issued, or "".
func (h *sessionHolder) committed() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.committedID
}

// queuedWrites is what the seam may still run of the queue.
type queuedWrites struct {
	// writes and settle belong to entries bound to no transition or to
	// the latest one; settle runs after a successful save, then writes.
	writes []func(w http.ResponseWriter)
	settle []func()
	// undo holds every entry's undo step, superseded or not: a failed
	// save delivered no credential of the request (no session cookie, and
	// none of the queued writes), so every change made outside the session
	// for it is reversed. A failed save is not atomic in the store: a
	// server record may have been written before the failure (a cache
	// store whose sign-in index update failed after the record was
	// swapped), but it names an id no client received. Each undo is
	// conditional on the state its change left, so it never reverses a
	// later change.
	undo []func()
}

// takeAfterSave empties the queue, so each entry runs at most once, and
// returns what the seam may still run (see queuedWrites). A superseded
// transition's writes are dropped. When the session was ended (it is
// saved destroyed) no write runs, since none may follow a session that no
// longer exists, and the queue is closed; the undo steps are still
// returned.
func (h *sessionHolder) takeAfterSave(sessionEnded bool) queuedWrites {
	h.mu.Lock()
	defer h.mu.Unlock()
	var q queuedWrites
	if sessionEnded {
		h.queueClosed = true
	}
	for _, e := range h.afterSave {
		if e.undo != nil {
			q.undo = append(q.undo, e.undo)
		}
		superseded := (e.transition != 0 || e.sessionBound) && e.transition != h.transition
		if sessionEnded || superseded {
			continue
		}
		if e.settle != nil {
			q.settle = append(q.settle, e.settle)
		}
		q.writes = append(q.writes, e.write)
	}
	h.afterSave = nil
	return q
}

// QueueAfterSessionSave queues write to run once the session r is served
// under has been saved, and reports whether it did. The CSRF middleware
// defers its XSRF-TOKEN cookie through it, so the cookie never names a
// token kept in a session that was not saved.
//
// It reports false when write will never run:
//
//   - r runs outside the session middleware: there is no save to follow,
//     and the caller writes at once;
//   - the request's queue is closed: the session save and its delivery are
//     over (the handler already wrote the response, or its commit ran),
//     so the response is committed or about to be and the caller must not
//     write the cookie at all.
//
// A write queued while the queued writes are delivered (from inside one of
// them) runs as part of that delivery. A failed save, or a save that ended
// the session, drops write. A sign-in or logout later in the request does
// not: write is bound to the request, not to the session it was served
// under when write was queued (see QueueSessionBoundWrite).
//
// write gets the response's headers only, before they are sent: its
// Header() is the response's header map, so http.SetCookie and header
// changes land in the response, while Write returns an error and writes
// nothing and WriteHeader is ignored; the response body and status are the
// handler's. The writer has no other capability (no Flush, Hijack or
// Unwrap): an unchecked assertion to one panics. write must not write the
// response through any other handle (a router.Context or writer it
// captured) either: the response is being committed when it runs, and a
// write through the router's writer never returns. This is not enforced.
//
// A write that panics ends the delivery: the writes after it do not run,
// the queue is closed, and a deletion of the session cookie an earlier
// write added still ends the session before the panic reaches the router,
// whose error response carries the response's cookies.
//
// write runs after the commit freed the request's authentication gate, so
// it may read the signed-in user (User, Check, ID). The request's session is
// already saved and sealed by then: a Login it calls is refused, a
// remember-me recall does not run, the session's id cannot be regenerated
// (auth.ErrSessionSealed), and a CSRF token rotation is refused. A Logout
// ends the session the save issued server-side but cannot delete the
// session cookie on this response; a deletion of the session cookie it
// adds (Context.DeleteCookie) ends the session as a handler's deletion
// does, and replaces the session cookie the save issued. The seal covers
// the id and the CSRF token only, and only on sessions that implement it
// (auth.BaseSession.Seal; a custom session without it is protected by the
// Logout's retirement of the issued id alone): Put, Remove and flash
// writes are accepted and never saved, Invalidate is not a Logout (it
// retires nothing), and an explicit Save still writes.
func QueueAfterSessionSave(r *http.Request, write func(w http.ResponseWriter)) bool {
	return queueBehindSave(r, write, false)
}

// QueueSessionBoundWrite is QueueAfterSessionSave for a write that names
// state of the session r is served under when it is called, such as the
// XSRF-TOKEN cookie a safe request bootstraps: besides the cases that drop
// a QueueAfterSessionSave write, a sign-in, remember-me recall or logout
// that replaces the session later in the request drops it. The replaced
// session's id is never saved, so a cookie naming it would name nothing
// the client holds, and the transition queues the writes of the session
// that replaced it (a sign-in or recall writes its own XSRF-TOKEN). It
// reports false in the same cases as QueueAfterSessionSave. velocity.New
// wires it as the CSRF middleware's csrf.Config.QueueAfterSessionSave.
func QueueSessionBoundWrite(r *http.Request, write func(w http.ResponseWriter)) bool {
	return queueBehindSave(r, write, true)
}

// queueBehindSave queues write on r's session holder (see
// QueueAfterSessionSave and QueueSessionBoundWrite).
func queueBehindSave(r *http.Request, write func(w http.ResponseWriter), sessionBound bool) bool {
	if r == nil || write == nil {
		return false
	}
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil || holder.getResponseWriter() == nil {
		return false
	}
	return holder.queueAfterSave(write, sessionBound)
}

// seamHolder returns r's session holder when r runs inside
// SessionMiddleware, the one place a session is saved. Otherwise (the
// scheme driven from a plain net/http handler, a script or a test, with
// no middleware around it) it returns a fresh holder for the one scheme
// operation and standalone true: the operation is its own save scope and
// commits through the same seam body when it ends, so its write is
// neither lost nor saved twice. anchor is then the holder
// WithSessionContext attached to r, which the operation reserves too; a
// standalone operation with no anchor is refused (see reserveOperation).
func seamHolder(r *http.Request) (holder *sessionHolder, standalone bool, anchor *sessionHolder) {
	holder, _ = r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if holder != nil && holder.getResponseWriter() != nil {
		return holder, false, nil
	}
	return &sessionHolder{saveScope: true}, true, holder
}

// isTorn reports whether an operation of the request was torn (see
// sessionHolder.torn); false for a nil holder.
func (h *sessionHolder) isTorn() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.torn
}

// getSession returns the cached session under a read lock.
func (h *sessionHolder) getSession() contract.Session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.session
}

// installLoaded caches s, a session just loaded or created for the
// request, unless another goroutine of the request cached one first, and
// returns the cached session: the first load wins, so every goroutine of
// the request, and every operation, works on one session object.
func (h *sessionHolder) installLoaded(s contract.Session) contract.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.session == nil {
		h.session = s
	}
	return h.session
}

// setSession installs s as the cached session under a write lock.
func (h *sessionHolder) setSession(s contract.Session) {
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

// WithSessionContext returns a new request with a session context attached
// to its context: the per-request holder the scheme caches the session in
// and reserves while an operation that may change the session runs. The
// session middleware attaches one itself. A caller outside it (a plain
// net/http handler, a script, a test) must wrap the request before calling
// Login, LoginByID, Attempt or Logout, and must pass the request this
// function returns: the context is on the returned request, not on r, and
// an operation given a request without one returns
// auth.ErrNoSessionContext before any side effect.
//
// Known limit: the authentication reads (Check, CheckWithError, User, ID,
// ResolveSession) still answer on a request with no session context. They
// commit nothing and write no cookie, and with nothing to reserve they run
// outside the request's gate: a store that calls a read back for such a
// request is not refused.
//
// A read is not free of side effects there. When such a request presents
// a valid remember cookie, Check, CheckWithError, User and ID start the
// recall, and the recall is refused only at its last step, where it finds
// nowhere to deliver the rotated credential. By then it has rotated the
// CSRF token (the rotator's RotateToken ran for a session id no client
// receives) and, with a server session store, written a record for that
// id, which stays until it expires. The read answers signed out and the
// presented remember credential is left as it was.
func WithSessionContext(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionCtxKey{}, &sessionHolder{}))
}

// sessionFromHolder returns the session cached on r's holder, or nil when no
// handler in the request resolved one (the holder was attached but
// SessionScheme.getSession was never called). Test helper / middleware helper
// only; nil is a normal outcome.
func sessionFromHolder(r *http.Request) contract.Session {
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		return nil
	}
	return holder.getSession()
}

// SessionFromRequest returns the session attached to r via
// WithSessionContext + SessionMiddleware (eager-bootstrap or
// handler-resolved), or nil when no session has been bound yet. It does
// not ask whether anything saves that session: a holder
// WithSessionContext attached on its own answers too. Code that writes
// into the session uses SessionFromContext, and the framework's CSRF
// session resolution uses SessionScheme.ResolveSession, which refuses a
// fresh session outside a save scope.
func SessionFromRequest(r *http.Request) contract.Session {
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
func SessionFromContext(ctx context.Context) contract.Session {
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
func sessionContext(r *http.Request, session contract.Session) context.Context {
	if SessionFromContext(r.Context()) == session {
		return r.Context()
	}
	return context.WithValue(r.Context(), sessionCtxKey{}, &sessionHolder{session: session, saveScope: true})
}

// modifiedSession is the optional capability the scheme reads a session's
// state through: whether it was ended (IsDestroyed, set once and never
// cleared) and whether it carries unsaved changes (IsModified).
// *auth.BaseSession (and therefore the framework's sessions via embedding)
// satisfies it; mock sessions in tests can opt in by exposing both.
//
// Whether a session is saved is never decided on IsModified: the save seam
// always calls Save and the session decides inside its own save, since a
// save in flight has cleared the mark and puts it back when it fails.
type modifiedSession interface {
	IsModified() bool
	IsDestroyed() bool
}

// activityRefreshInterval is the minimum interval between activity
// refreshes for a given session: the server record's Touch (LastSeenAt and
// the slid ExpiresAt) and the cookie's re-issue share it, so the record
// and the cookie slide on the same rule. Reads happen on every
// authenticated request to honor revocation; writes are spaced so a chatty
// client does not generate one extra store write and one cookie rewrite
// per request. It is a minute, which keeps the idle window accurate to a
// minute and gives the "active sessions" UI accurate timestamps without
// amplifying write volume, or half the idle window when that is shorter,
// so a one-minute idle timeout still slides for an active client (see
// auth.SessionConfig.ActivityRefreshInterval).
func (g *SessionScheme) activityRefreshInterval() time.Duration {
	return g.config.ActivityRefreshInterval()
}

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
	logger         contract.Logger
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

	// events holds the framework event dispatcher installed by
	// auth.Manager.SetEventDispatcher and applies the failure policy to a
	// failed dispatch (see internal/eventemit). Used to emit
	// auth.PasswordNeedsRehashEvent after a successful Attempt against
	// a stored hash that no longer matches the configured Hasher
	// parameters (M-08). No dispatcher disables event emission.
	events eventemit.Emitter

	// rememberUnsupportedLogged is set once the scheme has logged that a
	// request presented a remember cookie its user store cannot consume
	// (see rememberStore), so the warning is written once per scheme.
	rememberUnsupportedLogged atomic.Bool
}

// rememberStore returns the user store's remember-token compare-and-swap,
// the one way the scheme consumes a remember credential; ok is false when
// the user store lacks the capability (or is a nil value), and the scheme
// then has no remember-me: it issues no credential and honours none.
func (g *SessionScheme) rememberStore() (cas auth.RememberTokenCompareAndSwapper, ok bool) {
	return rememberCapability(g.loadUserStore())
}

// rememberCapability is rememberStore for one snapshot of the user store,
// so a caller that also looks the user up asks the same store both times.
func rememberCapability(userStore auth.UserStore) (cas auth.RememberTokenCompareAndSwapper, ok bool) {
	if nilval.Is(userStore) {
		return nil, false
	}
	cas, ok = userStore.(auth.RememberTokenCompareAndSwapper)
	return cas, ok
}

// rememberMatch is a remember cookie that validated: the user it names,
// the stored hash the presented token matched, and the store that hash was
// read from. Whatever consumes the credential afterwards (the rotation of a
// recall, the burn of a revoked session) compare-and-swaps from this hash
// on this store. It never reads the token from the user value again: a
// store that hands out a shared user value would show the hash a concurrent
// recall already swapped in, and the second swap would land on a credential
// this request never presented.
type rememberMatch struct {
	user  contract.Authenticatable
	hash  string
	store auth.RememberTokenCompareAndSwapper
	// expiresAt is when the presented credential ends: its issue time plus
	// SessionConfig.RememberTimeout.
	expiresAt time.Time
}

// errRememberCredentialEnded reports a recall refused because the presented
// remember credential's lifetime ran out while the recall was under way.
var errRememberCredentialEnded = errors.New("velocity/auth: the remember credential's lifetime ended during the recall")

// ended reports whether the presented credential's lifetime is over on the
// session clock. A recall mints credentials (an authenticated session, a
// replacement remember token), so its lifetime is not decided once when the
// cookie is read: every step that mints asks again, after the slow calls of
// user code before it (the user lookup, the CSRF rotation, the record
// store), which can outlast what was left of the lifetime. Asked once, in
// front of the lookup, a credential that expired during a slow lookup still
// signed its holder in and was replaced by a fresh one.
func (m rememberMatch) ended() bool {
	return sessionclock.Now().After(m.expiresAt)
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
	if nilval.Is(h) {
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
		if nilval.Is(store) {
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
	if nilval.Is(t) {
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

// SetLogger installs a logger used for the scheme's non-fatal warnings
// (store errors, a session cookie too large to send, a refused revival,
// a queued write that touched the response body). Unset or nil, they go
// through the framework's standalone fallback logger, which writes
// warnings and errors to standard error.
//
// Manager.SetLogger propagates to every registered scheme implementing
// contract.LoggerAware, and Manager.RegisterScheme hands the manager's
// logger to a scheme registered later, so a bootstrapped app logs through
// its framework logger without calling this directly.
func (g *SessionScheme) SetLogger(l contract.Logger) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.logger = l
}

var _ contract.LoggerAware = (*SessionScheme)(nil)

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
	// A failed dispatch is logged through the scheme's logger as it is at
	// the time of the failure. Installed here, not in the constructor, so
	// a scheme built as a literal gets it too.
	g.events.UseLogger(g.currentLogger)
	g.events.Set(fn)
}

// currentLogger returns the installed logger under a read lock, or nil.
func (g *SessionScheme) currentLogger() contract.Logger {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.logger
}

// getCSRFTokenRotator returns the installed rotator under a read lock so
// concurrent Login / Logout / recall paths see a consistent snapshot.
// Returns nil when none has been configured (rotation becomes a no-op).
func (g *SessionScheme) getCSRFTokenRotator() contract.CSRFTokenRotator {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.csrfRotator
}

// logWarn emits a warn event through the installed logger, or the
// framework's standalone fallback logger when none has been installed. A
// logger that panics is contained (the line goes to the fallback logger):
// most of these lines are written in the middle of a transition or its
// teardown (a Logout's revocations, a commit's cleanup), which must run
// whatever the logger does.
//
// No lock is held when it runs; a blocking logger stalls only the request
// that logs.
func (g *SessionScheme) logWarn(msg string, kvs ...any) {
	g.mu.RLock()
	l := g.logger
	g.mu.RUnlock()
	fallbacklog.Write(l, func(l contract.Logger) { l.Warn(msg, kvs...) })
}

// Check reports whether the request is authenticated. When a server-side
// session store has been installed, it is consulted: a revoked or expired
// record causes Check to return false even though the cookie itself is
// still valid.
//
// Inside the session middleware (or with WithSessionContext) the request
// is authenticated once between operations that may change its session:
// the first read of a request resolves the user and later reads (Check,
// CheckWithError, User, ID) return that answer without asking the stores
// again, until a Login, Logout or the commit changes it. A user deleted
// in the store mid-request stays signed in for the rest of that request.
// Reads on several goroutines of one request share the one resolution: a
// read that arrives while another goroutine's resolves waits for it, or
// for the end of the request's context. A cycle through goroutines the
// user code creates (a store that starts a goroutine which reads the user
// and waits for it) is not supported: it ends when the request's context
// does.
//
// Errors (including ErrSessionRevoked) are swallowed; callers that need to
// distinguish causes should use CheckWithError instead.
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
//   - auth.ErrOperationInProgress: another authentication operation of
//     the same request (a Login, Logout or the commit on another
//     goroutine, or the operation or read whose store is calling back)
//     is in flight; fail-closed, without waiting. A read of the user on
//     another goroutine is waited for instead (see Check); a read that
//     gives up waiting because the request's context ended returns this
//     error too
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
// The ladder runs once per request between operations that may change
// the session: the first read reserves the request's authentication gate,
// walks it with no lock held, and publishes the outcome (see gate.go);
// later reads return the published outcome. A read on another goroutine
// of the request waits while the first one runs; a read that re-enters
// it, or that meets a Login, Logout or commit in flight, fails closed with
// auth.ErrOperationInProgress. A request an operation was torn on reads as
// signed out. Without a session holder on the request (the scheme driven
// outside SessionMiddleware and WithSessionContext) there is nothing to
// publish into, and each read walks the ladder on the session it loads.
//
// Returns the resolved user, whether the request is authenticated, and
// the reason it is not (nil on the ordinary unauthenticated paths). Error
// policy is owned by the callers: CheckWithError surfaces err while User
// swallows everything to nil.
func (g *SessionScheme) resolveAuthenticatedUser(r *http.Request) (contract.Authenticatable, bool, error) {
	holder, _ := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if holder == nil {
		session := g.getSession(r)
		if session == nil {
			return nil, false, nil
		}
		var op gateOp
		return g.resolveAuthenticationChange(r, session, &op)
	}
	if id, err, ok := holder.published(); ok {
		if err != nil {
			return nil, false, err
		}
		return id.user, id.ok, id.err
	}
	var op gateOp
	id, err := holder.readTurn(r, &op, true)
	if err != nil {
		return nil, false, err
	}
	if id != nil {
		return id.user, id.ok, id.err
	}
	return g.resolveReserved(r, &op)
}

// resolveReserved is resolveAuthenticatedUser's turn: it walks the ladder
// holding the request's gate for op as the request's resolver, publishes
// the outcome and frees the gate. A read that meets it from its own
// goroutine is refused by its frame (see onResolvePath).
func (g *SessionScheme) resolveReserved(r *http.Request, op *gateOp) (contract.Authenticatable, bool, error) {
	defer op.abort()
	var (
		res     resolvedIdentity
		session = g.getSession(r)
	)
	if session != nil {
		res.user, res.ok, res.err = g.resolveAuthenticationChange(r, session, op)
	}
	op.identity = &res
	// A read that changed nothing (no recall) is published even after the
	// commit sealed the request: it only reports the saved state.
	if !op.apply(op.mutated) {
		// The response was committed while the recall ran: nothing it
		// changed is saved, so the request is not signed in by it.
		if res.ok {
			op.beginMutation()
			session.Remove(auth.UserIDSessionKey)
		}
		op.release()
		return nil, false, nil
	}
	op.release()
	return res.user, res.ok, res.err
}

// resolveAuthenticationChange is resolveAuthenticatedUser's ladder for
// session. The caller holds the request's gate for op.
func (g *SessionScheme) resolveAuthenticationChange(r *http.Request, session contract.Session, op *gateOp) (contract.Authenticatable, bool, error) {
	// A remember cookie this scheme cannot honour goes on every request
	// that presents one, signed in or not, before the ladder decides
	// whether a recall is tried at all.
	g.dropUnsupportedRememberCookie(r)

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
		if match, ok := g.matchRememberCookie(r); ok {
			if !g.anchorRecalledUser(r, session, match, op) {
				return nil, false, nil
			}
			return match.user, true, nil
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
		case errchain.Is(err, auth.ErrSessionExpired):
			// The lifetime policy ended the session on its server record
			// while the cookie is still live: the identity it carries is
			// stale. A valid remember cookie signs the user back in on a
			// new session, exactly as when the cookie itself expired.
			if match, ok := g.matchRememberCookie(r); ok {
				op.beginMutation()
				session.Remove(auth.UserIDSessionKey)
				if g.anchorRecalledUser(r, session, match, op) {
					return match.user, true, nil
				}
			}
		case errchain.Is(err, auth.ErrSessionRevoked):
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
// hash the cookie validated against (rememberMatch), never an unconditional
// write, so a credential a concurrent Login just minted elsewhere survives.
// A scheme whose user store has no compare-and-swap never issued a
// credential: matchRememberCookie reports none and there is nothing stored
// to clear.
func (g *SessionScheme) burnPresentedRememberToken(r *http.Request) {
	if _, err := r.Cookie("remember_" + g.config.Name); err != nil {
		return
	}
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
		if w := holder.getResponseWriter(); w != nil {
			g.clearRememberCookie(w)
		}
	}
	match, ok := g.matchRememberCookie(r)
	if !ok {
		return
	}
	if _, err := match.store.CompareAndSwapRememberToken(r.Context(), match.user, match.hash, ""); err != nil {
		g.logWarn("velocity/auth: clear remember token (revoked session) failed", "error", err)
	}
}

// User returns the authenticated user, or nil when the request is not
// authenticated. When a server-side session store is configured, a revoked
// or missing record causes User to return nil even when the cookie is
// otherwise valid. The request is authenticated once between operations
// that may change its session, and later reads return the same user (the
// same object) without asking the stores again; see Check.
//
// Remember-cookie revival (H-08 fix): when the session does not yet carry
// a user_id but the remember cookie is valid, the request is treated as a
// full re-authentication: the session ID is rotated (defeats fixation),
// user_id is anchored on the new session, and the server-side session
// store (when configured) is consulted on the rotated ID. If the store is
// configured and the write/lookup fails, User returns nil.
func (g *SessionScheme) User(r *http.Request) contract.Authenticatable {
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
//
// The caller holds the request's gate for op, which stages the recall's
// transition and credential writes until it ends.
func (g *SessionScheme) anchorRecalledUser(r *http.Request, session contract.Session, match rememberMatch, op *gateOp) bool {
	user := match.user
	// Capture the pre-rotation id so the CSRF rotator (when wired) can
	// drop any token bound to the planted id. Required to keep the
	// session-fixation defense complete: H-02 says the CSRF token MUST
	// follow Session.Regenerate, and this is the revival entry point
	// reached from both User() and CheckWithError() (G2's H-08).
	oldSessionID := session.ID()

	// The recalled user's identifier is the session's key to the user:
	// read it before anything changes, so an unreadable one revives
	// nothing and leaves the presented credential as it is.
	userID, _, err := identity.Of(user)
	if err != nil {
		g.logWarn("velocity/auth: remember-cookie revival refused", "error", err)
		return false
	}

	// A recall after the request's session was saved could not deliver
	// the replacement credential its rotation mints, so it is refused and
	// the presented credential stays as it is.
	holder, _ := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if holder != nil && holder.isSealed() {
		g.logWarn("velocity/auth: remember-cookie revival refused: the request's session was already saved")
		return false
	}

	// Rotate the session id BEFORE writing user_id so an attacker who
	// planted the prior id can no longer inherit authenticated state.
	op.beginMutation()
	if err := session.Regenerate(); err != nil {
		g.logWarn("velocity/auth: remember-cookie revival: session regenerate failed", "error", err)
		return false
	}
	// The recall is a transition of its own once the session is
	// regenerated: credential writes an earlier transition of this
	// request queued are superseded from here on.
	op.beginTransition()

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
			// `session` is the key every warning names the request's
			// current session by; the two sides of the rotation keep
			// their own keys beside it.
			current := sessionref.Of(session.ID())
			g.logWarn("velocity/auth: remember-cookie revival: csrf token rotate failed", "session", current, "old_session", sessionref.Of(oldSessionID), "new_session", current, "error", err)
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
			op.queueCredentialWrite(afterSaveWrite{write: func(w http.ResponseWriter) {
				rotator.WriteXSRFCookie(rotateCtx, w, newID)
			}})
		}
	}

	// The session is about to be given the user: the credential's lifetime
	// is decided again here, after the CSRF rotation (user code) above.
	if match.ended() {
		g.logWarn("velocity/auth: remember-cookie revival refused", "error", errRememberCredentialEnded)
		return false
	}
	session.Put(auth.UserIDSessionKey, userID)

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
	if err := g.rotateRememberToken(r, match, op); err != nil {
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
//   - the presented credential's lifetime ended during the recall,
//   - the user store does not implement auth.RememberTokenCompareAndSwapper
//     (auth.ErrRememberTokenStoreUnsupported; matchRememberCookie honours
//     no cookie on such a scheme, so a recall does not get here), or
//   - the stored hash no longer matches the presented token.
//
// The compare-and-swap is what closes the parallel-recall race: two
// requests presenting the same old cookie both validate before either
// write, but only one swap can land; the loser fails here instead of
// minting a second valid credential via last-writer-wins. An unconditional
// UpdateRememberTokenCtx cannot give that guarantee, so a scheme whose user
// store lacks the capability has no remember-me (see
// auth.ErrRememberTokenStoreUnsupported); the unconditional update writes
// the token only where none is consumed: the sign-in that issues it.
//
// Rotation is strict; there is no grace window for the previous token.
// The storage shape (a single remember_token hash on the user record)
// offers no durable slot for a previous-token grace entry, and scheme-local
// memory would not survive multi-host deployments, so we fail secure: at
// worst the user signs in again.
func (g *SessionScheme) rotateRememberToken(r *http.Request, match rememberMatch, op *gateOp) error {
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		return errors.New("velocity/auth: no session holder on request; cannot deliver rotated remember cookie")
	}
	if holder.getResponseWriter() == nil {
		return errors.New("velocity/auth: no response writer on request; cannot deliver rotated remember cookie")
	}
	user, cas := match.user, match.store
	if nilval.Is(cas) {
		return auth.ErrRememberTokenStoreUnsupported
	}

	// The stored hash the presented token matched in matchRememberCookie,
	// as it was read then; the compare-and-swap below anchors on it.
	oldToken := match.hash

	var newToken string
	cookie, err := g.mintRememberCookie(user, func(hashed string) error {
		// The replacement is minted by the swap below: the presented
		// credential's lifetime is decided one last time in front of it,
		// after the record store calls that came before.
		if match.ended() {
			return errRememberCredentialEnded
		}
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
	// commit can never take one without the other; a commit while the
	// recall holds the gate saves nothing and the recall's publish runs
	// the undo instead.
	ctx := context.WithoutCancel(r.Context())
	op.queueCredentialWrite(afterSaveWrite{write: func(w http.ResponseWriter) {
		http.SetCookie(w, cookie)
	}, undo: func() {
		// The store restores the token, on the user value too if it keeps
		// one in step (see auth.RememberTokenCompareAndSwapper): the scheme
		// never writes the user value itself, which would land over
		// whatever replaced or cleared the credential since the swap.
		swapped, err := cas.CompareAndSwapRememberToken(ctx, user, newToken, oldToken)
		if err != nil || !swapped {
			g.logWarn("velocity/auth: remember-cookie revival: session not saved and the remember token could not be restored; the visitor signs in again", "swapped", swapped, "error", err)
		}
	}})
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

// Login signs user in on r's session: it retires the current session,
// regenerates the id, rotates the CSRF token and records the sign-in, and
// with remember-me issues the remember credential once the session is
// saved. Inside the session middleware the seam saves the session;
// outside it Login commits its own save. After the request's session was
// saved (a write queued behind the save calling Login) the sign-in is
// refused with an error.
//
// Login holds the request's authentication gate while it runs (see
// gate.go): a Login while another authentication operation of the request
// is in flight, or from a store the operation calls, returns
// auth.ErrOperationInProgress without waiting, and a response committed
// while Login runs saves nothing, so Login then returns the sign-in
// refused error and the queued credential writes are dropped. The server
// record Login wrote for the new id names an id no client received and
// ends with its TTL. A request that carries no session context (neither the session
// middleware nor WithSessionContext put one on it) is refused with
// auth.ErrNoSessionContext before any side effect: a caller outside the
// middleware wraps the request with WithSessionContext and passes the
// request it returns.
//
// Remember-me needs a user store implementing
// auth.RememberTokenCompareAndSwapper: with any other store a Login asking
// for it returns auth.ErrRememberTokenStoreUnsupported and changes nothing
// (no session change, no credential).
//
// A failed Login may leave side effects, depending on where it fails:
//
//   - The previous session's server record cannot be retired: nothing
//     changed, and the writes an earlier sign-in of the request queued (a
//     remember-me recall's rotated cookie and XSRF-TOKEN) are delivered or
//     undone as before.
//   - Regenerate fails: with a server session store the previous
//     session's record was already retired and stays deleted, so a
//     signed-in previous session is ended; the
//     earlier sign-in's writes are still delivered or undone as before.
//   - The CSRF token rotation fails: the session was already regenerated,
//     so this sign-in supersedes an earlier sign-in's queued writes. After
//     a remember-me recall the rotated remember cookie is then not
//     delivered (the stored token moved, so remember-me on that device
//     needs a new sign-in), and the XSRF-TOKEN the request bootstrapped
//     may name the token of the session before the recall: the next
//     unsafe request can be refused (419) until a safe request writes the
//     cookie again.
func (g *SessionScheme) Login(w http.ResponseWriter, r *http.Request, user contract.Authenticatable, remember ...bool) error {
	// The session middleware saves the session and then writes the
	// cookies bound to it. Outside it, this login is its own save scope
	// and commits the same way before returning.
	//
	// The request is reserved before the user is looked at, so a request
	// the scheme refuses (no session context, an operation in flight) is
	// refused the same way whatever user it was handed.
	var op gateOp
	holder, standalone, err := reserveOperation(r, &op)
	if err != nil {
		return errchain.Errorf("velocity/auth: login refused: %w", err)
	}
	defer op.abort()
	// Guard the nil user before any session work. user is deref'd below
	// (session.Put(auth.UserIDSessionKey, ...)), so a nil here would
	// panic. Return a normal error instead of panicking on a runtime
	// condition.
	if nilval.Is(user) {
		op.publish(false)
		return auth.ErrUserNotFound
	}
	return g.signInReserved(w, r, holder, standalone, &op, user, nil, remember...)
}

// signInReserved is the sign-in Login, LoginByID and Attempt share, run
// holding the request's gate for op: it signs user in (loginReserved),
// applies the result, commits it when the operation is its own save scope
// (standalone), runs then (when set) once the sign-in succeeded, and frees
// the gate. The commit and then run under op's reservation, so a store
// they call that asks the scheme about the request is refused.
func (g *SessionScheme) signInReserved(w http.ResponseWriter, r *http.Request, holder *sessionHolder, standalone bool, op *gateOp, user contract.Authenticatable, then func(), remember ...bool) error {
	session, err := g.loginReserved(r, holder, user, op, remember...)
	if err != nil {
		// A sign-in that failed installs no session: after a Logout the
		// request stays ended.
		op.fresh = nil
		op.publish(true)
		return err
	}
	if !op.apply(true) {
		op.release()
		return errSessionSaved
	}
	if standalone {
		// The holder is this sign-in's own: nothing else reaches it.
		holder.setSession(session)
		err = commitStandalone(g, r, w, holder)
	}
	if err == nil && then != nil {
		then()
	}
	op.release()
	return err
}

// loginReserved is Login's body, run holding the request's gate for op. It
// returns the signed-in session, or nil with the error that stopped the
// sign-in.
func (g *SessionScheme) loginReserved(r *http.Request, holder *sessionHolder, user contract.Authenticatable, op *gateOp, remember ...bool) (contract.Session, error) {
	if holder.isSealed() {
		return nil, errSessionSaved
	}
	// Remember-me needs a user store that can consume the credential by
	// compare-and-swap. Without one the sign-in is refused here, before
	// anything changed, instead of issuing a credential the scheme could
	// only ever overwrite.
	// The store checked here is the one the credential is issued through
	// after the save: a SetUserStore in between does not get a credential
	// issued through a store that was never checked.
	wantsRemember := len(remember) > 0 && remember[0]
	var rememberUsers auth.UserStore
	if wantsRemember {
		rememberUsers = g.loadUserStore()
		if _, ok := rememberCapability(rememberUsers); !ok {
			return nil, auth.ErrRememberTokenStoreUnsupported
		}
	}
	// The user's identifier is the session's key to the user: read it
	// before anything changes, so an unreadable one signs nobody in and
	// leaves the session as it was.
	userID, _, err := identity.Of(user)
	if err != nil {
		return nil, err
	}

	session := g.getSession(r)
	switch {
	case session == nil:
		var err error
		session, err = g.store.Create("")
		if err != nil {
			return nil, err
		}
		// Cache in request context if available; a session a concurrent
		// load of the request cached first wins.
		if cached, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && cached != nil {
			session = cached.installLoaded(session)
		}
	case holder.isEnded():
		// A Logout of this request ended the holder's session: its id is
		// retired and its record gone, so the sign-in starts from a fresh
		// session, installed on the holder when op publishes.
		var err error
		session, err = g.store.Create("")
		if err != nil {
			return nil, err
		}
		op.fresh = session
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
		return nil, errchain.Errorf("velocity/auth: login aborted: previous session not retired: %w", err)
	}

	// Regenerate session ID for security. A failure here must abort the
	// login: proceeding with the old session ID opens a session-fixation
	// window (an attacker who planted the cookie keeps access).
	op.beginMutation()
	if err := session.Regenerate(); err != nil {
		return nil, errchain.Errorf("velocity/auth: login aborted: session regenerate failed: %w", err)
	}
	// The session is replaced: from here on this sign-in supersedes the
	// credential writes an earlier transition of the request queued (an
	// earlier sign-in's remember cookie).
	op.beginTransition()
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
			return nil, errchain.Errorf("velocity/auth: login aborted: csrf token rotate failed: %w", err)
		}
		op.queueCredentialWrite(afterSaveWrite{write: func(w http.ResponseWriter) {
			rotator.WriteXSRFCookie(rotateCtx, w, sessionID)
		}})
	}

	// Store user ID in session
	session.Put(auth.UserIDSessionKey, userID)

	// Handle remember me as best-effort, after the session save. A
	// failure here (e.g. the users table lacks a remember_token column,
	// the user store cannot persist, or the identifier cannot be
	// encoded) must NOT fail an otherwise-successful login, and must not
	// undo the committed session and CSRF rotation. Log and continue:
	// the user is authenticated for this session, just not recalled
	// across a new one. The credential is issued (its hash stored) while
	// the seam still holds the request's gate after the save, and its
	// cookie written with the other queued writes, so a logout that
	// follows the commit clears it after it was stored.
	if wantsRemember {
		ctx := r.Context()
		var cookie *http.Cookie
		op.queueCredentialWrite(afterSaveWrite{
			settle: func() {
				c, err := g.issueRememberCookie(ctx, rememberUsers, user)
				if err != nil {
					g.logWarn("velocity/auth: remember-me cookie not set; login still succeeded", "error", err)
					return
				}
				cookie = c
			},
			write: func(w http.ResponseWriter) {
				if cookie != nil {
					// A Logout earlier in the request wrote the cookie's
					// deletion: this sign-in's cookie replaces it, so the
					// response carries one line for the name.
					dropCookieDeletions(w.Header(), cookie.Name)
					http.SetCookie(w, cookie)
				}
			},
		})
	}

	// The sign-in record is written before the session is saved, inside
	// the middleware and outside it alike: a session store that keeps the
	// session in that record (session.ServerStore) saves into it and never
	// creates a signed-in record itself. When the save then fails, the
	// record names an id no client received and ends with its TTL.
	g.recordServerSession(r, session, user)
	return session, nil
}

// LoginByID signs in the user the user store finds for id, as Login does.
// It takes the request's authentication gate before it looks the user up:
// while another authentication operation of the request is in flight, or
// from a store that operation calls, it returns
// auth.ErrOperationInProgress without calling the store.
//
// A request that carries no session context (neither the session
// middleware nor WithSessionContext put one on it) is refused with
// auth.ErrNoSessionContext before any side effect: a caller outside the
// middleware wraps the request with WithSessionContext and passes the
// request it returns.
func (g *SessionScheme) LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
	var op gateOp
	holder, standalone, err := reserveOperation(r, &op)
	if err != nil {
		return errchain.Errorf("velocity/auth: login refused: %w", err)
	}
	defer op.abort()
	user, err := g.loadUserStore().FindByIDCtx(r.Context(), id)
	if err != nil {
		op.publish(false)
		return err
	}
	// FindByID may return (nil, nil) for an unknown id. Surface that as an
	// error here so we never sign in a nil user (the user_id deref would
	// panic).
	if user == nil {
		op.publish(false)
		return auth.ErrUserNotFound
	}
	return g.signInReserved(w, r, holder, standalone, &op, user, nil, remember...)
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
//
// Attempt takes the request's authentication gate before anything else
// and holds it through the credential check, the sign-in and the
// throttle's success record: while another authentication operation of
// the request is in flight, or from a store that operation calls, it
// returns false and auth.ErrOperationInProgress at once, with no throttle,
// user store or password work. Reads of the signed-in user on the request
// fail closed while the attempt runs, its timed floor included.
//
// A request that carries no session context (neither the session
// middleware nor WithSessionContext put one on it) is refused with
// auth.ErrNoSessionContext before any side effect: a caller outside the
// middleware wraps the request with WithSessionContext and passes the
// request it returns.
func (g *SessionScheme) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	var op gateOp
	holder, standalone, err := reserveOperation(r, &op)
	if err != nil {
		return false, errchain.Errorf("velocity/auth: attempt refused: %w", err)
	}
	defer op.abort()

	// Snapshot throttler, user store, and hasher once so the credential
	// check and the success tail below see consistent references even if
	// a concurrent Set* call swaps one mid-call.
	throttler := g.loadThrottler()
	hasher := g.effectiveHasher()
	user, keys, ok, err := attemptCredentials(r, credentials, g.loadUserStore(), hasher, throttler, g.effectiveAttemptFloor(), g.getTrustedProxies(), &g.loginAdmitter, g.getLoginChallenge())
	if !ok {
		op.publish(false)
		return false, err
	}

	// Sign the user in (post-timebox; the success path's residual delay
	// is the login pipeline itself, which is the same on every successful
	// auth so timing here is not a privacy concern). The throttle's
	// success record runs under the reservation, as its checks did.
	if err := g.signInReserved(w, r, holder, standalone, &op, user, func() {
		recordAttemptSuccess(r, keys, throttler, &g.loginAdmitter)
	}, remember...); err != nil {
		return false, err
	}

	// Hash-staleness check (M-08): when the stored hash no longer
	// matches the configured Hasher parameters (e.g. operator bumped
	// BcryptCost from 10 to 14), emit a PasswordNeedsRehashEvent so
	// listeners can re-hash on the next login. The event carries the
	// user identifier only; the plaintext stays inside this stack
	// frame and is not surfaced to subscribers. It is emitted once the
	// gate is free, so a listener may read the signed-in user.
	maybeEmitRehashEvent(r.Context(), &g.events, hasher, user, "session")
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

// Logout logs out the user.
//
// Logout holds the request's authentication gate (see gate.go) from start
// to end: a Logout while another authentication operation of the request
// is in flight, or from a store that operation calls, returns
// auth.ErrOperationInProgress without waiting and changes nothing, and a
// store the Logout calls, the server-side teardown after the session is
// invalidated included (the cookie store's revocation, the server record
// deletes, a standalone Logout's save), gets the same answer when it asks
// the scheme about the request. A request that carries no session context (neither the session
// middleware nor WithSessionContext put one on it) is refused with
// auth.ErrNoSessionContext before any side effect: a caller outside the
// middleware wraps the request with WithSessionContext and passes the
// request it returns.
//
// A Logout that returns an error wrapping auth.ErrNoSessionContext or
// auth.ErrOperationInProgress was refused and ended nothing: the session,
// its server record and the remember credential are as they were, and the
// visitor is still signed in. Check the error.
func (g *SessionScheme) Logout(w http.ResponseWriter, r *http.Request) error {
	// The session middleware writes the delete cookie for the
	// invalidated session. Outside it, this logout is its own save scope
	// and commits the same way below.
	var op gateOp
	holder, standalone, err := reserveOperation(r, &op)
	if err != nil {
		return errchain.Errorf("velocity/auth: logout refused: %w", err)
	}
	defer op.abort()
	session := g.getSession(r)
	if session == nil {
		return nil
	}
	// Capture the session ID before Invalidate so we can also tear down
	// the server-side record. BaseSession.Invalidate currently leaves id
	// intact, but capturing here is robust against future changes.
	sessionID := session.ID()

	// retired lists the ids the server-side teardown below ends: the
	// session's, and after the request's save the id that save issued
	// too, which is the one the browser holds whatever the session object
	// reports by now.
	retired := []string{sessionID}
	if holder.isSealed() {
		// The request's session was saved already (a write queued behind
		// the save calling Logout): the server-side teardown below still
		// ends the session and the remember credential, but no save
		// follows to delete the session cookie on this response.
		g.logWarn("velocity/auth: logout after the session was saved: the session is ended server-side, its cookie is not deleted by this response", "session", sessionref.Of(sessionID))
		if committed := holder.committed(); committed != "" && committed != sessionID {
			retired = append(retired, committed)
		}
	}

	// Revoke the CSRF token for this session BEFORE Invalidate clears
	// the session bag (H-02). Without this, a token kept apart from the
	// session would survive in the CSRF store and a
	// captured cookie+token pair would remain valid against the now-
	// logged-out session id. A revoke failure is logged and swallowed:
	// logout must not refuse to clear the cookie because a downstream
	// store is unavailable.
	if rotator := g.getCSRFTokenRotator(); rotator != nil {
		if sessionID != "" {
			op.beginMutation()
			if err := rotator.RevokeToken(sessionContext(r, session), sessionID); err != nil {
				g.logWarn("velocity/auth: csrf token revoke (logout) failed", "session", sessionref.Of(sessionID), "error", err)
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
		case err != nil && !errchain.Is(err, auth.ErrUserNotFound):
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
	op.beginMutation()
	invalidateErr := session.Invalidate()
	// The session is ended: this logout supersedes the credential writes
	// an earlier transition of the request queued, so a remember-me
	// sign-in earlier in the request never issues its credential after
	// the logout cleared it. The session is whole again (ended), so the
	// transition is applied; the teardown below, and a standalone
	// logout's commit on the holder that is this logout's own, still run
	// under the reservation, which is freed once they are done.
	op.beginTransition()
	op.endsSession = true
	op.apply(false)

	// The session middleware saves the invalidated session, which
	// writes the delete cookie because the session is now marked
	// destroyed. A standalone logout commits it here; a failure also
	// continues to the revocation + server-store delete path, since
	// without revoke the still-decrypting captured cookie would
	// re-authenticate against a live server-side record.
	var saveErr error
	if standalone {
		holder.setSession(session)
		saveErr = commitStandalone(g, r, w, holder)
	}

	// Revoke in the underlying SessionStore when it supports the
	// revocation capability (CookieStore). The cookie value still
	// decrypts post-logout, so without this call a captured cookie
	// remains valid until its IssuedAt window elapses. Best-effort:
	// the store may not implement the interface (other drivers,
	// future stores), and Revoke has no failure mode.
	for _, id := range retired {
		if id == "" {
			continue
		}
		if rev, ok := g.store.(sessionRevoker); ok {
			rev.Revoke(id)
		}
		if store := g.getServerStore(); store != nil {
			if err := store.Delete(r.Context(), id); err != nil { //store-rmw-ok: logout retires the id for good: no request writes a record under a retired id again, so nothing newer can be removed
				g.logWarn("velocity/auth: server session store delete (logout) failed", "session", sessionref.Of(id), "error", err)
			}
		}
	}
	op.release()

	// Surface the earliest hard error: invalidate first (the
	// upstream entropy failure callers most care about), then save.
	// Server-side teardown above is best-effort with its own logging.
	// The invalidate failure is returned, not logged too, so it is
	// reported once.
	if invalidateErr != nil {
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
	if nilval.Is(userStore) {
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
//
// Once the session middleware saved the request's session, the session is
// sealed: its Regenerate returns auth.ErrSessionSealed, since the cookie
// the save delivered names its id (see QueueAfterSessionSave).
func (g *SessionScheme) Session(r *http.Request) contract.Session {
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
//
// It runs under the request's authentication reservation, as a read of the
// user does (see gate.go): while an operation that may change the session
// is in flight, or from the goroutine of the read in progress, it returns
// auth.ErrOperationInProgress; on a request an operation was torn on it
// returns auth.ErrSessionNotFound.
func (g *SessionScheme) ResolveSession(r *http.Request) (contract.Session, error) {
	holder, _ := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if holder == nil {
		return g.resolveSessionReserved(r)
	}
	var op gateOp
	if _, err := holder.readTurn(r, &op, false); err != nil {
		return nil, err
	}
	return g.resolveSessionTurn(r, holder, &op)
}

// resolveSessionTurn is ResolveSession's turn: its body, run holding the
// request's gate for op as the request's resolver. A read that meets it
// from its own goroutine is refused by its frame (see onResolvePath).
func (g *SessionScheme) resolveSessionTurn(r *http.Request, holder *sessionHolder, op *gateOp) (contract.Session, error) {
	defer op.abort()
	if holder.isTorn() {
		op.publish(false)
		return nil, auth.ErrSessionNotFound
	}
	sess, err := g.resolveSessionReserved(r)
	op.publish(false)
	return sess, err
}

// resolveSessionReserved is ResolveSession's body, run holding the
// request's gate when the request has a holder.
func (g *SessionScheme) resolveSessionReserved(r *http.Request) (contract.Session, error) {
	sess := sessionFromHolder(r)
	if sess == nil || sess.ID() == "" {
		sess = g.getSession(r)
	}
	if sess == nil || sess.ID() == "" {
		return nil, auth.ErrSessionNotFound
	}
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); !ok || holder == nil || !holder.inSaveScope() {
		// No save scope: nothing of the framework saves this session, so
		// the mark is not cleared by a save the request's commit makes,
		// and it says what it said at the load (fresh) or after the last
		// change. Without a holder the session is this call's own.
		fresh, ok := sess.(interface{ IsModified() bool })
		if !ok || fresh.IsModified() { //session-mark-ok: judges a session no save scope saves as fresh or changed; no framework save is in flight on it
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
func (g *SessionScheme) getSession(r *http.Request) contract.Session {
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

	// Cache in request context if available; a concurrent first load of
	// the request that cached its session first wins.
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
		return holder.installLoaded(session)
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
// activityRefreshInterval.
//
// Returns nil when no store is configured (cookie-only mode preserved).
func (g *SessionScheme) consultServerStore(r *http.Request, session contract.Session) error {
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
		// The reap is conditional on the record the store holds when it
		// lands: a record put under the id since this read is kept.
		_, _ = store.DeleteIf(r.Context(), sessionID, func(meta *auth.SessionMeta) bool {
			return g.pastAbsoluteCapAt(meta.CreatedAt)
		})
		rec, err = nil, auth.ErrSessionExpired
	}
	if err != nil {
		var resolved error
		if errchain.Is(err, auth.ErrSessionExpired) {
			resolved = auth.ErrSessionExpired
		} else if errchain.Is(err, auth.ErrSessionNotFound) {
			resolved = auth.ErrSessionRevoked
		} else {
			g.logWarn("velocity/auth: server session store get failed", "session", sessionref.Of(sessionID), "error", err)
			resolved = errchain.Errorf("velocity/auth: server session store get: %w", err)
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
	if now.Sub(rec.LastSeenAt) < g.activityRefreshInterval() {
		return nil
	}
	err := store.Touch(ctx, rec.ID, now, g.recordExpiry(rec.CreatedAt, now))
	if err == nil {
		return nil
	}
	if errchain.Is(err, auth.ErrSessionExpired) {
		return auth.ErrSessionExpired
	}
	if errchain.Is(err, auth.ErrSessionNotFound) {
		return auth.ErrSessionRevoked
	}
	g.logWarn("velocity/auth: server session store touch (lastseen) failed", "session", sessionref.Of(rec.ID), "error", err)
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
	return rec != nil && g.pastAbsoluteCapAt(rec.CreatedAt)
}

// pastAbsoluteCapAt reports whether a session created at createdAt is older
// than the absolute lifetime.
func (g *SessionScheme) pastAbsoluteCapAt(createdAt time.Time) bool {
	abs := g.config.AbsoluteTimeout()
	return abs > 0 && !createdAt.IsZero() && sessionclock.Now().After(createdAt.Add(abs))
}

// recordServerSession writes the freshly-issued session to the server-side
// store on Login. Failures are logged and swallowed so a transient store
// outage does not break login (the user is authenticated for this request
// and subsequent reads will fail-closed).
//
// session.Regenerate() inside Login already produced a fresh id, so this
// writes a brand-new record; Login removed the previous id's record before
// the regenerate (retireServerRecord).
func (g *SessionScheme) recordServerSession(r *http.Request, session contract.Session, user contract.Authenticatable) {
	store := g.getServerStore()
	if store == nil {
		return
	}
	sessionID := session.ID()
	if sessionID == "" {
		g.logWarn("velocity/auth: server session store skipped (empty session id)")
		return
	}
	_, userID, err := identity.Of(user)
	if err != nil {
		g.logWarn("velocity/auth: server session store skipped", "error", err)
		return
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
		g.logWarn("velocity/auth: server session store put (login) failed", "session", sessionref.Of(sessionID), "error", err)
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
	if err := store.Delete(r.Context(), id); err != nil && !errchain.Is(err, auth.ErrSessionNotFound) { //store-rmw-ok: the sign-in retires the old id for good: the session moves to a fresh id, so no record is written under the old one again
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
	if errchain.Is(err, auth.ErrUserNotFound) || (err == nil && user == nil) {
		return nil
	}
	if err != nil {
		return errchain.Errorf("velocity/auth: remember token not cleared: user lookup failed: %w", err)
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

// checkRememberCookie reports the user a valid remember cookie names, or
// nil; see matchRememberCookie.
func (g *SessionScheme) checkRememberCookie(r *http.Request) contract.Authenticatable {
	match, ok := g.matchRememberCookie(r)
	if !ok {
		return nil
	}
	return match.user
}

// dropUnsupportedRememberCookie applies the policy for a remember cookie
// presented to a scheme whose user store cannot consume the credential by
// compare-and-swap: the cookie is deleted on the response (once per
// request) and the scheme says so once. It does nothing when the store has
// the capability, which it checks first, so a scheme with remember-me pays
// no cookie lookup here.
func (g *SessionScheme) dropUnsupportedRememberCookie(r *http.Request) {
	if _, ok := g.rememberStore(); ok {
		return
	}
	if _, err := r.Cookie("remember_" + g.config.Name); err != nil {
		return
	}
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
		if w := holder.getResponseWriter(); w != nil {
			dropCookieDeletions(w.Header(), "remember_"+g.config.Name)
			g.clearRememberCookie(w)
		}
	}
	if g.rememberUnsupportedLogged.CompareAndSwap(false, true) {
		g.logWarn("velocity/auth: a remember cookie was presented, but the user store does not implement RememberTokenCompareAndSwapper; the cookie is deleted and ignored (logged once)")
	}
}

// matchRememberCookie validates the request's remember cookie and returns
// the match (the user, the stored hash the token matched and the store it
// was read from); ok is false when there is no valid cookie. On a scheme
// without remember-me (a user store that lacks
// auth.RememberTokenCompareAndSwapper) every cookie is invalid: it is
// deleted on the response and the miss is logged once per scheme.
//
// Validation only: rotate-on-use (V2-08) happens in anchorRecalledUser,
// which calls rotateRememberToken once the revival fully anchors, so a
// recall that fails fixation/store checks does not burn the token.
func (g *SessionScheme) matchRememberCookie(r *http.Request) (rememberMatch, bool) {
	cookie, err := r.Cookie("remember_" + g.config.Name)
	if err != nil {
		return rememberMatch{}, false
	}

	// A scheme whose user store cannot consume the credential by
	// compare-and-swap issues none (auth.ErrRememberTokenStoreUnsupported),
	// so the cookie is not one of its own: it is deleted on the response
	// and ignored, whatever it holds, and the user store is not asked.
	// One snapshot of the store serves the capability and the lookup.
	userStore := g.loadUserStore()
	cas, ok := rememberCapability(userStore)
	if !ok {
		g.dropUnsupportedRememberCookie(r)
		return rememberMatch{}, false
	}

	// Decrypt cookie value
	if g.encryptor == nil {
		return rememberMatch{}, false
	}
	decrypted, err := g.encryptor.Decrypt(cookie.Value)
	if err != nil {
		return rememberMatch{}, false
	}

	// The payload is userID|issuedAt|token (see mintRememberCookie). The
	// credential ends RememberTimeout after it was issued, on the server:
	// the cookie's Max-Age only tells the browser when to drop it, and a
	// captured copy replayed by hand must not outlive it.
	userID, issuedAt, token, ok := parseRememberPayload(decrypted)
	if !ok {
		return rememberMatch{}, false
	}
	match := rememberMatch{store: cas, expiresAt: issuedAt.Add(g.config.RememberTimeout())}
	if match.ended() {
		return rememberMatch{}, false
	}

	// Look up user by ID
	user, err := userStore.FindByIDCtx(r.Context(), userID)
	if err != nil || user == nil {
		return rememberMatch{}, false
	}
	// The lookup is user code and may have taken longer than the
	// credential had left.
	if match.ended() {
		return rememberMatch{}, false
	}

	// Verify remember token with constant-time comparison.
	// We hash the incoming token with SHA-256 and compare against the stored
	// hash. Legacy rows that still hold a raw token continue to work because
	// we fall through to a direct compare. The stored token is read once:
	// the value compared here is the one the match carries.
	storedToken := user.GetRememberToken()
	if storedToken == "" {
		return rememberMatch{}, false
	}
	if crypto.EqualString(storedToken, hashRememberToken(token)) || crypto.EqualString(storedToken, token) {
		match.user, match.hash = user, storedToken
		return match, true
	}
	return rememberMatch{}, false
}

// issueRememberCookie issues the remember-me credential at login: it
// persists the new token hash unconditionally through the user store
// (there is no prior credential to guard against; login may always
// overwrite) and returns the cookie for the caller to write. userStore is
// the store loginReserved checked for the compare-and-swap capability
// (auth.ErrRememberTokenStoreUnsupported otherwise), captured once for the
// sign-in. ctx is the
// request context so a client disconnect aborts the user store write.
func (g *SessionScheme) issueRememberCookie(ctx context.Context, userStore auth.UserStore, user contract.Authenticatable) (*http.Cookie, error) {
	return g.mintRememberCookie(user, func(hashed string) error {
		return userStore.UpdateRememberTokenCtx(ctx, user, hashed)
	})
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
func (g *SessionScheme) mintRememberCookie(user contract.Authenticatable, persist func(hashed string) error) (*http.Cookie, error) {
	_, userID, err := identity.Of(user)
	if err != nil {
		return nil, err
	}
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
	value := rememberPayload(userID, issuedAt, token)

	// Encrypt value. The encryptor authenticates the payload, so the
	// issue time cannot be altered.
	if g.encryptor == nil {
		return nil, errors.New("velocity/auth: encryptor not configured, cannot set remember cookie")
	}
	encrypted, err := g.encryptor.Encrypt(value)
	if err != nil {
		return nil, err
	}

	// Store only the hash of the token on the user record. The scheme
	// writes the token through the store alone and never sets it on the
	// user value: a store that hands every request one shared user value
	// may have swapped or cleared the credential again by the time an
	// unconditional set here would land, and that set would write over it.
	// The store keeps the user value it handed out in step with what it
	// persisted, inside its own write (auth.RememberTokenCompareAndSwapper).
	hashed := hashRememberToken(token)
	if err := persist(hashed); err != nil {
		return nil, err
	}

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
		return "", errchain.Errorf("velocity/auth: failed to generate remember token: %w", err)
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
