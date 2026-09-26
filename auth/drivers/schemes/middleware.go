package schemes

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/sessionclock"
	"github.com/velocitykode/velocity/router"
)

// preCommitHooker is the optional capability the save-at-end middleware
// uses to register a pre-commit hook on the response writer. The
// router's response writer implements it, and the router fires a hook
// nothing fired once its error boundary is done with the request; any
// other writer (test recorders, custom wrappers) is wrapped in a
// preCommitWriter for the handler's run, which fires the save the same
// way.
type preCommitHooker interface {
	BeforeFirstWrite(fn func())
}

// SessionMiddleware returns a router.MiddlewareFunc that gives every
// request save-at-end session semantics: the request is given a sessionHolder
// (via WithSessionContext) so SessionScheme.getSession can cache the
// resolved session for the lifetime of the request; BEFORE the response
// headers are committed, the holder is consulted and, if a session was
// touched and mutated, it is saved to the response writer.
//
// Where the save happens:
//
//   - Pre-commit hook, on the router's response writer, which exposes
//     BeforeFirstWrite. The hook fires once before the first WriteHeader
//     or Write call, so Set-Cookie lands in the same response the
//     handler is about to flush (c.JSON / c.Text / c.Redirect / direct
//     writes commit headers from inside the handler body). The hook stays
//     armed after the handler returns: when the handler returned an
//     error, the router's error boundary writes the error response later,
//     and its first write fires the hook, so session changes the error
//     path makes (a flash the error page drains, a render rule's Put, the
//     intended-URL stash) are saved with it. When neither the handler nor
//     the error path writes anything, the router fires the hook once the
//     boundary is done, before net/http sends its implicit 200.
//
//   - Any other response writer (httptest.ResponseRecorder, custom
//     wrappers) is wrapped for the handler's run so its first committing
//     write fires the same save; when the handler writes nothing, the
//     save runs as the handler returns.
//
// The save is the one place the framework persists a session: Login,
// Logout, the intended-URL stash and resolver, and view flashes only
// mutate the request's session, so a request emits at most one session
// Set-Cookie. After a successful save the seam writes the cookies Login
// bound to the saved session id (XSRF-TOKEN, then remember), so a client
// never holds either for a session that was not persisted. Login and
// Logout called outside this middleware (a plain net/http handler, a
// script, a test) are their own save scope and commit through the same
// seam body before returning: one save per operation, since outside the
// middleware the scheme sees no response boundary to wait for. Composing
// several scheme operations on one response needs the middleware.
//
// Mounted more than once on a request (nested), the outermost instance
// owns the save; the inner ones pass straight through.
//
// velocity.New installs it on the app router whenever the default scheme
// is a *SessionScheme (see SessionMiddlewareFor); consumers do not
// normally need to wire this themselves.
func (g *SessionScheme) SessionMiddleware() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			return g.serveWithSession(c, next)
		}
	}
}

// SessionMiddlewareFor returns a middleware that runs each request through
// the SessionMiddleware of the scheme current returns at request time, or
// straight to the handler when current returns nil. It lets the
// application install the save seam once, when the router is built, while
// the scheme it serves follows later changes to the default scheme.
func SessionMiddlewareFor(current func() *SessionScheme) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			g := current()
			if g == nil {
				return next(c)
			}
			return g.serveWithSession(c, next)
		}
	}
}

