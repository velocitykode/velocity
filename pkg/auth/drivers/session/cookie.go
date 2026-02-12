package session

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/velocitykode/velocity/pkg/auth"
	"github.com/velocitykode/velocity/pkg/crypto"
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

// Get gets session from request
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
		ID    string                 `json:"id"`
		Data  map[string]interface{} `json:"data"`
		Flash map[string]interface{} `json:"flash"`
	}

	if err := json.Unmarshal([]byte(decrypted), &sessionData); err != nil {
		return s.Create("")
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

	// Serialize session data
	sessionData := struct {
		ID    string                 `json:"id"`
		Data  map[string]interface{} `json:"data"`
		Flash map[string]interface{} `json:"flash"`
	}{
		ID:    cookieSession.ID(),
		Data:  cookieSession.GetData(),
		Flash: cookieSession.GetFlashData(),
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
