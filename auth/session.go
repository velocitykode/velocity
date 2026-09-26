package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// ErrInsecureSessionConfig is returned from SessionConfig.Validate when the
// configuration would ship insecure cookie defaults to production (e.g.,
// Secure=false, HttpOnly=false without opt-in, zero SameSite, or
// SameSite=None without Secure). A separate error type makes it trivial
// for bootstrap code to log-then-continue in dev and fail-fast in prod.
var ErrInsecureSessionConfig = errors.New("velocity/auth: insecure session config")

// ErrInvalidLifetime is returned from SessionConfig.Validate when the
// configured IdleLifetime is negative. Negative lifetimes would translate to
// a cookie with Expires in the past which most browsers treat as already
// deleted; the framework refuses to boot rather than ship a no-op session
// cookie. IdleLifetime == 0 is permitted and produces a session-lifetime
// (no Expires / MaxAge=0) cookie per RFC 6265.
var ErrInvalidLifetime = errors.New("velocity/auth: session lifetime must be >= 0")

// sessionRandReader is the entropy source for session IDs. Tests may swap
// this for a failing reader to exercise rand.Read error paths.
var sessionRandReader io.Reader = rand.Reader

// Session represents a user session
type Session interface {
	// Get session ID
	ID() string

	// Get value from session
	Get(key string) interface{}

	// Put value in session
	Put(key string, value interface{})

	// Has checks if key exists
	Has(key string) bool

	// Remove value from session
	Remove(key string)

	// Clear all session data
	Clear()

	// Regenerate session ID
	Regenerate() error

	// Invalidate session
	Invalidate() error

	// Flash messages
	Flash(key string, value interface{})
	GetFlash(key string) interface{}

	// FlushFlash returns the entire flash bag and clears it in one call.
	// Returns nil (not an empty map) when the bag is empty so callers can
	// rely on JSON omitempty / nil checks.
	FlushFlash() map[string]interface{}

	// Save session
	Save(w http.ResponseWriter) error
}

// SessionStore loads and saves the session a request carries. The session
// scheme reads and writes the session only through it, so handlers use the
// same session API whichever store holds the data. The framework ships two:
// session.CookieStore keeps the session in an encrypted cookie (at most 4096
// bytes on the wire), and session.ServerStore keeps it in the session's
// server record and sends only the id. Pass one to the scheme with
// schemes.WithSessionStore.
type SessionStore interface {
	// Create a new session
	Create(id string) (Session, error)

	// Get session by ID
	Get(r *http.Request, id string) (Session, error)

	// Save session
	Save(w http.ResponseWriter, session Session) error

	// Destroy session
	Destroy(id string) error

	// Garbage collection
	GarbageCollect(maxLifetime time.Duration) error
}

// BaseSession provides common session functionality
type BaseSession struct {
	id        string
	data      map[string]interface{}
	flash     map[string]interface{}
	modified  bool
	destroyed bool
	mu        sync.RWMutex
}

// NewSession creates a new session. If id is empty, a random ID is generated;
// when generation fails (which only happens if crypto/rand returns an error),
// the zero-value *BaseSession is returned with empty id and the caller can
// detect the failure via ID() == "". Most callers should prefer NewSessionWithError.
func NewSession(id string) *BaseSession {
	s, _ := NewSessionWithError(id)
	return s
}

// NewSessionWithError creates a new session and returns any error from the
// underlying crypto/rand call used to generate the session ID.
//
// When id is empty a new ID is generated AND the session is marked as
// modified, so the next Save() will issue a Set-Cookie carrying the fresh
// ID. Without this the unconditional Save() behaviour previously masked
// the issue: dropping that unconditional save would otherwise drop the
// cookie for newly-created sessions and clients could never stabilise on
// a server-issued ID.
func NewSessionWithError(id string) (*BaseSession, error) {
	freshlyCreated := id == ""
	if freshlyCreated {
		generated, err := generateSessionID()
		if err != nil {
			return &BaseSession{
				data:  make(map[string]interface{}),
				flash: make(map[string]interface{}),
			}, err
		}
		id = generated
	}

	return &BaseSession{
		id:       id,
		data:     make(map[string]interface{}),
		flash:    make(map[string]interface{}),
		modified: freshlyCreated,
	}, nil
}

// ID returns session ID
func (s *BaseSession) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

// Get gets value from session
func (s *BaseSession) Get(key string) interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[key]
}

// Put puts value in session
func (s *BaseSession) Put(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	s.modified = true
}

// Has checks if key exists
func (s *BaseSession) Has(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[key]
	return ok
}

// Remove removes value from session
func (s *BaseSession) Remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	s.modified = true
}

// Clear clears all session data
func (s *BaseSession) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = make(map[string]interface{})
	s.flash = make(map[string]interface{})
	s.modified = true
}

