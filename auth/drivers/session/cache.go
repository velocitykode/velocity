package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
)

// Key namespaces inside the shared cache. The meta key holds the JSON
// session record, the user key holds the set of session ids owned by one
// user, and the generation key holds the user's current revoke-all token.
const (
	cacheMetaPrefix = "session:meta:"
	cacheUserPrefix = "session:user:"
	cacheGenPrefix  = "session:gen:"
)

// userIndexSlack is added to the per-user index TTL so the set outlives
// the meta key it points at, absorbing clock skew between the two writes.
// The index expiry is extend-only (contract.CacheSetStore), so a short
// lived login after a long lived one never shortens the index.
const userIndexSlack = 1 * time.Minute

// cacheGenTokenBytes is the entropy of a revoke-all generation token.
const cacheGenTokenBytes = 16

var (
	// ErrCacheStoreNilBackend is returned by NewCacheStore when no cache
	// store is supplied.
	ErrCacheStoreNilBackend = errors.New("velocity/auth/session: cache backend is nil")

	// ErrCacheStoreUnsupported is returned by NewCacheStore when the cache
	// backend lacks the contract.CacheReplacer or contract.CacheSetStore
	// capability. Without them the store cannot make Touch and the user
	// index atomic, which is the point of this driver; the memory and
	// redis cache drivers implement both.
	ErrCacheStoreUnsupported = errors.New("velocity/auth/session: cache backend lacks replace or set operations")
)

// cacheBackend is what CacheStore needs from the cache: the base
// operations plus the two optional capabilities that make the writes
// atomic.
type cacheBackend interface {
	contract.Cache
	contract.CacheReplacer
	contract.CacheSetStore
}

// CacheStore is an auth.ServerSessionStore backed by a velocity cache
// store. It is the production driver: every app instance sharing the same
// cache backend (Redis) sees the same session records, so revocations
// issued on one instance are enforced on all of them.
//
// Every write is atomic on the backend:
//
//   - the per-user index is a backend set (contract.CacheSetStore), never
//     read-modify-written in this process;
//   - Touch goes through contract.CacheReplacer (SET XX), so a refresh that
//     loses the race against a revocation cannot recreate the record;
//   - DeleteAllForUser rotates the user's generation token before touching
//     the index, and Get rejects any record carrying an older token, so
//     "sign out everywhere" is authoritative even when the index is
//     incomplete. Every record carries a token from Put on, and a token
//     that cannot be read fails closed: the session is rejected.
//
// The zero value is not usable; construct with NewCacheStore.
type CacheStore struct {
	backend cacheBackend
	clock   func() time.Time
	rand    io.Reader
}

// NewCacheStore builds a CacheStore over backend. The backend must
// implement contract.CacheReplacer and contract.CacheSetStore (the memory
// and redis cache drivers do); ErrCacheStoreUnsupported is returned
// otherwise so a misconfigured deployment fails at boot, not at the first
// revocation. Pass the manager's default store:
//
//	store, err := cache.DefaultStore()
//	sessions, err := session.NewCacheStore(store)
//	authManager.SetServerSessionStore(sessions)
func NewCacheStore(backend contract.Cache) (*CacheStore, error) {
	if backend == nil {
		return nil, ErrCacheStoreNilBackend
	}
	full, ok := backend.(cacheBackend)
	if !ok {
		return nil, ErrCacheStoreUnsupported
	}
	return &CacheStore{backend: full, clock: time.Now, rand: rand.Reader}, nil
}

func cacheMetaKey(id string) string     { return cacheMetaPrefix + id }
func cacheUserKey(userID string) string { return cacheUserPrefix + userID }
func cacheGenKey(userID string) string  { return cacheGenPrefix + userID }

