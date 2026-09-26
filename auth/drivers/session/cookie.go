// Package session provides session storage drivers.
//
// CookieStore (this file) is a self-contained, stateless-on-the-server store
// suitable for development and small deployments. It is NOT recommended for
// production: a captured cookie remains replayable until the IssuedAt window
// elapses (H-03 fix) or the in-process revocation list rejects it (H-04
// fix). Operators running multiple processes/hosts must use a shared
// ServerSessionStore so administrative revocations survive a single-host
// restart and propagate across the fleet.
package session

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/sessionclock"
)

// CookieStore implements SessionStore using encrypted cookies.
//
// SECURITY: CookieStore alone cannot enforce server-side Logout (see audit
// H-04). It carries a per-process revocation list so a Logout call rejects
// subsequent Gets for the same session ID, but the list is in-memory only:
// it is lost on restart and does not cross process boundaries. For
// production deployments install an auth.ServerSessionStore (Redis/SQL) via
// Manager.SetServerSessionStore so revocations survive across the fleet.
type CookieStore struct {
	config    auth.SessionConfig
	encryptor crypto.Encryptor

	// revokedMu protects revoked + revokedTTLs. The revocation list grows
	// only via Revoke calls; entries naturally age out after their cookie
	// lifetime expires (no infinite growth as long as session lifetime is
	// bounded).
	revokedMu   sync.RWMutex
	revoked     map[string]time.Time // sessionID -> revoked-at timestamp
	revokedTTLs map[string]time.Time // sessionID -> cookie expiry (cleanup hint)
}

// NewCookieStore creates a new cookie session store with an injected encryptor.
func NewCookieStore(config auth.SessionConfig, encryptor crypto.Encryptor) (*CookieStore, error) {
	if encryptor == nil {
		return nil, fmt.Errorf("cookie store requires an encryptor")
	}
	return &CookieStore{
		config:      config,
		encryptor:   encryptor,
		revoked:     make(map[string]time.Time),
		revokedTTLs: make(map[string]time.Time),
	}, nil
}

// Revoke marks sessionID as revoked. Subsequent Get calls for this id are
// rejected and a fresh empty session is returned, even if the cookie value
// is otherwise valid. The fix for H-04 (CookieStore Logout does not
// invalidate server-side): a captured cookie cannot be replayed after the
// user logs out.
//
// In-process scope: the revocation list lives in RAM and does not survive
// restart. Multi-host deployments MUST also install a real
// ServerSessionStore so revocations propagate.
func (s *CookieStore) Revoke(sessionID string) {
	if sessionID == "" {
		return
	}
	now := sessionclock.Now()
	// A revoked cookie was issued before now, so it expires within one
	// idle window (or the absolute cap when the policy has no idle
	// timeout); the entry only has to outlive it.
	lifetime := s.config.IdleTimeout()
	if lifetime <= 0 {
		lifetime = s.config.AbsoluteTimeout()
	}
	if lifetime <= 0 {
		// Without a bounded lifetime we cannot tell when to age out
		// the entry; conservatively keep it for 24h so the in-memory
		// map does not grow without bound.
		lifetime = 24 * time.Hour
	}
	s.revokedMu.Lock()
	defer s.revokedMu.Unlock()
	if s.revoked == nil {
		s.revoked = make(map[string]time.Time)
	}
	if s.revokedTTLs == nil {
		s.revokedTTLs = make(map[string]time.Time)
	}
	s.revoked[sessionID] = now
	s.revokedTTLs[sessionID] = now.Add(lifetime)
	// Opportunistic cleanup: when we Revoke, drop any expired
	// entries so the map does not grow indefinitely.
	for id, expiry := range s.revokedTTLs {
		if now.After(expiry) {
			delete(s.revoked, id)
			delete(s.revokedTTLs, id)
		}
	}
}

// isRevoked reports whether sessionID has been added to the revocation list
// and has not yet aged out.
func (s *CookieStore) isRevoked(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	s.revokedMu.RLock()
	expiry, ok := s.revokedTTLs[sessionID]
	s.revokedMu.RUnlock()
	if !ok {
		return false
	}
	return sessionclock.Now().Before(expiry)
}

// Create creates a new session
func (s *CookieStore) Create(id string) (auth.Session, error) {
	return &CookieSession{
		BaseSession: auth.NewSession(id),
		store:       s,
	}, nil
}