// Regenerate regenerates session ID. Returns an error if the underlying
// crypto/rand call fails; in that case the session ID is left unchanged.
func (s *BaseSession) Regenerate() error {
	id, err := generateSessionID()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
	s.modified = true
	return nil
}

// Invalidate invalidates session.
//
// Beyond clearing the data/flash bags and marking the session destroyed,
// Invalidate rotates the session ID so that any caller holding the
// pre-invalidate ID cannot rehydrate state against this session.
// Without rotation an attacker who learned the old ID (logged elsewhere,
// browser history, etc.) could re-submit it before the store flushes the
// deletion and a downstream layer that rebuilds a session by ID would
// reattach to the old identifier. Invalidate is therefore a destroy plus
// an ID migration, not just a data wipe.
//
// A regenerate failure is non-fatal: the session is still marked
// destroyed, the bags are cleared, and the id is forced to empty so the
// rotation invariant ("the pre-invalidate id can no longer be reused as
// this session's id") holds even on the entropy-exhausted path.
func (s *BaseSession) Invalidate() error {
	// Generate a fresh ID before taking the write lock so the rare
	// crypto/rand failure path can bubble up without holding s.mu.
	newID, genErr := generateSessionID()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.destroyed = true
	s.data = make(map[string]interface{})
	s.flash = make(map[string]interface{})
	if genErr != nil {
		// Defence in depth: zero the ID so the pre-invalidate ID is no
		// longer the session's identifier even though we could not
		// produce a fresh one. The session is destroyed anyway; any
		// subsequent Save will be a no-op or a cookie-clear.
		s.id = ""
		s.modified = true
		return genErr
	}
	s.id = newID
	s.modified = true
	return nil
}

// Flash sets flash message
func (s *BaseSession) Flash(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flash[key] = value
	s.modified = true
}

// GetFlash gets and removes flash message
func (s *BaseSession) GetFlash(key string) interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.flash[key]
	if ok {
		delete(s.flash, key)
		s.modified = true
	}
	return value
}

// FlushFlash returns the entire flash bag and clears it. Returns nil when
// the bag is empty so callers can omit empty flash payloads via JSON
// omitempty without a separate length check.
func (s *BaseSession) FlushFlash() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.flash) == 0 {
		return nil
	}
	out := s.flash
	s.flash = make(map[string]interface{})
	s.modified = true
	return out
}

// Save saves session (implemented by stores)
func (s *BaseSession) Save(w http.ResponseWriter) error {
	// This should be overridden by specific store implementations
	return nil
}

// IsModified checks if session was modified
func (s *BaseSession) IsModified() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modified
}

// MarkModified flags the session as changed so the save seam writes it
// even though no value in it changed. The session scheme uses it to
// re-issue the cookie on activity, which slides the cookie's idle window.
func (s *BaseSession) MarkModified() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modified = true
}

// MarkClean clears the modified flag. Session stores call this after a
// successful Save() so that a subsequent Save() on the same in-memory
// session, without intervening writes, is a no-op. Every Encrypt
// produces a fresh IV, so re-emitting Set-Cookie on every response
// rotates the cookie value and breaks anything keyed by it (e.g. CSRF
// token stores).
func (s *BaseSession) MarkClean() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modified = false
}

// IsDestroyed checks if session was destroyed
func (s *BaseSession) IsDestroyed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.destroyed
}

// GetData returns session data (for serialization)
func (s *BaseSession) GetData() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Create a copy to avoid race conditions
	data := make(map[string]interface{})
	for k, v := range s.data {
		data[k] = v
	}
	return data
}

// SetData sets session data (for deserialization)
func (s *BaseSession) SetData(data map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = data
}

// GetFlashData returns flash data (for serialization)
func (s *BaseSession) GetFlashData() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Create a copy to avoid race conditions
	flash := make(map[string]interface{})
	for k, v := range s.flash {
		flash[k] = v
	}
	return flash
}

// SetFlashData sets flash data (for deserialization)
func (s *BaseSession) SetFlashData(flash map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flash = flash
}

