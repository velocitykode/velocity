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
)

// ErrInsecureSessionConfig is returned from SessionConfig.Validate when the
// configuration would ship insecure cookie defaults to production (e.g.,
// Secure=false, HttpOnly=false without opt-in, zero SameSite, or
// SameSite=None without Secure). A separate error type makes it trivial
// for bootstrap code to log-then-continue in dev and fail-fast in prod.
var ErrInsecureSessionConfig = errors.New("velocity/auth: insecure session config")

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

// SessionStore handles session storage
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

// Invalidate invalidates session
func (s *BaseSession) Invalidate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.destroyed = true
	s.data = make(map[string]interface{})
	s.flash = make(map[string]interface{})
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

// SessionConfig holds session configuration
type SessionConfig struct {
	Name     string
	Lifetime int // Minutes
	Path     string
	Domain   string
	Secure   bool
	HttpOnly bool
	SameSite http.SameSite

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
func (c SessionConfig) Validate(env string) error {
	if !c.HttpOnly && !c.AllowJSAccess {
		return fmt.Errorf("%w: HttpOnly=false requires AllowJSAccess=true opt-in", ErrInsecureSessionConfig)
	}
	if !c.Secure && !isNonProdEnvSession(env) {
		return fmt.Errorf("%w: Secure=false is not permitted in %q env (set APP_ENV=testing or development to allow)", ErrInsecureSessionConfig, env)
	}
	if c.SameSite == http.SameSiteDefaultMode {
		return fmt.Errorf("%w: SameSite must be set to Lax, Strict, or None (got default/zero)", ErrInsecureSessionConfig)
	}
	if c.SameSite == http.SameSiteNoneMode && !c.Secure {
		return fmt.Errorf("%w: SameSite=None requires Secure=true", ErrInsecureSessionConfig)
	}
	return nil
}

// isNonProdEnvSession reports whether env is a non-production environment
// that may relax the Secure cookie requirement. Mirrors the csrf helper but
// kept package-local to avoid an import cycle.
func isNonProdEnvSession(env string) bool {
	switch env {
	case "testing", "development":
		return true
	}
	return false
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
