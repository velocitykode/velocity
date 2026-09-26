package stores

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/crypto"
)

var (
	ErrTokenNotFound = errors.New("velocity/csrf: token not found")
)

// SessionStore implements in-memory session-based CSRF token storage.
//
// A token lives on an idle clock: it expires after going idleLifetime
// without being read, and every Get restarts that clock. The framework sets
// the idle lifetime from the session's lifetime policy, so the token of an
// active session never ages out on its own; it ends with the session (a
// session that idles out or reaches its absolute cap gets a new id, and a
// token is only reachable through its session's id).
type SessionStore struct {
	tokens       map[string]*tokenEntry
	mu           sync.RWMutex
	idleLifetime time.Duration

	// lifecycleMu guards cancel so Start/Shutdown can race safely.
	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
}

type tokenEntry struct {
	token     string
	expiresAt time.Time
}

// NewSessionStore creates a new session-based token store.
// Call Start() to begin the background cleanup goroutine.
//
// An optional idle lifetime can be provided: how long a token stays valid
// without being read. Defaults to 24h if zero or omitted.
//
// Accepted call signatures:
//
//	NewSessionStore()                // 24h idle lifetime
//	NewSessionStore(idleLifetime)    // custom idle lifetime
func NewSessionStore(args ...any) *SessionStore {
	ttl := 24 * time.Hour

	for _, arg := range args {
		if v, ok := arg.(time.Duration); ok && v > 0 {
			ttl = v
		}
	}

	return &SessionStore{
		tokens:       make(map[string]*tokenEntry),
		idleLifetime: ttl,
	}
}

// Start begins the background goroutine that periodically removes expired
// tokens. The provided context controls the goroutine lifetime; pass
// context.Background() if you intend to stop it via Shutdown() instead.
//
// Calling Start again cancels the previous cleanup goroutine and replaces
// it, so repeated Start calls never leak goroutines.
func (s *SessionStore) Start(ctx context.Context) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	innerCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	async.Go(func() { s.cleanup(innerCtx) })
}

// Shutdown stops the background cleanup goroutine. It is safe to call
// more than once (and before Start) and honours the supplied context
// deadline for uniformity with other ShutdownAware types. Stop is
// instantaneous; the deadline is only consulted when it is already
// cancelled.
func (s *SessionStore) Shutdown(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.lifecycleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// Get retrieves a token for the given session ID and restarts its idle
// clock: every read (the XSRF cookie write on a safe request, the
// validation of an unsafe one) is session activity.
func (s *SessionStore) Get(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.tokens[id]
	if !exists {
		return "", ErrTokenNotFound
	}

	now := time.Now()
	if now.After(entry.expiresAt) {
		delete(s.tokens, id)
		return "", ErrTokenNotFound
	}
	entry.expiresAt = now.Add(s.idleLifetime)

	return entry.token, nil
}

// Set stores a token for the given session ID
func (s *SessionStore) Set(id string, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tokens[id] = &tokenEntry{
		token:     token,
		expiresAt: time.Now().Add(s.idleLifetime),
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

// ConsumeIfMatch implements csrf.AtomicConsumer. It atomically (under the
// store's write lock) compares the stored token for id against expected
// using a constant-time comparison, and deletes the entry only on match.
// In-memory locking is sufficient for the single-process case; cross-
// process deployments backed by Redis or another remote store must
// implement their own driver that uses a Lua script or equivalent atomic
// primitive.
//
// Returns consumed=true only when the entry existed, was unexpired, and
// matched expected. A missing/expired/mismatched entry returns
// consumed=false with no error.
func (s *SessionStore) ConsumeIfMatch(id string, expected string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.tokens[id]
	if !ok {
		return false, nil
	}
	if time.Now().After(entry.expiresAt) {
		// Expired entries are not a match; clean up opportunistically.
		delete(s.tokens, id)
		return false, nil
	}
	// Constant-time compare to avoid leaking a length/timing oracle to
	// callers who can probe entries via the public refresh handler.
	if !crypto.EqualString(entry.token, expected) {
		return false, nil
	}
	delete(s.tokens, id)
	return true, nil
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