// serveWithSession runs next inside the save seam; see SessionMiddleware.
//
// The request, not the middleware, owns the commit: the first session
// middleware to see the request binds its holder and commits it once, and
// a session middleware nested inside it (the app's and one a group mounts
// again) runs its handler straight through, so a request is saved once
// whichever middleware or writer is involved.
func (g *SessionScheme) serveWithSession(c *router.Context, next router.HandlerFunc) error {
	// A holder with a response writer is one an enclosing session
	// middleware already bound and commits.
	if holder, ok := c.Request.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil && holder.getResponseWriter() != nil {
		return next(c)
	}

	// Replace the request with one carrying a sessionHolder so any
	// scheme call inside the handler caches its session lookup AND so
	// the seam can recover the session when it commits. A holder
	// WithSessionContext attached on its own (no middleware) is kept,
	// so writes made through it before this point are saved.
	r := c.Request
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		r = WithSessionContext(r)
		c.Request = r
		holder = r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	}

	// Hand the response writer to the holder so scheme read paths
	// (User/Check) can emit Set-Cookie during remember-cookie
	// revival: rotate-on-use needs to deliver the replacement
	// remember cookie, and those methods only see the request. It also
	// marks the holder as bound by this middleware.
	w := c.Response
	holder.setResponseWriter(w)
	holder.markSaveScope()

	// Eagerly bind a session to the request so anonymous-but-
	// stateful concerns (CSRF token mint, flash bag, anything
	// that wants a stable per-visitor id) have something to bind
	// to before the handler runs. Without this the lazy
	// SessionScheme.getSession path means a handler that never
	// touches the session (a plain Inertia page render, a static
	// dashboard, the login form GET) leaves the response with no
	// Set-Cookie, so the next POST arrives with no session id and
	// CSRF middleware has nothing to validate against (419 on the
	// first state-changing request).
	//
	// Order: load existing session first; only Create on miss so
	// we never overwrite a returning visitor's id. The created
	// session is marked modified so the commit writes the cookie
	// even when the handler never touched the bag.
	ensureSession(g, c.Request)

	// The holder's commit runs at most once per request, so the
	// session never writes two Set-Cookie headers whichever site
	// fires it.
	doSave := func() {
		holder.commitOnce.Do(func() {
			_ = commitSession(g, c.Request, w, holder)
		})
	}

	// Pre-commit hook: fires once just before the first
	// WriteHeader/Write/Flush/Hijack commits the response, whether the
	// handler or the router's error boundary writes it, and otherwise
	// once the boundary is done with the request. A writer without the
	// hook is wrapped for the handler's run so its first write fires the
	// commit the same way.
	if h, hooked := w.(preCommitHooker); hooked {
		h.BeforeFirstWrite(doSave)
		return next(c)
	}
	c.Response = &preCommitWriter{ResponseWriter: w, beforeCommit: doSave}
	err := next(c)
	c.Response = w
	// Nothing was written: commit now, before anything outside this
	// middleware writes the response.
	doSave()
	return err
}

// preCommitWriter gives a response writer without the router's pre-commit
// hook one: beforeCommit runs once, just before the first write that
// commits the response headers (a final WriteHeader, Write, Flush or
// Hijack), so the session cookie lands in the response the handler
// writes.
type preCommitWriter struct {
	http.ResponseWriter
	beforeCommit func()
	committed    bool
}

func (p *preCommitWriter) commit() {
	if !p.committed {
		p.committed = true
		p.beforeCommit()
	}
}

// WriteHeader commits the response, except for an informational status
// (1xx other than 101), which goes out ahead of the final one.
func (p *preCommitWriter) WriteHeader(statusCode int) {
	if statusCode < 100 || statusCode > 199 || statusCode == http.StatusSwitchingProtocols {
		p.commit()
	}
	p.ResponseWriter.WriteHeader(statusCode)
}

func (p *preCommitWriter) Write(b []byte) (int, error) {
	p.commit()
	return p.ResponseWriter.Write(b)
}