// generateSessionID generates a random session ID.
// Returns an error when the entropy source fails rather than panicking.
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(sessionRandReader, b); err != nil {
		return "", fmt.Errorf("velocity/auth: failed to generate session id: %w", err)
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// Session store names for SessionConfig.Store.
const (
	// SessionStoreCookie keeps the whole session (data and flash) in the
	// encrypted session cookie. It is the default.
	SessionStoreCookie = "cookie"

	// SessionStoreServer keeps the session on the server, in the record the
	// ServerSessionStore holds for it; the cookie carries only the session
	// id.
	SessionStoreServer = "server"
)

// SessionConfig holds session configuration.
//
// IdleLifetime and AbsoluteLifetime are the session's one lifetime policy,
// shared by the session cookie, the server session record and the CSRF
// token bound to the session: a session ends when it has gone IdleLifetime
// without a request, or when it reaches AbsoluteLifetime from sign-in,
// whichever comes first (see ExpiresAt).
type SessionConfig struct {
	Name string

	// IdleLifetime is the idle timeout in minutes: a session that receives
	// no request for this long ends, and every request slides it forward
	// (the cookie is re-issued and the server record's expiry refreshed,
	// both at most once a minute). Zero means no idle timeout: the cookie
	// is a browser-session cookie (no Max-Age) and only AbsoluteLifetime
	// bounds the session.
	IdleLifetime int // Minutes

	Path     string
	Domain   string
	Secure   bool
	HttpOnly bool
	SameSite http.SameSite

	// AbsoluteLifetime caps a session's total age in minutes, measured from
	// its creation (a sign-in regenerates the session and restarts it),
	// regardless of activity. The idle window slides on every request, so
	// an active session would otherwise live forever; this cap ends it
	// unconditionally, on the cookie and the server record alike.
	//
	// Zero means "use the framework default" (30 days), NOT disabled:
	// leaving the field unset still bounds session age (fail-secure). A
	// NEGATIVE value is the explicit opt-out that disables the absolute
	// cap entirely; only set this deliberately, it restores the
	// unbounded-session behaviour. When positive it must be >= IdleLifetime
	// (Validate rejects an absolute cap shorter than the idle window).
	AbsoluteLifetime int // Minutes; 0 = default (30 days), negative = no cap (explicit opt-out)

	// RememberLifetime is how long, in minutes, a remember-me credential
	// signs its user back in after the session has ended. It is the remember
	// cookie's Max-Age, independent of IdleLifetime and AbsoluteLifetime:
	// those end the session, the remember credential then starts a new one
	// (a revival is a sign-in and restarts the absolute cap). Every revival
	// reissues the credential for another RememberLifetime. Zero means the
	// framework default (30 days); negative is rejected by Validate.
	RememberLifetime int // Minutes; 0 = default (30 days)

	// AllowJSAccess opts in to HttpOnly=false. Without this flag the
	// session cookie MUST be HttpOnly, otherwise JavaScript (and any
	// injected script) can steal the session ID. Name is intentionally
	// loud so reviewers notice.
	AllowJSAccess bool

	// AllowCookieStoreInProduction opts the framework into running the
	// default CookieStore in a production environment without a
	// ServerSessionStore wired up. Without this flag, App.Bootstrap
	// refuses to boot in production when no ServerSessionStore is
	// installed: a captured cookie cannot be invalidated server-wide on
	// Logout (only the in-process revocation list rejects it, and that
	// list does not cross process boundaries). Operators who accept the
	// single-host risk profile (small / dev-like prod) MUST opt in here;
	// the name is loud so reviewers notice. See audit H-04.
	AllowCookieStoreInProduction bool

	// Store names where the session lives: SessionStoreCookie (the
	// default when empty) keeps it in the encrypted cookie, which browsers
	// cap at 4096 bytes; SessionStoreServer keeps it in the server session
	// record and sends only the id. velocity.New reads it (env
	// SESSION_STORE) to pick the session scheme's store.
	Store string
}

// defaultAbsoluteLifetime is the absolute session-age cap applied when
// SessionConfig.AbsoluteLifetime is zero (unset). Fail-secure: an
// unconfigured field still bounds total session age instead of leaving
// kept-warm sessions immortal.
const defaultAbsoluteLifetime = 30 * 24 * time.Hour

// defaultRememberLifetime is the remember-me credential lifetime applied
// when SessionConfig.RememberLifetime is zero (unset).
const defaultRememberLifetime = 30 * 24 * time.Hour

// RememberTimeout returns how long a remember-me credential lasts:
// RememberLifetime minutes when positive, 30 days otherwise.
func (c SessionConfig) RememberTimeout() time.Duration {
	if c.RememberLifetime > 0 {
		return time.Duration(c.RememberLifetime) * time.Minute
	}
	return defaultRememberLifetime
}

// IdleTimeout returns how long a session may go without a request before
// it ends. Zero means the session has no idle timeout.
func (c SessionConfig) IdleTimeout() time.Duration {
	if c.IdleLifetime <= 0 {
		return 0
	}
	return time.Duration(c.IdleLifetime) * time.Minute
}

// AbsoluteTimeout returns the cap on a session's total age: AbsoluteLifetime
// minutes when positive, 30 days when zero, and zero (no cap) when
// negative.
func (c SessionConfig) AbsoluteTimeout() time.Duration {
	switch {
	case c.AbsoluteLifetime > 0:
		return time.Duration(c.AbsoluteLifetime) * time.Minute
	case c.AbsoluteLifetime < 0:
		return 0
	default:
		return defaultAbsoluteLifetime
	}
}

// ExpiresAt returns when a session created at createdAt and last active at
// lastActive ends under this policy: the earlier of lastActive plus the
// idle timeout and createdAt plus the absolute cap. A policy with neither
// returns the zero time (the session never expires on its own).
func (c SessionConfig) ExpiresAt(createdAt, lastActive time.Time) time.Time {
	var end time.Time
	if idle := c.IdleTimeout(); idle > 0 {
		end = lastActive.Add(idle)
	}
	if abs := c.AbsoluteTimeout(); abs > 0 {
		if capAt := createdAt.Add(abs); end.IsZero() || capAt.Before(end) {
			end = capAt
		}
	}
	return end
}

// sessionRecordGrace is how long a server session record outlives the
// session it backs. The session scheme touches the record at most once a
// minute and always before it writes a cookie, so a record that ends one
// minute after the policy's end is never gone while the cookie is live: an
// idle session is reported as expired, never as revoked because a store had
// already reaped its record. The session is checked server-side against the
// policy's end, so the grace extends nothing a client can use.
const sessionRecordGrace = time.Minute

// RecordExpiresAt returns when the server record of a session created at
// createdAt and last active at lastActive ends: ExpiresAt plus one minute of
// grace, so the record always outlives the cookie it backs. A policy with
// neither an idle timeout nor an absolute cap returns the zero time.
func (c SessionConfig) RecordExpiresAt(createdAt, lastActive time.Time) time.Time {
	end := c.ExpiresAt(createdAt, lastActive)
	if end.IsZero() {
		return end
	}
	return end.Add(sessionRecordGrace)
}

// CookiePolicy derives the framework cookie policy from the session
// cookie's Path, Domain, Secure and SameSite. velocity.New calls it once on
// the validated config and carries the result on app.Services, so every
// framework cookie (XSRF token, remember, maintenance bypass and their
// deletions; flash rides in the session) follows the session cookie's
// attributes.
func (c SessionConfig) CookiePolicy() contract.CookiePolicy {
	return contract.NewCookiePolicy(c.Path, c.Domain, c.Secure, c.SameSite)
}

// Validate checks the SessionConfig for insecure defaults. Pass env to
// enable environment-aware rules: Secure=false is permitted when env is
// "testing" or "development", rejected otherwise. An empty env is treated
// as production for strict validation.
//
// Rules:
//   - HttpOnly must be true unless AllowJSAccess is set
//   - Secure must be true outside testing/development
//   - SameSite must be set (non-zero value)
//   - SameSite=None requires Secure=true
//   - IdleLifetime must be >= 0 (negative produces an already-expired cookie)
//   - AbsoluteLifetime, when positive, must be >= IdleLifetime (an absolute cap
//     shorter than the rolling window is a misconfiguration)
//   - RememberLifetime must be >= 0
func (c SessionConfig) Validate(env string) error {
	if c.IdleLifetime < 0 {
		return fmt.Errorf("%w: got %d minutes", ErrInvalidLifetime, c.IdleLifetime)
	}
	if c.AbsoluteLifetime > 0 && c.AbsoluteLifetime < c.IdleLifetime {
		return fmt.Errorf("%w: AbsoluteLifetime %d minutes is shorter than IdleLifetime %d minutes", ErrInvalidLifetime, c.AbsoluteLifetime, c.IdleLifetime)
	}
	if c.RememberLifetime < 0 {
		return fmt.Errorf("%w: RememberLifetime %d minutes is negative", ErrInvalidLifetime, c.RememberLifetime)
	}
	if !c.HttpOnly && !c.AllowJSAccess {
		return fmt.Errorf("%w: HttpOnly=false requires AllowJSAccess=true opt-in", ErrInsecureSessionConfig)
	}
	if !c.Secure && !contract.IsDevOrTestEnv(env) {
		return fmt.Errorf("%w: Secure=false is not permitted in %q env (set APP_ENV to a dev or test profile to allow)", ErrInsecureSessionConfig, env)
	}
	if c.SameSite == 0 || c.SameSite == http.SameSiteDefaultMode {
		return fmt.Errorf("%w: SameSite must be set to Lax, Strict, or None (got default/zero)", ErrInsecureSessionConfig)
	}
	if c.SameSite == http.SameSiteNoneMode && !c.Secure {
		return fmt.Errorf("%w: SameSite=None requires Secure=true", ErrInsecureSessionConfig)
	}
	return nil
}

// GetSessionFromRequest gets session from request
func GetSessionFromRequest(r *http.Request, store SessionStore, name string) (Session, error) {
	// Try to get session ID from cookie
	cookie, err := r.Cookie(name)
	if err != nil {
		// No cookie, create new session
		return store.Create("")
	}

	// Get existing session
	session, err := store.Get(r, cookie.Value)
	if err != nil {
		// Session not found or invalid, create new
		return store.Create("")
	}

	return session, nil
}