// Get gets session from request
//
// Cookie payloads carry an IssuedAt timestamp and an immutable CreatedAt,
// both enforced server-side under the session's lifetime policy
// (SessionConfig.ExpiresAt): a cookie issued longer than IdleLifetime ago
// is idle-expired, and one whose CreatedAt is older than AbsoluteLifetime
// has reached the absolute cap. Either way a fresh empty session is
// returned; curl and replay tools ignore the client-side MaxAge, so the
// check has to live here. IssuedAt slides forward every time the session
// scheme re-issues the cookie on activity, so an active session stays
// inside the idle window until the absolute cap ends it.
//
// When the expired cookie carried a signed-in user, the replacement
// reports AuthenticationExpired so the scheme can tell the caller the
// session expired rather than that it was never signed in.
//
// Legacy payloads without IssuedAt (zero time) are accepted to preserve
// rolling-deploy compatibility: the next Save() bumps IssuedAt, after which
// the new value enforces. Payloads without CreatedAt fall back to IssuedAt
// for the absolute check and have it persisted on the next Save. Operators
// who want strict cutoff can rotate APP_KEY which invalidates every prior
// cookie.
func (s *CookieStore) Get(r *http.Request, id string) (auth.Session, error) {
	// Get cookie
	cookie, err := r.Cookie(s.config.Name)
	if err != nil {
		return s.Create("")
	}

	// Decrypt cookie value
	decrypted, err := s.encryptor.Decrypt(cookie.Value)
	if err != nil {
		return s.Create("")
	}

	// Deserialize session data
	var sessionData struct {
		ID        string                 `json:"id"`
		Data      map[string]interface{} `json:"data"`
		Flash     map[string]interface{} `json:"flash"`
		IssuedAt  time.Time              `json:"iat,omitempty"`
		CreatedAt time.Time              `json:"cat,omitempty"`
	}

	if err := json.Unmarshal([]byte(decrypted), &sessionData); err != nil {
		return s.Create("")
	}

	// Revocation enforcement (H-04 fix). When Logout calls Revoke
	// against this session id, every subsequent Get returns a fresh
	// empty session even though the cookie value still decrypts.
	// In-process only: see CookieStore doc for the multi-host caveat.
	if s.isRevoked(sessionData.ID) {
		return s.Create("")
	}

	// Payloads minted before CreatedAt existed fall back to IssuedAt (the
	// oldest timestamp we hold) so live sessions gain the absolute cap
	// immediately; the next Save persists the fallback as the permanent
	// CreatedAt.
	createdAt := sessionData.CreatedAt
	if createdAt.IsZero() {
		createdAt = sessionData.IssuedAt
	}
	if s.expired(createdAt, sessionData.IssuedAt) {
		fresh, err := s.Create("")
		if err != nil {
			return fresh, err
		}
		if sessionData.Data[auth.UserIDSessionKey] != nil {
			fresh.(*CookieSession).authenticationExpired = true
		}
		return fresh, nil
	}

	// Create session with data
	session := &CookieSession{
		BaseSession: auth.NewSession(sessionData.ID),
		store:       s,
		createdAt:   createdAt,
		issuedAt:    sessionData.IssuedAt,
	}
	session.SetData(sessionData.Data)
	session.SetFlashData(sessionData.Flash)

	return session, nil
}

// expired reports whether a cookie created at createdAt and issued at
// issuedAt has ended under the lifetime policy at the current session time.
// A zero IssuedAt (legacy payload) skips the idle check; a zero CreatedAt
// skips the absolute check.
func (s *CookieStore) expired(createdAt, issuedAt time.Time) bool {
	now := sessionclock.Now()
	if idle := s.config.IdleTimeout(); idle > 0 && !issuedAt.IsZero() && now.After(issuedAt.Add(idle)) {
		return true
	}
	if abs := s.config.AbsoluteTimeout(); abs > 0 && !createdAt.IsZero() && now.After(createdAt.Add(abs)) {
		return true
	}
	return false
}