// Flush commits the response and flushes it when the writer can.
func (p *preCommitWriter) Flush() {
	p.commit()
	if f, ok := p.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack commits the session before handing the connection over, when the
// writer can hijack.
func (p *preCommitWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := p.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	p.commit()
	return h.Hijack()
}

// Committed reports whether the response is committed.
func (p *preCommitWriter) Committed() bool {
	if cr, ok := p.ResponseWriter.(contract.CommitReporter); ok {
		return cr.Committed()
	}
	return p.committed
}

// Unwrap returns the wrapped writer (for http.ResponseController).
func (p *preCommitWriter) Unwrap() http.ResponseWriter {
	return p.ResponseWriter
}

// commitSession is the save seam's body and the only framework code that
// saves a session: it renews the session on activity (renewOnActivity),
// saves holder's session to w when it changed, then runs the cookie writes
// queued behind the save. A failed save drops the queued writes, which
// are bound to the session id the save did not persist, runs the queued
// undo steps instead, and is returned.
//
// The save runs under the holder's lifecycle lock held exclusively, so it
// waits for an authentication transition in flight (a recall between
// writing the user and swapping the remember token) and never saves or
// takes the queue halfway through one. Only the writes of the latest
// transition run; a superseded one's (a remember-me sign-in the request
// then logged out of) are dropped (see sessionHolder.takeAfterSave).
// Still under the lock, the commit seals the request and its session (a
// later sign-in or recall is refused, and the session's id can no longer
// be regenerated, since nothing it changed would be saved), records the
// id the save issues, and settles the queued credentials (a sign-in's
// remember token reaches the user store), then releases the lock and
// delivers the queued writes (see deliverAfterSave). A queued write may
// therefore read the signed-in user of the request, and a logout it runs
// comes after every credential the save issued and ends the session the
// save issued.
//
// A response that already deletes the session cookie (Context.DeleteCookie)
// ends the session: it is invalidated and saved destroyed, which removes a
// server record and sends the one deletion. A session saved destroyed (so
// ended, or by Logout) runs none of the queued writes, which are bound to
// the ended session; its save does not persist anything they could name.
// Renewal can never issue the cookie again after the handler deleted it. A
// deletion of the session cookie a queued write adds ends the session the
// same way once the delivery is over, also when a later queued write
// panics (see finishDelivery).
func commitSession(g *SessionScheme, r *http.Request, w http.ResponseWriter, holder *sessionHolder) error {
	holder.lifecycle.Lock()
	if s, ok := holder.getSession().(sealableSession); ok {
		s.Seal()
	}
	writes, err := commitSessionHeld(g, r, w, holder)
	holder.lifecycle.Unlock()
	if err != nil {
		holder.closeQueue()
		return err
	}
	defer func() {
		holder.lifecycle.Lock()
		defer holder.lifecycle.Unlock()
		finishDelivery(g, r, w, holder)
	}()
	deliverAfterSave(g, w, holder, writes)
	return nil
}

// commitStandalone is commitSession for a Login or Logout outside the
// session middleware, which holds the holder's lifecycle lock exclusively
// and commits its own save scope. Its holder is the operation's own, so
// nothing outside the operation queues on it or reads through it, and the
// queued writes run while the lock is still held. The session is not
// sealed: outside the middleware each operation is its own save, and a
// later one on the same request saves again.
func commitStandalone(g *SessionScheme, r *http.Request, w http.ResponseWriter, holder *sessionHolder) error {
	writes, err := commitSessionHeld(g, r, w, holder)
	if err != nil {
		holder.closeQueue()
		return err
	}
	defer finishDelivery(g, r, w, holder)
	deliverAfterSave(g, w, holder, writes)
	return nil
}

// finishDelivery ends the delivery of the writes queued behind a
// successful save, however it ended: it closes the holder's queue and
// drops what is left in it, then ends the session the commit issued when
// the delivery added a deletion of the session cookie (see
// endSessionDeletedAfterSave). The commit defers it before the first
// queued write runs, so a write that panics still leaves the queue closed
// (a write queued afterwards is refused, not accepted for a delivery that
// never comes) and a deletion an earlier write added still ends the
// session: the router answers the panic with its error response, which
// carries the response's cookies, deletion included. Nothing is saved
// again, and a panic goes on to the router once it returns. The caller
// holds the holder's lifecycle lock exclusively.
func finishDelivery(g *SessionScheme, r *http.Request, w http.ResponseWriter, holder *sessionHolder) {
	holder.closeQueue()
	g.endSessionDeletedAfterSave(r, w, holder)
}

// sealableSession is the capability commitSession seals a session through:
// once sealed, the session refuses to regenerate its id. *auth.BaseSession,
// and so every framework session, satisfies it.
type sealableSession interface {
	Seal()
}

// commitSessionHeld saves the session and returns the queued writes for
// the caller to deliver once it released the holder's lifecycle lock,
// which it holds exclusively.
func commitSessionHeld(g *SessionScheme, r *http.Request, w http.ResponseWriter, holder *sessionHolder) ([]func(http.ResponseWriter), error) {
	holder.seal()
	session := holder.getSession()
	if session == nil {
		holder.takeAfterSave(true)
		return nil, nil
	}
	ended := g.endSessionDeletedBy(r, w, session)
	if ms, ok := session.(modifiedSession); ok && ms.IsDestroyed() {
		ended = true
	}
	queued := holder.takeAfterSave(ended)
	g.renewOnActivity(r, session)
	if !ended {
		holder.setCommittedID(session.ID())
	}
	// Skip the save when no mutation occurred. The modifiedSession
	// capability covers *auth.BaseSession and the cookie store's
	// wrapper; sessions that do not expose the capability fall through
	// to an unconditional Save (cheaper than reflection, and
	// CookieStore.Save itself short-circuits on !IsModified() too).
	if ms, ok := session.(modifiedSession); !ok || ms.IsModified() || ms.IsDestroyed() {
		if err := saveSessionFromMiddleware(g, w, session); err != nil {
			if len(queued.writes) > 0 {
				g.logWarn("velocity/auth: save-at-end middleware: cookies bound to the unsaved session dropped", "session_id", session.ID(), "count", len(queued.writes))
			}
			for _, fn := range queued.undo {
				fn()
			}
			return nil, err
		}
	}
	for _, fn := range queued.settle {
		fn()
	}
	return queued.writes, nil
}

// deliverAfterSave runs the writes queued behind a successful save, then
// the writes they queue in turn, and closes the holder's queue once none
// is left, so a write queued after that is refused at registration (see
// QueueAfterSessionSave). Each write gets an afterSaveWriter over w's
// headers, never w: the response is being committed, and a body write or
// status from a queued write would commit it from inside the commit.
func deliverAfterSave(g *SessionScheme, w http.ResponseWriter, holder *sessionHolder, writes []func(http.ResponseWriter)) {
	sink := &afterSaveWriter{header: w.Header(), g: g}
	for {
		for _, fn := range writes {
			fn(sink)
		}
		writes = holder.takeDeliveredLate()
		if writes == nil {
			return
		}
	}
}

// errAfterSaveWrite is what a write queued behind the session save gets
// from a body write: it writes headers only.
var errAfterSaveWrite = errors.New("velocity/auth: a write queued behind the session save writes response headers only; the body was not written")

// afterSaveWriter is the writer a write queued behind the session save
// gets: the response's header map, so cookies and headers it sets are sent
// with the response, and no body or status. Write returns
// errAfterSaveWrite and WriteHeader is ignored, both logged; the response
// is committed by the write that fired the save, with the handler's status
// and body.
type afterSaveWriter struct {
	header http.Header
	g      *SessionScheme
}

// Header returns the response's header map.
func (a *afterSaveWriter) Header() http.Header {
	return a.header
}

// Write writes nothing and returns errAfterSaveWrite.
func (a *afterSaveWriter) Write(b []byte) (int, error) {
	a.g.logWarn("velocity/auth: a write queued behind the session save wrote a response body; ignored", "bytes", len(b))
	return 0, errAfterSaveWrite
}

// WriteHeader ignores statusCode.
func (a *afterSaveWriter) WriteHeader(statusCode int) {
	a.g.logWarn("velocity/auth: a write queued behind the session save set a response status; ignored", "status", statusCode)
}

// endSessionDeletedAfterSave ends the session the commit issued when the
// delivery of the queued writes added a deletion of the session cookie to
// w (Context.DeleteCookie in a queued write): as with a deletion the
// handler made (endSessionDeletedBy), the session is invalidated, the
// issued id is revoked in the cookie store of this process and its record
// removed from the scheme's server session store (a failed removal is
// logged), so a captured copy of the cookie is refused on every instance
// sharing that store. The response keeps one session cookie line: the
// session cookie the save issued and the deletions are replaced by one
// deletion built by the session's cookie policy. The caller holds the
// holder's lifecycle lock exclusively.
func (g *SessionScheme) endSessionDeletedAfterSave(r *http.Request, w http.ResponseWriter, holder *sessionHolder) {
	id := holder.committed()
	if id == "" {
		return
	}
	header := w.Header()
	lines := header.Values("Set-Cookie")
	kept := lines[:0:0]
	deleted := false
	prefix := g.config.Name + "="
	for _, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			kept = append(kept, line)
			continue
		}
		if c, err := http.ParseSetCookie(line); err == nil && c.MaxAge < 0 {
			deleted = true
		}
	}
	if !deleted {
		return
	}
	holder.setCommittedID("")
	header.Del("Set-Cookie")
	for _, line := range kept {
		header.Add("Set-Cookie", line)
	}
	http.SetCookie(w, g.config.CookiePolicy().Cookie(g.config.Name, "", -1, g.config.HttpOnly))
	if session := holder.getSession(); session != nil {
		if ms, ok := session.(modifiedSession); !ok || !ms.IsDestroyed() {
			if err := session.Invalidate(); err != nil {
				g.logWarn("velocity/auth: session invalidate (session cookie deleted) failed", "session_id", id, "error", err)
			}
		}
	}
	if rev, ok := g.store.(sessionRevoker); ok {
		rev.Revoke(id)
	}
	if err := g.retireServerRecord(r, id); err != nil {
		g.logWarn("velocity/auth: server session store delete (session cookie deleted) failed", "session_id", id, "error", err)
	}
}

