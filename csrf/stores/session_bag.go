package stores

import (
	"context"
	"crypto/sha256"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/sessionclock"
)

// TokenSessionKey is the session bag key the CSRF token lives under.
const TokenSessionKey = "csrf.token"

// ErrNoSessionBag is returned when the request is not served under the
// session the token is asked for: no session middleware held a session
// for it, or the held session has a different id. No token is read from
// or written to a session that will not be saved with the response.
var ErrNoSessionBag = errors.New("velocity/csrf: no session held for this request")

// SessionBag is the key-value bag of the session a request is served
// under. auth.Session satisfies it.
type SessionBag interface {
	ID() string
	Get(key string) any
	Put(key string, value any)
	Remove(key string)
}

// defaultConsumedTokenLifetime is how long a consumed single-use token is
// remembered when NewSessionBagStore is given no lifetime.
const defaultConsumedTokenLifetime = 30 * 24 * time.Hour

// consumedPruneInterval bounds how often ConsumeIfMatch sweeps expired
// entries out of the consumed-token set.
const consumedPruneInterval = time.Minute

// SessionBagStore keeps the CSRF token in the session of the request, under
// TokenSessionKey. The token is saved with the session at the session's one
// save point and lives exactly as long as the session: it survives a
// restart and validates on every replica that can read the session, and a
// session that ends (idle timeout, absolute cap, logout) takes its token
// with it. It has no clock of its own.
//
// The session is found through the request context (bagFor), so the CSRF
// middleware must run inside the session middleware; velocity.New installs
// both on the router. Outside it the store holds no token (ErrNoSessionBag)
// and unsafe requests are rejected.
//
// Single-use tokens: ConsumeIfMatch removes the token from the session and
// records it as consumed in this process for consumedLifetime, because a
// session that lives in the cookie is still carried, token included, by a
// captured copy of the previous cookie, and the session scheme renews such
// a copy on activity. The record therefore has to last as long as any
// renewal of that copy can: the session's absolute cap. A session that
// still carries a consumed token gives it up the next time the store reads
// it (Get removes it, so a fresh token is minted and saved in its place).
// A token is accepted once per instance (ConsumedPerInstance): a replay on
// the same instance is refused, including a concurrent double submit; a
// replay on another instance can be accepted once there.
type SessionBagStore struct {
	bagFor           func(ctx context.Context) SessionBag
	consumedLifetime time.Duration

	mu        sync.Mutex
	consumed  map[[sha256.Size]byte]time.Time
	nextPrune time.Time

	outsideSessionLogged atomic.Bool
}

// NewSessionBagStore returns a store that keeps tokens in the session
// bagFor returns for a request context (nil when the request carries no
// session). consumedLifetime is how long a consumed single-use token stays
// refused; set it to the session's absolute cap, the longest a captured
// session cookie can be kept alive by renewal. Zero means 30 days, the
// default absolute cap.
func NewSessionBagStore(bagFor func(ctx context.Context) SessionBag, consumedLifetime time.Duration) *SessionBagStore {
	if consumedLifetime <= 0 {
		consumedLifetime = defaultConsumedTokenLifetime
	}
	return &SessionBagStore{
		bagFor:           bagFor,
		consumedLifetime: consumedLifetime,
		consumed:         make(map[[sha256.Size]byte]time.Time),
	}
}

// bag returns the session the request is served under when its id is id.
func (s *SessionBagStore) bag(ctx context.Context, id string) (SessionBag, error) {
	var b SessionBag
	if s.bagFor != nil && ctx != nil {
		b = s.bagFor(ctx)
	}
	if b == nil {
		if s.outsideSessionLogged.CompareAndSwap(false, true) {
			log.Printf("velocity/csrf: WARNING the CSRF token lives in the session, but a request reached the CSRF middleware outside the session middleware; no token is issued or accepted there. Mount the CSRF middleware on the app router")
		}
		return nil, ErrNoSessionBag
	}
	if id == "" || b.ID() != id {
		return nil, ErrNoSessionBag
	}
	return b, nil
}

// token returns the token held in b, or "".
func token(b SessionBag) string {
	v, _ := b.Get(TokenSessionKey).(string)
	return v
}

// Get returns the token held in the session with id id. A token this
// process recorded as consumed is removed from the session instead and
// reported as not found, so a captured session that still carries it is
// given a fresh token and never hands the consumed one back to a page.
func (s *SessionBagStore) Get(ctx context.Context, id string) (string, error) {
	b, err := s.bag(ctx, id)
	if err != nil {
		return "", err
	}
	t := token(b)
	if t == "" {
		return "", ErrTokenNotFound
	}
	if s.wasConsumed(t) {
		b.Remove(TokenSessionKey)
		return "", ErrTokenNotFound
	}
	return t, nil
}

// wasConsumed reports whether this process recorded t as consumed and the
// record has not expired.
func (s *SessionBagStore) wasConsumed(t string) bool {
	key := sha256.Sum256([]byte(t))
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.consumed[key]
	return ok && !sessionclock.Now().After(until)
}

// Set puts token in the session with id id. The session saves it with the
// response.
func (s *SessionBagStore) Set(ctx context.Context, id string, token string) error {
	b, err := s.bag(ctx, id)
	if err != nil {
		return err
	}
	b.Put(TokenSessionKey, token)
	return nil
}

// Delete removes the token from the session with id id. A session this
// request is not served under holds nothing reachable from here (its token
// ends with it), so that is not an error; neither is a missing token.
func (s *SessionBagStore) Delete(ctx context.Context, id string) error {
	b, err := s.bag(ctx, id)
	if err != nil {
		return nil
	}
	b.Remove(TokenSessionKey)
	return nil
}

// Exists reports whether the session with id id holds a token.
func (s *SessionBagStore) Exists(ctx context.Context, id string) bool {
	_, err := s.Get(ctx, id)
	return err == nil
}

// ConsumeIfMatch implements csrf.AtomicConsumer. It compares the session's
// token with expected in constant time and, on a match this process has not
// consumed before, removes it from the session and records it as consumed.
// The record is taken under one lock, so of two concurrent requests on this
// instance carrying the same token exactly one is accepted.
func (s *SessionBagStore) ConsumeIfMatch(ctx context.Context, id string, expected string) (bool, error) {
	b, err := s.bag(ctx, id)
	if err != nil {
		return false, nil
	}
	held := token(b)
	if held == "" || !crypto.EqualString(held, expected) {
		return false, nil
	}
	key := sha256.Sum256([]byte(held))
	now := sessionclock.Now()

	s.mu.Lock()
	if now.After(s.nextPrune) {
		for k, until := range s.consumed {
			if now.After(until) {
				delete(s.consumed, k)
			}
		}
		s.nextPrune = now.Add(consumedPruneInterval)
	}
	if until, ok := s.consumed[key]; ok && !now.After(until) {
		s.mu.Unlock()
		b.Remove(TokenSessionKey)
		return false, nil
	}
	s.consumed[key] = now.Add(s.consumedLifetime)
	s.mu.Unlock()

	b.Remove(TokenSessionKey)
	return true, nil
}

// ConsumptionScope implements csrf.AtomicConsumer: the token travels with
// the session to every instance while the consumed record stays in this
// process.
func (s *SessionBagStore) ConsumptionScope() ConsumptionScope {
	return ConsumedPerInstance
}
