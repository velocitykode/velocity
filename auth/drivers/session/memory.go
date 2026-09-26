package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/internal/sessionclock"
)

// defaultSweepInterval is how often the background goroutine reaps
// expired sessions when no override is supplied.
const defaultSweepInterval = 1 * time.Minute

// MemoryStore is an in-process implementation of auth.ServerSessionStore.
// It is intended for development, testing, and single-process deployments.
// Production multi-process deployments should use a Redis or database
// backed driver. All map access is RWMutex protected.
type MemoryStore struct {
	mu sync.RWMutex
	// byID is the canonical storage, keyed by session id.
	byID map[string]*auth.StoredSession
	// byUser is a secondary index from user id to the set of session
	// ids belonging to that user. Always kept in sync with byID under
	// the same mutex.
	byUser map[string]map[string]struct{}

	sweepInterval time.Duration
	stop          chan struct{}
	stopOnce      sync.Once
	started       bool
	clock         func() time.Time
}

// MemoryOption configures a MemoryStore at construction time.
type MemoryOption func(*MemoryStore)

// WithSweepInterval overrides the default 1 minute background sweep
// cadence. Values <= 0 are ignored.
func WithSweepInterval(d time.Duration) MemoryOption {
	return func(s *MemoryStore) {
		if d > 0 {
			s.sweepInterval = d
		}
	}
}

// NewMemoryStore constructs a MemoryStore and starts the background
// expiry sweep goroutine. Call Close to stop the goroutine.
func NewMemoryStore(opts ...MemoryOption) *MemoryStore {
	s := &MemoryStore{
		byID:          make(map[string]*auth.StoredSession),
		byUser:        make(map[string]map[string]struct{}),
		sweepInterval: defaultSweepInterval,
		stop:          make(chan struct{}),
		clock:         sessionclock.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.started = true
	// async.Go wraps the goroutine in panic recovery so a runtime panic
	// in the sweep loop is reported via the framework panic handler
	// rather than crashing the process.
	async.Go(func() { s.sweepLoop() })
	return s
}

// sweepLoop periodically reaps expired sessions until Close is called.
func (s *MemoryStore) sweepLoop() {
	t := time.NewTicker(s.sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.sweepExpired()
		}
	}
}

// sweepExpired removes every session whose ExpiresAt is in the past.
func (s *MemoryStore) sweepExpired() {
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.byID {
		if !sess.ExpiresAt.IsZero() && now.After(sess.ExpiresAt) {
			s.removeLocked(id)
		}
	}
}

// removeLocked deletes a session from byID and byUser. Caller must hold
// the write lock.
func (s *MemoryStore) removeLocked(id string) {
	sess, ok := s.byID[id]
	if !ok {
		return
	}
	delete(s.byID, id)
	if set, ok := s.byUser[sess.UserID]; ok {
		delete(set, id)
		if len(set) == 0 {
			delete(s.byUser, sess.UserID)
		}
	}
}

// cloneStored returns a defensive copy so callers cannot mutate stored
// state through the returned pointer.
func cloneStored(in *auth.StoredSession) *auth.StoredSession {
	out := *in
	out.Data = cloneData(in.Data)
	return &out
}