// endSessionDeletedBy invalidates session when w already carries a
// deletion of the session cookie, and reports whether it did. The
// response's own deletion lines for the cookie are removed: the destroyed
// session's save writes the deletion with the store's attributes, whether
// or not its server-side teardown succeeds. As at logout, the ended id is
// revoked in the cookie store of this process and its record is removed
// from the scheme's server session store, so a captured copy of the cookie
// is refused on every instance sharing that store; a failed removal is
// logged.
func (g *SessionScheme) endSessionDeletedBy(r *http.Request, w http.ResponseWriter, session auth.Session) bool {
	if ms, ok := session.(modifiedSession); ok && ms.IsDestroyed() {
		return false
	}
	header := w.Header()
	lines := header.Values("Set-Cookie")
	kept := lines[:0:0]
	deleted := false
	prefix := g.config.Name + "="
	for _, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			kept = append(kept, line)
			continue
		}
		if c, err := http.ParseSetCookie(line); err == nil && c.MaxAge < 0 {
			deleted = true
			continue
		}
		kept = append(kept, line)
	}
	if !deleted {
		return false
	}
	header.Del("Set-Cookie")
	for _, line := range kept {
		header.Add("Set-Cookie", line)
	}
	id := session.ID()
	if err := session.Invalidate(); err != nil {
		g.logWarn("velocity/auth: session invalidate (session cookie deleted) failed", "session_id", id, "error", err)
	}
	if rev, ok := g.store.(sessionRevoker); ok && id != "" {
		rev.Revoke(id)
	}
	if r == nil {
		return true
	}
	if err := g.retireServerRecord(r, id); err != nil {
		g.logWarn("velocity/auth: server session store delete (session cookie deleted) failed", "session_id", id, "error", err)
	}
	return true
}

