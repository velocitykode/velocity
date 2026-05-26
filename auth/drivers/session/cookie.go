package session

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

// CookieStore implements SessionStore using encrypted cookies
type CookieStore struct {
	config    auth.SessionConfig
	encryptor crypto.Encryptor
}

// NewCookieStore creates a new cookie session store with an injected encryptor.
func NewCookieStore(config auth.SessionConfig, encryptor crypto.Encryptor) (*CookieStore, error) {
	if encryptor == nil {
		return nil, fmt.Errorf("cookie store requires an encryptor")
	}
	return &CookieStore{
		config:    config,
		encryptor: encryptor,
	}, nil
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

// Get gets session from request
//
// Cookie payloads include an IssuedAt timestamp that is enforced server-side:
// any cookie older than SessionConfig.Lifetime minutes is rejected and a
// fresh empty session is returned. Without this, a captured cookie remains
// replayable indefinitely (until APP_KEY rotates) even if the client-side
// MaxAge/Expires says otherwise, since curl/replay tools ignore those.
//
// Legacy payloads without IssuedAt (zero time) are accepted to preserve
// rolling-deploy compatibility: the next Save() bumps IssuedAt, after which
// the new value enforces. Operators who want strict cutoff can rotate
// APP_KEY which invalidates every prior cookie.
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
		ID       string                 `json:"id"`
		Data     map[string]interface{} `json:"data"`
		Flash    map[string]interface{} `json:"flash"`
		IssuedAt time.Time              `json:"iat,omitempty"`
	}

	if err := json.Unmarshal([]byte(decrypted), &sessionData); err != nil {
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

	// Create session with data
	session := &CookieSession{
		BaseSession: auth.NewSession(sessionData.ID),
		store:       s,
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

	// Serialize session data. IssuedAt bumps on every Save so a rolling
	// active session keeps refreshing its server-side expiry window;
	// captured-and-replayed cookies past Lifetime are rejected in Get.
	sessionData := struct {
		ID       string                 `json:"id"`
		Data     map[string]interface{} `json:"data"`
		Flash    map[string]interface{} `json:"flash"`
		IssuedAt time.Time              `json:"iat,omitempty"`
	}{
		ID:       cookieSession.ID(),
		Data:     cookieSession.GetData(),
		Flash:    cookieSession.GetFlashData(),
		IssuedAt: cookieNowFn(),
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

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     s.config.Name,
		Value:    encrypted,
		Path:     s.config.Path,
		Domain:   s.config.Domain,
		MaxAge:   s.config.Lifetime * 60, // Convert minutes to seconds
		HttpOnly: s.config.HttpOnly,
		Secure:   s.config.Secure,
		SameSite: s.config.SameSite,
		Expires:  time.Now().Add(time.Duration(s.config.Lifetime) * time.Minute),
	})

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
}

// Save saves session to cookie
func (s *CookieSession) Save(w http.ResponseWriter) error {
	return s.store.Save(w, s)
}
