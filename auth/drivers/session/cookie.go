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
	now := cookieNowFn()
	lifetime := time.Duration(s.config.Lifetime) * time.Minute
	if lifetime <= 0 {
		// Without a configured lifetime we cannot tell when to age out
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
	return cookieNowFn().Before(expiry)
}

// Create creates a new session
func (s *CookieStore) Create(id string) (auth.Session, error) {
	return &CookieSession{
		BaseSession: auth.NewSession(id),
		store:       s,
	}, nil
}

// cookieNowFn is the wall-clock source used for IssuedAt enforcement. Tests
// may override it to simulate cookies minted in the past so the expiry
// rejection path can be exercised deterministically. Production code must
// never reassign this; it exists solely as a test seam.
var cookieNowFn = time.Now

// defaultAbsoluteLifetime is the absolute session-age cap applied when
// SessionConfig.AbsoluteLifetime is zero (unset). Fail-secure: an
// unconfigured field still bounds total session age instead of leaving
// kept-warm sessions immortal (V2-09).
const defaultAbsoluteLifetime = 30 * 24 * time.Hour

// absoluteLifetime resolves the configured absolute cap. Positive config
// values are minutes; zero falls back to defaultAbsoluteLifetime; negative
// is the explicit "no absolute cap" opt-out and returns 0 (disabled).
func (s *CookieStore) absoluteLifetime() time.Duration {
	switch {
	case s.config.AbsoluteLifetime > 0:
		return time.Duration(s.config.AbsoluteLifetime) * time.Minute
	case s.config.AbsoluteLifetime < 0:
		return 0
	default:
		return defaultAbsoluteLifetime
	}
}

// Get gets session from request
//
// Cookie payloads include an IssuedAt timestamp that is enforced server-side:
// any cookie older than SessionConfig.Lifetime minutes is rejected and a
// fresh empty session is returned. Without this, a captured cookie remains
// replayable indefinitely (until APP_KEY rotates) even if the client-side
// MaxAge/Expires says otherwise, since curl/replay tools ignore those.
//
// Payloads also carry an immutable CreatedAt stamped at first Save. Because
// IssuedAt slides forward on every Save, an actively-used session would
// otherwise never expire; Get additionally rejects any cookie whose total
// age exceeds the absolute cap (SessionConfig.AbsoluteLifetime, default 30
// days when unset, negative to disable).
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

	// Server-side expiry enforcement. When IssuedAt is non-zero (cookies
	// minted by the post-fix Save), enforce Lifetime minutes from issue.
	// Zero IssuedAt is the legacy/no-config path: skip enforcement and
	// let the next Save bump the timestamp.
	if !sessionData.IssuedAt.IsZero() && s.config.Lifetime > 0 {
		lifetime := time.Duration(s.config.Lifetime) * time.Minute
		if cookieNowFn().After(sessionData.IssuedAt.Add(lifetime)) {
			return s.Create("")
		}
	}

	// Absolute lifetime enforcement (V2-09). IssuedAt slides forward on
	// every Save, so the rolling Lifetime window alone never ends an
	// actively-used session. CreatedAt is stamped once at first Save and
	// copied forward verbatim thereafter; reject when total session age
	// exceeds the absolute cap. Payloads minted before this field existed
	// have no CreatedAt: fall back to IssuedAt (the oldest timestamp we
	// hold) so live sessions survive the deploy and gain a cap immediately;
	// the next Save persists the fallback as the permanent CreatedAt.
	createdAt := sessionData.CreatedAt
	if createdAt.IsZero() {
		createdAt = sessionData.IssuedAt
	}
	if abs := s.absoluteLifetime(); abs > 0 && !createdAt.IsZero() {
		if cookieNowFn().After(createdAt.Add(abs)) {
			return s.Create("")
		}
	}

	// Create session with data
	session := &CookieSession{
		BaseSession: auth.NewSession(sessionData.ID),
		store:       s,
		createdAt:   createdAt,
	}
	session.SetData(sessionData.Data)
	session.SetFlashData(sessionData.Flash)

	return session, nil
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
		// Delete cookie
		http.SetCookie(w, &http.Cookie{
			Name:     s.config.Name,
			Value:    "",
			Path:     s.config.Path,
			Domain:   s.config.Domain,
			MaxAge:   -1,
			HttpOnly: s.config.HttpOnly,
			Secure:   s.config.Secure,
			SameSite: s.config.SameSite,
		})
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
	createdAt := cookieSession.createdAt
	if createdAt.IsZero() {
		createdAt = cookieNowFn()
	}

	// Serialize session data. IssuedAt bumps on every Save so a rolling
	// active session keeps refreshing its server-side expiry window;
	// captured-and-replayed cookies past Lifetime are rejected in Get.
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
		IssuedAt:  cookieNowFn(),
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

	// Build the cookie. When Lifetime <= 0 the operator wants a session-
	// lifetime cookie (no persistent expiry): omit Expires entirely and
	// leave MaxAge at its zero value, which RFC 6265 specifies as "no
	// Max-Age, treat as session". Setting Expires=time.Now() (the previous
	// behaviour) made the cookie appear already-expired in some browsers,
	// which silently dropped every Set-Cookie the framework emitted.
	// Negative Lifetime is rejected at SessionConfig.Validate so we only
	// have to handle the >0 and ==0 cases here.
	cookie := &http.Cookie{
		Name:     s.config.Name,
		Value:    encrypted,
		Path:     s.config.Path,
		Domain:   s.config.Domain,
		HttpOnly: s.config.HttpOnly,
		Secure:   s.config.Secure,
		SameSite: s.config.SameSite,
	}
	if s.config.Lifetime > 0 {
		cookie.MaxAge = s.config.Lifetime * 60
		cookie.Expires = cookieNowFn().Add(time.Duration(s.config.Lifetime) * time.Minute)
	}
	http.SetCookie(w, cookie)

	// Persist the (possibly just-stamped) CreatedAt on the in-memory
	// session so further Saves within the same request copy it forward
	// instead of re-stamping.
	cookieSession.createdAt = createdAt

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
	createdAt time.Time
}

// Save saves session to cookie
func (s *CookieSession) Save(w http.ResponseWriter) error {
	return s.store.Save(w, s)
}