// renewableSession is the capability renewOnActivity needs from a session:
// when its cookie was issued, and a way to have the seam re-issue it.
// session.CookieSession satisfies it.
type renewableSession interface {
	IssuedAt() time.Time
	MarkModified()
}

// renewOnActivity slides the session's idle window: a request is
// activity, so once the cookie is lastSeenDebounce old the session is
// marked for the seam to re-issue, which restarts its IssuedAt and
// MaxAge (the absolute cap still bounds both). The debounce keeps a busy
// client to one cookie rewrite a minute.
//
// For a signed-in session with a server store, the server record slides
// first: every cookie the seam writes (a renewal or any other change) is
// preceded by a debounced Touch through consultServerStore, so the record
// always outlives the cookie and an idle timeout is seen on the cookie,
// as expiry. When that consult fails (the record was revoked or expired,
// or the store is down) the cookie is not renewed; a save the handler
// asked for still happens.
func (g *SessionScheme) renewOnActivity(r *http.Request, session auth.Session) {
	ms, ok := session.(modifiedSession)
	if !ok || ms.IsDestroyed() {
		return
	}
	rs, ok := session.(renewableSession)
	if !ok {
		return
	}
	issuedAt := rs.IssuedAt()
	due := !issuedAt.IsZero() && sessionclock.Now().Sub(issuedAt) >= lastSeenDebounce
	if !due && !ms.IsModified() {
		return
	}
	if r != nil && session.Get(auth.UserIDSessionKey) != nil && g.getServerStore() != nil {
		if err := g.consultServerStore(r, session); err != nil {
			return
		}
	}
	if due {
		rs.MarkModified()
	}
}

