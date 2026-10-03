package stores

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/sessionclock"
	"github.com/velocitykode/velocity/trace"
)

// TokenSessionKey is the session bag key the CSRF token lives under.
const TokenSessionKey = "csrf.token"

// ErrNoSessionBag is returned when the request is not served under the
// session the token is asked for: no session middleware held a session
// for it, or the held session has a different id. No token is read from
// or written to a session that will not be saved with the response.
var ErrNoSessionBag = errors.New("velocity/csrf: no session held for this request")

// ErrSessionSealed is returned when a token would be written into a
// session its request already saved (a write queued behind the session
// save rotating the token): nothing saves the session again, so the token
// the client holds would stay the valid one while the write reported
// success.
var ErrSessionSealed = errors.New("velocity/csrf: the request's session was already saved; no token written")

// SharedBag is the optional capability of a session whose data the other
// requests of the same session share through a server-side record
// (session.ServerSession has it; a session held in its cookie does not).
//
// UpdateShared runs update on the value the record holds under key
// (exists reports whether it holds one) and leaves what update returns
// there (keep=false removes the key), as one step against every other
// write to the record from every request and instance sharing it. update
// may run more than once (a record store that retries on a conflicting
// write) and must depend on its arguments only; an error from it aborts
// the write and is returned. UpdateShared returns the value the record
// holds afterwards and keeps the session as the record is, as its value
// and as the base its save measures changes against, so the session's own
// save never writes the key again over a later change. A session with no
// record yet (created or regenerated, and not saved) is shared with no
// other request: update runs on the session's own value. A session saved
// to a record that is gone gets an error wrapping
// contract.ErrSessionRecordGone; the record is not recreated.
//
// SessionBagStore makes every token write through it when the session has
// it: the mint (LoadOrStore), Set, Delete and single-use consumption are
// then atomic on the shared record, so single use holds deployment-wide
// wherever the record is shared. Without it the store reads and writes the
// request's own copy, saved with the response: concurrent requests of one
// session are last-write-wins, and single use is enforced on each instance
// by the record of consumed tokens kept in this process. Reads (Get) are
// always the request's snapshot of the session.
type SharedBag interface {
	UpdateShared(ctx context.Context, key string, update func(current any, exists bool) (next any, keep bool, err error)) (held any, exists bool, err error)
}

// sealedBag is the optional capability of a session that reports it was
// saved by its request. auth.BaseSession, and so every framework session,
// has it.
type sealedBag interface {
	Sealed() bool
}

// SessionBag is the key-value bag of the session a request is served
// under. contract.Session satisfies it.
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

	// logMu guards logger, which SetLogger may replace while requests read
	// it.
	logMu  sync.RWMutex
	logger contract.Logger
}

// SetLogger installs the logger the store writes its warning to (a request
// that reached the CSRF middleware outside the session middleware). Unset
// or nil, it goes through the framework's standalone fallback logger. The
// CSRF instance holding the store hands it its own logger. Safe to call
// while the store serves requests.
func (s *SessionBagStore) SetLogger(l contract.Logger) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logger = l
}

var _ contract.LoggerAware = (*SessionBagStore)(nil)

// log returns the installed logger, or the fallback logger when none is,
// bound to the request, trace and span ids ctx carries.
func (s *SessionBagStore) log(ctx context.Context) contract.Logger {
	s.logMu.RLock()
	l := fallbacklog.Resolve(s.logger)
	s.logMu.RUnlock()
	return l.With(trace.LogFields(ctx)...)
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
			s.log(ctx).Warn("velocity/csrf: the CSRF token lives in the session, but a request reached the CSRF middleware outside the session middleware; no token is issued or accepted there. Mount the CSRF middleware on the app router")
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
		s.removeToken(ctx, b, t)
		return "", ErrTokenNotFound
	}
	return t, nil
}