// cacheRecord is the JSON shape persisted under the meta key. It mirrors
// auth.StoredSession with explicit tags so the stored format is stable,
// plus the generation token the record was issued under.
type cacheRecord struct {
	ID         string         `json:"id"`
	UserID     string         `json:"user_id"`
	Data       map[string]any `json:"data,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	LastSeenAt time.Time      `json:"last_seen_at"`
	ExpiresAt  time.Time      `json:"expires_at"`
	IPAddress  string         `json:"ip_address,omitempty"`
	UserAgent  string         `json:"user_agent,omitempty"`
	Generation string         `json:"generation,omitempty"`
}

func (r *cacheRecord) toStored() *auth.StoredSession {
	return &auth.StoredSession{
		ID:         r.ID,
		UserID:     r.UserID,
		Data:       r.Data,
		CreatedAt:  r.CreatedAt,
		LastSeenAt: r.LastSeenAt,
		ExpiresAt:  r.ExpiresAt,
		IPAddress:  r.IPAddress,
		UserAgent:  r.UserAgent,
	}
}

func encodeRecord(r *cacheRecord) (string, error) {
	buf, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("velocity/auth/session: encode session %s: %w", r.ID, err)
	}
	return string(buf), nil
}

func decodeRecord(raw string) (*cacheRecord, error) {
	var r cacheRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, fmt.Errorf("velocity/auth/session: decode session: %w", err)
	}
	return &r, nil
}

// recordTTL returns the cache TTL for a record: the time left until
// ExpiresAt, floored at one second so a record about to expire is still
// written rather than dropped; zero ExpiresAt means no expiry.
func recordTTL(expiresAt, now time.Time) time.Duration {
	if expiresAt.IsZero() {
		return 0
	}
	d := expiresAt.Sub(now)
	if d < time.Second {
		return time.Second
	}
	return d
}

// generation returns the user's current generation token, or "" when it is
// absent or unreadable. The contract read surface does not distinguish a
// missing key from a backend error, so every record is stamped with a
// non-empty token at Put and a "" here never matches: an unreadable
// generation fails closed (the session is rejected), never open.
func (s *CacheStore) generation(ctx context.Context, userID string) string {
	gen, _ := s.backend.GetStringCtx(ctx, cacheGenKey(userID))
	return gen
}

// ensureGeneration returns the user's current generation token, creating
// one atomically (add-if-absent) when the user has none yet. Every Put
// runs through here so no record ever carries an empty token.
func (s *CacheStore) ensureGeneration(ctx context.Context, userID string) (string, error) {
	if gen := s.generation(ctx, userID); gen != "" {
		return gen, nil
	}
	candidate, err := s.newGeneration()
	if err != nil {
		return "", err
	}
	if _, err := s.backend.AddCtx(ctx, cacheGenKey(userID), candidate, 0); err != nil {
		return "", fmt.Errorf("velocity/auth/session: create generation: %w", err)
	}
	// Re-read rather than assume: a concurrent creator may have won the
	// add, and a backend that reports success but cannot serve the read
	// must not let a record be issued under a token nobody can verify.
	gen := s.generation(ctx, userID)
	if gen == "" {
		return "", errors.New("velocity/auth/session: generation unreadable after create")
	}
	return gen, nil
}

// newGeneration returns a fresh random token.
func (s *CacheStore) newGeneration() (string, error) {
	b := make([]byte, cacheGenTokenBytes)
	if _, err := io.ReadFull(s.rand, b); err != nil {
		return "", fmt.Errorf("velocity/auth/session: generate revocation token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// live loads the record for id and applies the liveness rules: absent is
// ErrSessionNotFound; past ExpiresAt is evicted and ErrSessionExpired;
// issued under a superseded generation is evicted and ErrSessionNotFound
// (it was revoked in bulk).
func (s *CacheStore) live(ctx context.Context, id string) (*cacheRecord, error) {
	raw, ok := s.backend.GetStringCtx(ctx, cacheMetaKey(id))
	if !ok {
		return nil, auth.ErrSessionNotFound
	}
	rec, err := decodeRecord(raw)
	if err != nil {
		return nil, err
	}
	if !rec.ExpiresAt.IsZero() && s.clock().After(rec.ExpiresAt) {
		s.evict(ctx, rec)
		return nil, auth.ErrSessionExpired
	}
	// Fail closed: a record without a token, or one whose token does not
	// match the current (possibly unreadable) generation, is revoked.
	if rec.Generation == "" || rec.Generation != s.generation(ctx, rec.UserID) {
		s.evict(ctx, rec)
		return nil, auth.ErrSessionNotFound
	}
	return rec, nil
}

// evict drops a record and its index membership, best effort: the meta key
// may already have been TTL-evicted and a stale index member is skipped by
// ListForUser.
func (s *CacheStore) evict(ctx context.Context, rec *cacheRecord) {
	_ = s.backend.ForgetCtx(ctx, cacheMetaKey(rec.ID))
	_ = s.backend.SetRemoveCtx(ctx, cacheUserKey(rec.UserID), rec.ID)
}

// Get implements auth.ServerSessionStore.
func (s *CacheStore) Get(ctx context.Context, id string) (*auth.StoredSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, auth.ErrSessionNotFound
	}
	rec, err := s.live(ctx, id)
	if err != nil {
		return nil, err
	}
	return rec.toStored(), nil
}

// Put implements auth.ServerSessionStore. It is the Login-time write: the
// record is stamped with the user's current generation token, LastSeenAt
// is set to now, and the id joins the user's index set.
func (s *CacheStore) Put(ctx context.Context, sess *auth.StoredSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sess == nil {
		return errors.New("velocity/auth/session: nil session")
	}
	if sess.ID == "" {
		return errors.New("velocity/auth/session: empty session id")
	}
	if sess.UserID == "" {
		return errors.New("velocity/auth/session: empty user id")
	}

	gen, err := s.ensureGeneration(ctx, sess.UserID)
	if err != nil {
		return err
	}
	now := s.clock()
	rec := &cacheRecord{
		ID:         sess.ID,
		UserID:     sess.UserID,
		Data:       sess.Data,
		CreatedAt:  sess.CreatedAt,
		LastSeenAt: now,
		ExpiresAt:  sess.ExpiresAt,
		IPAddress:  sess.IPAddress,
		UserAgent:  sess.UserAgent,
		Generation: gen,
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}

	// A session id re-bound to a different user must leave the previous
	// owner's index so ListForUser stays consistent.
	if raw, ok := s.backend.GetStringCtx(ctx, cacheMetaKey(rec.ID)); ok {
		if prev, err := decodeRecord(raw); err == nil && prev.UserID != rec.UserID {
			_ = s.backend.SetRemoveCtx(ctx, cacheUserKey(prev.UserID), rec.ID)
		}
	}

	encoded, err := encodeRecord(rec)
	if err != nil {
		return err
	}
	ttl := recordTTL(rec.ExpiresAt, now)
	if err := s.backend.PutCtx(ctx, cacheMetaKey(rec.ID), encoded, ttl); err != nil {
		return fmt.Errorf("velocity/auth/session: put session: %w", err)
	}
	// SetAddCtx is extend-only, so the index lives as long as its
	// longest-lived member (forever once a non-expiring session joins).
	indexTTL := ttl
	if indexTTL > 0 {
		indexTTL += userIndexSlack
	}
	if err := s.backend.SetAddCtx(ctx, cacheUserKey(rec.UserID), indexTTL, rec.ID); err != nil {
		// Roll the meta write back so no record exists that the user can
		// neither list nor revoke.
		_ = s.backend.ForgetCtx(ctx, cacheMetaKey(rec.ID))
		return fmt.Errorf("velocity/auth/session: index session: %w", err)
	}
	return nil
}

// Touch implements auth.ServerSessionStore. The refreshed record is written
// with contract.CacheReplacer, so it lands only if the key still exists:
// a Delete or DeleteAllForUser between the read and this write wins, and
// the caller sees ErrSessionNotFound.
func (s *CacheStore) Touch(ctx context.Context, id string, lastSeen time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return auth.ErrSessionNotFound
	}
	rec, err := s.live(ctx, id)
	if err != nil {
		return err
	}
	rec.LastSeenAt = lastSeen
	encoded, err := encodeRecord(rec)
	if err != nil {
		return err
	}
	replaced, err := s.backend.ReplaceCtx(ctx, cacheMetaKey(id), encoded, recordTTL(rec.ExpiresAt, s.clock()))
	if err != nil {
		return fmt.Errorf("velocity/auth/session: touch session: %w", err)
	}
	if !replaced {
		return auth.ErrSessionNotFound
	}
	return nil
}

// Delete implements auth.ServerSessionStore. Idempotent: a missing id is
// a no-op.
func (s *CacheStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	var userID string
	if raw, ok := s.backend.GetStringCtx(ctx, cacheMetaKey(id)); ok {
		if rec, err := decodeRecord(raw); err == nil {
			userID = rec.UserID
		}
	}
	if err := s.backend.ForgetCtx(ctx, cacheMetaKey(id)); err != nil {
		return fmt.Errorf("velocity/auth/session: delete session: %w", err)
	}
	if userID != "" {
		_ = s.backend.SetRemoveCtx(ctx, cacheUserKey(userID), id)
	}
	return nil
}

// DeleteAllForUser implements auth.ServerSessionStore. The generation
// token is rotated first, which on its own invalidates every record issued
// before this call (Get and Touch compare tokens); the indexed records are
// then removed so listings and storage catch up. A user with no sessions
// still gets a rotated token, which is harmless and keeps the call a
// single code path.
func (s *CacheStore) DeleteAllForUser(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if userID == "" {
		return nil
	}
	gen, err := s.newGeneration()
	if err != nil {
		return err
	}
	if err := s.backend.ForeverCtx(ctx, cacheGenKey(userID), gen); err != nil {
		return fmt.Errorf("velocity/auth/session: rotate generation: %w", err)
	}
	ids, err := s.backend.SetMembersCtx(ctx, cacheUserKey(userID))
	if err != nil {
		return fmt.Errorf("velocity/auth/session: load index: %w", err)
	}
	for _, id := range ids {
		if err := s.backend.ForgetCtx(ctx, cacheMetaKey(id)); err != nil {
			return fmt.Errorf("velocity/auth/session: delete session %s: %w", id, err)
		}
	}
	if len(ids) > 0 {
		if err := s.backend.SetRemoveCtx(ctx, cacheUserKey(userID), ids...); err != nil {
			return fmt.Errorf("velocity/auth/session: clear index: %w", err)
		}
	}
	return nil
}

// ListForUser implements auth.ServerSessionStore. Members whose record is
// gone, expired, or from a superseded generation are dropped from the
// index on the way out so it stays bounded. Data is never included.
func (s *CacheStore) ListForUser(ctx context.Context, userID string) ([]*auth.SessionMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if userID == "" {
		return nil, nil
	}
	ids, err := s.backend.SetMembersCtx(ctx, cacheUserKey(userID))
	if err != nil {
		return nil, fmt.Errorf("velocity/auth/session: load index: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]*auth.SessionMeta, 0, len(ids))
	stale := make([]string, 0)
	for _, id := range ids {
		rec, err := s.live(ctx, id)
		if err != nil {
			if errors.Is(err, auth.ErrSessionNotFound) || errors.Is(err, auth.ErrSessionExpired) {
				stale = append(stale, id)
				continue
			}
			return nil, err
		}
		if rec.UserID != userID {
			// The id was re-bound to another user after joining this index.
			stale = append(stale, id)
			continue
		}
		out = append(out, rec.toStored().ToMeta())
	}
	if len(stale) > 0 {
		_ = s.backend.SetRemoveCtx(ctx, cacheUserKey(userID), stale...)
	}
	return out, nil
}

// Compile-time check that CacheStore satisfies the interface.
var _ auth.ServerSessionStore = (*CacheStore)(nil)
