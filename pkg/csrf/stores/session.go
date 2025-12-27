package stores

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrTokenNotFound = errors.New("token not found")
)

// SessionStore implements in-memory session-based CSRF token storage
type SessionStore struct {
	tokens map[string]*tokenEntry
	mu     sync.RWMutex
}

type tokenEntry struct {
	token     string
	expiresAt time.Time
}

// NewSessionStore creates a new session-based token store
func NewSessionStore() *SessionStore {
	s := &SessionStore{
		tokens: make(map[string]*tokenEntry),
	}
	// Start cleanup goroutine
	go s.cleanup()
	return s
}

// Get retrieves a token for the given session ID
func (s *SessionStore) Get(id string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.tokens[id]
	if !exists {
		return "", ErrTokenNotFound
	}

	// Check if expired
	if time.Now().After(entry.expiresAt) {
		return "", ErrTokenNotFound
	}

	return entry.token, nil
}

// Set stores a token for the given session ID
func (s *SessionStore) Set(id string, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tokens[id] = &tokenEntry{
		token:     token,
		expiresAt: time.Now().Add(24 * time.Hour),
	}
	return nil
}

// Delete removes a token
func (s *SessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.tokens, id)
	return nil
}

// Exists checks if a token exists and is not expired
func (s *SessionStore) Exists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.tokens[id]
	if !exists {
		return false
	}

	// Check if expired
	return !time.Now().After(entry.expiresAt)
}

// cleanup removes expired tokens every hour
func (s *SessionStore) cleanup() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for id, entry := range s.tokens {
			if now.After(entry.expiresAt) {
				delete(s.tokens, id)
			}
		}
		s.mu.Unlock()
	}
}