// wasConsumed reports whether this process recorded t as consumed and the
// record has not expired.
func (s *SessionBagStore) wasConsumed(t string) bool {
	key := sha256.Sum256([]byte(t))
	now := sessionclock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.consumed[key]
	return ok && !now.After(until)
}

// Set puts token in the session with id id, replacing the token held: in
// the shared record at once when the session is a SharedBag, else in the
// request's session, which saves it with the response. A session its
// request already saved is left as it is and ErrSessionSealed returned.
func (s *SessionBagStore) Set(ctx context.Context, id string, token string) error {
	b, err := s.bag(ctx, id)
	if err != nil {
		return err
	}
	if sb, ok := b.(sealedBag); ok && sb.Sealed() {
		return ErrSessionSealed
	}
	_, err = s.update(ctx, b, func(string) (string, bool) { return token, true })
	return err
}

// LoadOrStore implements csrf.Store. It returns the token the session with
// id id holds (loaded=true), or stores candidate there and returns it when
// the session holds none, or holds one this process recorded as consumed.
// In a session that implements SharedBag the check and the store are one
// step on the shared record, against every request of the session on every
// instance sharing it: requests that loaded the session before any of them
// minted (tabs opened at once after the token was revoked) all get the one
// token the first of them stored. Otherwise the request's own session is
// read and written. A session its request already saved is left as it is
// and ErrSessionSealed returned.
func (s *SessionBagStore) LoadOrStore(ctx context.Context, id, candidate string) (string, bool, error) {
	b, err := s.bag(ctx, id)
	if err != nil {
		return "", false, err
	}
	if sealed(b) {
		return "", false, ErrSessionSealed
	}
	held, err := s.update(ctx, b, func(current string) (string, bool) {
		if current != "" && !s.wasConsumed(current) {
			return current, true
		}
		return candidate, true
	})
	if err != nil {
		return "", false, err
	}
	if held == "" {
		return "", false, errNoSharedToken
	}
	return held, held != candidate, nil
}

// errNoSharedToken reports a SharedBag that kept no token where one was
// stored.
var errNoSharedToken = errors.New("velocity/csrf: the session's shared record kept no token")

// Delete removes the token from the session with id id: from the shared
// record at once when the session is a SharedBag, else from the request's
// session. A session this request is not served under holds nothing
// reachable from here (its token ends with it), so that is not an error;
// neither is a missing token, nor a shared record that is gone.
func (s *SessionBagStore) Delete(ctx context.Context, id string) error {
	b, err := s.bag(ctx, id)
	if err != nil {
		return nil
	}
	_, err = s.update(ctx, b, func(string) (string, bool) { return "", false })
	if errchain.Is(err, contract.ErrSessionRecordGone) {
		return nil
	}
	return err
}

// update runs fn on the token b holds ("" for none) and leaves what it
// returns there (keep=false removes the token), returning the token held
// afterwards ("" for none). In a SharedBag it runs as one UpdateShared step
// on the shared record, so fn may run more than once and must depend on
// its argument only; otherwise it reads and writes the request's session,
// writing only a change.
func (s *SessionBagStore) update(ctx context.Context, b SessionBag, fn func(current string) (next string, keep bool)) (string, error) {
	if shared, ok := b.(SharedBag); ok {
		held, exists, err := shared.UpdateShared(ctx, TokenSessionKey, func(current any, _ bool) (any, bool, error) {
			t, _ := current.(string)
			next, keep := fn(t)
			if !keep {
				return nil, false, nil
			}
			return next, true, nil
		})
		if err != nil || !exists {
			return "", err
		}
		t, _ := held.(string)
		return t, nil
	}
	current := b.Get(TokenSessionKey)
	t, _ := current.(string)
	next, keep := fn(t)
	switch {
	case !keep:
		if current != nil {
			b.Remove(TokenSessionKey)
		}
		return "", nil
	case next != t || current == nil:
		b.Put(TokenSessionKey, next) //store-rmw-ok: a bag without SharedBag is this request's own copy, saved whole with the response; SharedBag documents that it is last-write-wins
	}
	return next, nil
}

