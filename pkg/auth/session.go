package auth

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

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

// NewSession creates a new session
func NewSession(id string) *BaseSession {
	if id == "" {
		id = generateSessionID()
	}

	return &BaseSession{
		id:    id,
		data:  make(map[string]interface{}),
		flash: make(map[string]interface{}),
	}
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

// Regenerate regenerates session ID
func (s *BaseSession) Regenerate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = generateSessionID()
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

// generateSessionID generates a random session ID
func generateSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID
		return base64.URLEncoding.EncodeToString([]byte(time.Now().String()))
	}
	return base64.URLEncoding.EncodeToString(b)
}

// SessionConfig holds session configuration
type SessionConfig struct {
	Driver   string
	Name     string
	Lifetime int // Minutes
	Path     string
	Domain   string
	Secure   bool
	HttpOnly bool
	SameSite http.SameSite
}

// NewSessionConfigFromEnv creates a SessionConfig from environment variables
func NewSessionConfigFromEnv() SessionConfig {
	lifetime := 120 // Default 2 hours
	if envLifetime := os.Getenv("SESSION_LIFETIME"); envLifetime != "" {
		if parsed, err := strconv.Atoi(envLifetime); err == nil {
			lifetime = parsed
		}
	}

	secure := false
	if os.Getenv("SESSION_SECURE") == "true" {
		secure = true
	}

	httpOnly := true
	if os.Getenv("SESSION_HTTP_ONLY") == "false" {
		httpOnly = false
	}

	sameSite := http.SameSiteLaxMode
	switch os.Getenv("SESSION_SAME_SITE") {
	case "strict":
		sameSite = http.SameSiteStrictMode
	case "none":
		sameSite = http.SameSiteNoneMode
	}

	driver := os.Getenv("SESSION_DRIVER")
	if driver == "" {
		driver = "cookie"
	}

	name := os.Getenv("SESSION_NAME")
	if name == "" {
		name = "velocity_session"
	}

	path := os.Getenv("SESSION_PATH")
	if path == "" {
		path = "/"
	}

	return SessionConfig{
		Driver:   driver,
		Name:     name,
		Lifetime: lifetime,
		Path:     path,
		Domain:   os.Getenv("SESSION_DOMAIN"),
		Secure:   secure,
		HttpOnly: httpOnly,
		SameSite: sameSite,
	}
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