// Save saves session to cookie
func (s *CookieStore) Save(w http.ResponseWriter, session auth.Session) error {
	cookieSession, ok := session.(*CookieSession)
	if !ok {
		baseSession, ok := session.(*auth.BaseSession)
		if !ok {
			return auth.ErrInvalidSession
		}
		cookieSession = &CookieSession{
			BaseSession: baseSession,
			store:       s,
		}
	}

	// Check if session was destroyed
	if cookieSession.IsDestroyed() {
		// Delete cookie: same policy as the write, so the Path and Domain
		// match and the browser drops it.
		http.SetCookie(w, s.config.CookiePolicy().Cookie(s.config.Name, "", -1, s.config.HttpOnly))
		return nil
	}

	// Skip re-encryption when nothing changed since load. Every Encrypt
	// produces a new IV, so unconditionally refreshing the cookie on every
	// response rotates the ciphertext and breaks anything keyed by the
	// cookie value (e.g. CSRF token stores) on the next request.
	if !cookieSession.IsModified() {
		return nil
	}

	// CreatedAt is immutable: stamp it once for sessions that have never
	// been persisted (zero value here means new session, or a legacy load
	// that carried no timestamp at all), then copy the loaded value forward
	// verbatim on every subsequent Save. IssuedAt keeps bumping; CreatedAt
	// is the anchor the absolute-lifetime check in Get enforces against.
	now := sessionclock.Now()
	createdAt := cookieSession.createdAt
	if createdAt.IsZero() {
		createdAt = now
	}

	// Serialize session data. IssuedAt bumps on every Save so an active
	// session (the scheme re-issues the cookie on activity) keeps sliding
	// its idle window; captured-and-replayed cookies past IdleLifetime are
	// rejected in Get.
	sessionData := struct {
		ID        string                 `json:"id"`
		Data      map[string]interface{} `json:"data"`
		Flash     map[string]interface{} `json:"flash"`
		IssuedAt  time.Time              `json:"iat,omitempty"`
		CreatedAt time.Time              `json:"cat,omitempty"`
	}{
		ID:        cookieSession.ID(),
		Data:      cookieSession.GetData(),
		Flash:     cookieSession.GetFlashData(),
		IssuedAt:  now,
		CreatedAt: createdAt,
	}

	data, err := json.Marshal(sessionData)
	if err != nil {
		return err
	}

	// Encrypt data
	encrypted, err := s.encryptor.Encrypt(string(data))
	if err != nil {
		return err
	}

	// Build the cookie. With an idle timeout the cookie expires when the
	// lifetime policy ends the session: one idle window from now, or the
	// absolute cap when that comes first, so the browser drops it at the
	// same moment Get would reject it. With IdleLifetime == 0 the operator
	// wants a browser-session cookie: omit Expires entirely and leave
	// MaxAge at its zero value, which RFC 6265 specifies as "no Max-Age,
	// treat as session". Setting Expires=time.Now() (an earlier behaviour)
	// made the cookie appear already-expired in some browsers, which
	// silently dropped every Set-Cookie the framework emitted. Negative
	// IdleLifetime is rejected at SessionConfig.Validate.
	cookie := s.config.CookiePolicy().Cookie(s.config.Name, encrypted, 0, s.config.HttpOnly)
	if s.config.IdleTimeout() > 0 {
		expiresAt := s.config.ExpiresAt(createdAt, now)
		maxAge := int(expiresAt.Sub(now).Round(time.Second) / time.Second)
		if maxAge < 1 {
			maxAge = 1
		}
		cookie.MaxAge = maxAge
		cookie.Expires = expiresAt
	}
	http.SetCookie(w, cookie)

	// Persist the (possibly just-stamped) CreatedAt on the in-memory
	// session so further Saves within the same request copy it forward
	// instead of re-stamping.
	cookieSession.createdAt = createdAt
	cookieSession.issuedAt = now

	// Clear the modified flag so a second Save() on the same session
	// without intervening writes does not rotate the cookie. The check at
	// the top of Save() short-circuits when IsModified() is false; without
	// this reset, that gate is one-shot only.
	cookieSession.MarkClean()

	return nil
}

// Destroy destroys session
func (s *CookieStore) Destroy(id string) error {
	// Cookie destruction is handled in Save when session is invalidated
	return nil
}

// GarbageCollect performs garbage collection (not needed for cookies)
func (s *CookieStore) GarbageCollect(maxLifetime time.Duration) error {
	// Cookies handle their own expiration
	return nil
}

// CookieSession wraps BaseSession for cookie storage
type CookieSession struct {
	*auth.BaseSession
	store *CookieStore

	// createdAt carries the immutable first-creation timestamp from Get to
	// Save so the absolute-lifetime cap (V2-09) survives Save round-trips.
	// Zero means "never persisted yet": Save stamps it exactly once.
	// Regenerate clears it: a new session id is a new session, so a
	// sign-in restarts the absolute cap.
	createdAt time.Time

	// issuedAt is when the cookie this session was loaded from was issued
	// (or when Save last issued it). Zero for a session not loaded from a
	// cookie.
	issuedAt time.Time

	// authenticationExpired marks the empty session Get returned in place
	// of a signed-in cookie the lifetime policy had ended.
	authenticationExpired bool
}

// IssuedAt returns when the cookie this session was loaded from was
// issued, or when Save last issued it. It is the zero time for a session
// that has not been issued as a cookie. The session scheme reads it to
// re-issue the cookie on activity at most once per debounce interval.
func (s *CookieSession) IssuedAt() time.Time {
	return s.issuedAt
}

// AuthenticationExpired reports that the request carried a signed-in
// session cookie the lifetime policy had ended (idle for longer than
// IdleLifetime, or older than AbsoluteLifetime) and this session is its
// empty replacement.
func (s *CookieSession) AuthenticationExpired() bool {
	return s.authenticationExpired
}

// Regenerate gives the session a fresh id, keeping its data, and restarts
// its absolute lifetime: a new id is a new session.
func (s *CookieSession) Regenerate() error {
	if err := s.BaseSession.Regenerate(); err != nil {
		return err
	}
	s.createdAt = time.Time{}
	return nil
}

// Save saves session to cookie
func (s *CookieSession) Save(w http.ResponseWriter) error {
	return s.store.Save(w, s)
}