// ensureSession is the eager-bootstrap helper used by SessionMiddleware.
// It triggers the scheme's normal getSession path, which loads an
// existing session from cookie OR mints a fresh one via
// auth.GetSessionFromRequest's store.Create("") fallback. The session
// is cached in the request-scoped sessionHolder so downstream
// getSession callers observe the same instance, and (for freshly
// minted ids) BaseSession sets modified=true so the post-handler
// doSave path writes the Set-Cookie even when the handler never
// touches the bag.
//
// Exists as a package-level var so test fixtures that stub scheme
// internals can override it, mirroring saveSessionFromMiddleware's
// seam.
var ensureSession = func(g *SessionScheme, r *http.Request) {
	_ = g.getSession(r)
}

// saveSessionFromMiddleware is a small indirection so tests can override
// the save path without reaching into router/http internals. It exists
// solely to keep SessionMiddleware ergonomic to unit-test alongside the
// store implementation it drives.
//
// A failed save of a live session writes no session cookie and the
// response goes out without it: the browser keeps the session cookie it
// already holds, and the client never receives what this request changed
// in the session (after a sign-in, the visitor is still signed out). The
// store's write is not atomic, though: a cache-backed server record can
// already hold the changes when the save failed after swapping it (the
// sign-in index update failed), and the client already holds that
// record's id; the queued undo steps reverse only the changes made
// outside the session (see queuedWrites). A destroyed
// session's save writes the cookie deletion whether or not its server-side
// teardown then fails. The failure is logged; an oversize cookie gets its
// own line naming the fix.
var saveSessionFromMiddleware = func(g *SessionScheme, w http.ResponseWriter, s auth.Session) error {
	if err := s.Save(w); err != nil {
		if errors.Is(err, session.ErrCookieTooLarge) {
			g.logWarn("velocity/auth: session not saved: the session cookie would exceed 4096 bytes, so none was sent; keep less in the session or set SESSION_STORE=server", "session_id", s.ID(), "error", err)
			return err
		}
		g.logWarn("velocity/auth: save-at-end middleware: session save failed", "session_id", s.ID(), "error", err)
		return err
	}
	return nil
}