// Exists reports whether the session with id id holds a token.
func (s *SessionBagStore) Exists(ctx context.Context, id string) bool {
	_, err := s.Get(ctx, id)
	return err == nil
}

// ConsumeIfMatch implements csrf.AtomicConsumer. In a session that
// implements SharedBag (and its request has not saved it) it compares the
// token the shared record holds with expected in constant time and removes
// it on a match, as one step on the record: of concurrent requests carrying
// the same token, on every instance sharing the record, exactly one is
// accepted, and a token another request stored since this one loaded the
// session is compared and kept, never removed by this request's save. The
// consumed token is also recorded in this process, so a request loaded
// before the consume, reading it from its snapshot, mints a fresh one
// instead of handing it out. Otherwise it compares the session's token
// and, on a match this process has not consumed before, removes it from
// the session and records it as consumed; the record is taken under one
// lock, so of two concurrent requests on this instance carrying the same
// token exactly one is accepted.
func (s *SessionBagStore) ConsumeIfMatch(ctx context.Context, id string, expected string) (bool, error) {
	b, err := s.bag(ctx, id)
	if err != nil {
		return false, nil
	}
	if _, ok := b.(SharedBag); ok && !sealed(b) {
		var consumed bool
		_, err := s.update(ctx, b, func(current string) (string, bool) {
			consumed = current != "" && crypto.EqualString(current, expected)
			return current, !consumed && current != ""
		})
		if err != nil {
			if errchain.Is(err, contract.ErrSessionRecordGone) {
				return false, nil
			}
			return false, err
		}
		if consumed {
			// Requests of the session loaded before the consume still
			// read the token from their snapshot: the record of consumed
			// tokens makes them mint a fresh one instead of handing it out.
			s.recordConsumed(expected)
		}
		return consumed, nil
	}
	held := token(b)
	if held == "" || !crypto.EqualString(held, expected) {
		return false, nil
	}
	if !s.recordConsumed(held) {
		b.Remove(TokenSessionKey)
		return false, nil
	}
	b.Remove(TokenSessionKey)
	return true, nil
}

// recordConsumed records t as consumed in this process and reports
// whether it was not recorded already; the record is taken under one lock,
// so of concurrent callers with the same token exactly one gets true.
func (s *SessionBagStore) recordConsumed(t string) bool {
	key := sha256.Sum256([]byte(t))
	now := sessionclock.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if now.After(s.nextPrune) {
		for k, until := range s.consumed {
			if now.After(until) {
				delete(s.consumed, k)
			}
		}
		s.nextPrune = now.Add(consumedPruneInterval)
	}
	if until, ok := s.consumed[key]; ok && !now.After(until) {
		return false
	}
	s.consumed[key] = now.Add(s.consumedLifetime)
	return true
}

// sealed reports whether b's request already saved it.
func sealed(b SessionBag) bool {
	sb, ok := b.(sealedBag)
	return ok && sb.Sealed()
}

// removeToken removes t, a token this process recorded as consumed that
// the request read from b. In a SharedBag the record loses it only while
// it still holds t, so a token another request stored since is kept;
// otherwise, or when the record cannot be written, the request's session
// drops it.
func (s *SessionBagStore) removeToken(ctx context.Context, b SessionBag, t string) {
	if _, ok := b.(SharedBag); ok && !sealed(b) {
		_, err := s.update(ctx, b, func(current string) (string, bool) {
			if current != "" && crypto.EqualString(current, t) {
				return "", false
			}
			return current, current != ""
		})
		if err == nil {
			return
		}
	}
	b.Remove(TokenSessionKey)
}

// ConsumptionScope implements csrf.AtomicConsumer: the token travels with
// the session to every instance while the consumed record stays in this
// process.
func (s *SessionBagStore) ConsumptionScope() ConsumptionScope {
	return ConsumedPerInstance
}