// cloneData copies the top level of a record's Data. Nested values are
// shared: writers hand the store a fresh tree on every write and never
// mutate one they handed over.
func cloneData(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Get returns the StoredSession for id. Expired sessions are removed
// and reported as ErrSessionExpired.
func (s *MemoryStore) Get(ctx context.Context, id string) (*auth.StoredSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Snapshot under the read lock: Touch mutates the stored record in
	// place under the write lock, so the copy must not happen after RUnlock.
	s.mu.RLock()
	sess, ok := s.byID[id]
	var snap *auth.StoredSession
	if ok {
		snap = cloneStored(sess)
	}
	s.mu.RUnlock()
	if !ok {
		return nil, auth.ErrSessionNotFound
	}
	if !snap.ExpiresAt.IsZero() && s.clock().After(snap.ExpiresAt) {
		s.mu.Lock()
		s.removeLocked(id)
		s.mu.Unlock()
		return nil, auth.ErrSessionExpired
	}
	return snap, nil
}

// Put creates or replaces a session record. ID is required; an empty
// UserID is a signed-out visitor's record, kept out of the user index.
// LastSeenAt is updated to the current time on every call.
func (s *MemoryStore) Put(ctx context.Context, sess *auth.StoredSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sess == nil {
		return errors.New("velocity/auth/session: nil session")
	}
	if sess.ID == "" {
		return errors.New("velocity/auth/session: empty session id")
	}
	stored := cloneStored(sess)
	now := s.clock()
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = now
	}
	stored.LastSeenAt = now

	s.mu.Lock()
	defer s.mu.Unlock()
	// If the id is already mapped to a different user, drop the stale
	// secondary-index entry so the set stays consistent.
	if existing, ok := s.byID[stored.ID]; ok && existing.UserID != stored.UserID {
		if set, ok := s.byUser[existing.UserID]; ok {
			delete(set, stored.ID)
			if len(set) == 0 {
				delete(s.byUser, existing.UserID)
			}
		}
	}
	s.byID[stored.ID] = stored
	if stored.UserID == "" {
		return nil
	}
	set, ok := s.byUser[stored.UserID]
	if !ok {
		set = make(map[string]struct{})
		s.byUser[stored.UserID] = set
	}
	set[stored.ID] = struct{}{}
	return nil
}

// Touch slides an existing record under the store mutex: LastSeenAt and
// ExpiresAt take the given values. It never inserts: a missing id returns auth.ErrSessionNotFound, and a record
// past ExpiresAt is removed and reported as auth.ErrSessionExpired, so an
// activity refresh that loses the race against Delete or DeleteAllForUser
// cannot resurrect the revoked session.
func (s *MemoryStore) Touch(ctx context.Context, id string, lastSeen, expiresAt time.Time) error {
	return s.slide(ctx, id, lastSeen, expiresAt, false, nil)
}

// UpdateData replaces an existing record's Data and slides it like Touch,
// under the store mutex. It never inserts: a missing id returns
// auth.ErrSessionNotFound and an expired record is removed and reported as
// auth.ErrSessionExpired.
func (s *MemoryStore) UpdateData(ctx context.Context, id string, data map[string]any, lastSeen, expiresAt time.Time) error {
	return s.slide(ctx, id, lastSeen, expiresAt, true, data)
}

// slide is the update-if-present write behind Touch and UpdateData.
func (s *MemoryStore) slide(ctx context.Context, id string, lastSeen, expiresAt time.Time, replaceData bool, data map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return auth.ErrSessionNotFound
	}
	if !sess.ExpiresAt.IsZero() && s.clock().After(sess.ExpiresAt) {
		s.removeLocked(id)
		return auth.ErrSessionExpired
	}
	sess.LastSeenAt = lastSeen
	sess.ExpiresAt = expiresAt
	if replaceData {
		sess.Data = cloneData(data)
	}
	return nil
}

// Delete removes a single session. It is idempotent: deleting a missing
// id returns nil.
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(id)
	return nil
}

// DeleteAllForUser removes every session record belonging to userID.
func (s *MemoryStore) DeleteAllForUser(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.byUser[userID]
	if !ok {
		return nil
	}
	// Snapshot ids to avoid mutating the map while iterating.
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	for _, id := range ids {
		s.removeLocked(id)
	}
	return nil
}

// ListForUser returns metadata for every non-expired session belonging
// to userID. Expired entries encountered during the walk are removed.
func (s *MemoryStore) ListForUser(ctx context.Context, userID string) ([]*auth.SessionMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.byUser[userID]
	if !ok {
		return nil, nil
	}
	out := make([]*auth.SessionMeta, 0, len(set))
	expired := make([]string, 0)
	for id := range set {
		sess, ok := s.byID[id]
		if !ok {
			continue
		}
		if !sess.ExpiresAt.IsZero() && now.After(sess.ExpiresAt) {
			expired = append(expired, id)
			continue
		}
		out = append(out, sess.ToMeta())
	}
	for _, id := range expired {
		s.removeLocked(id)
	}
	return out, nil
}

// Close stops the background sweep goroutine. It is idempotent and safe
// to call concurrently. The context is honoured for cancellation but
// teardown is essentially instant.
func (s *MemoryStore) Close(ctx context.Context) error {
	s.stopOnce.Do(func() {
		close(s.stop)
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// Compile-time check that MemoryStore satisfies the interface.
var _ auth.ServerSessionStore = (*MemoryStore)(nil)
