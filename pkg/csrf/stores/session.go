package stores

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrTokenNotFound = errors.New("token not found")
)

// SessionStore implements in-memory session-based CSRF token storage
type SessionStore struct {
	tokens   map[string]*tokenEntry
	mu       sync.RWMutex
	lifetime time.Duration
	cancel   context.CancelFunc
}

type tokenEntry struct {
	token     string
	expiresAt time.Time
}

// NewSessionStore creates a new session-based token store.
// An optional lifetime can be provided; defaults to 24h if zero or omitted.
// Call Close() when done to stop the background cleanup goroutine.
func NewSessionStore(lifetime ...time.Duration) *SessionStore {
	ttl := 24 * time.Hour
	if len(lifetime) > 0 && lifetime[0] > 0 {
		ttl = lifetime[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &SessionStore{
		tokens:   make(map[string]*tokenEntry),
		lifetime: ttl,
		cancel:   cancel,
	}
	// Start cleanup goroutine
	go s.cleanup(ctx)
	return s
}

// Close stops the background cleanup goroutine.
func (s *SessionStore) Close() {
	s.cancel()
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
		expiresAt: time.Now().Add(s.lifetime),
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

// cleanup removes expired tokens every hour until the context is cancelled.
func (s *SessionStore) cleanup(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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
}
