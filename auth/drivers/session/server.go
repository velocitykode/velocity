package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/internal/sessionclock"
)

// Keys of the session payload inside a server record's Data.
const (
	recordDataKey  = "data"
	recordFlashKey = "flash"
)

// sessionIDBytes is the entropy of a framework session id; the id is its
// base64url encoding (see auth.NewSession).
const sessionIDBytes = 32

// ServerStore implements auth.SessionStore on the server: the session's
// data and flash live in the session's auth.ServerSessionStore record, and
// the cookie carries only the session id, so a session of any size costs
// one short cookie. The record is the same one the session scheme uses as
// its revocation index, so each session has exactly one server record,
// holding its owner, timestamps and payload together.
//
// Lifetime: the record is the authority. Every save slides it to the
// session lifetime policy's end (auth.SessionConfig.RecordExpiresAt, the
// policy end plus one minute of grace), and Get checks the policy itself
// on the record's CreatedAt and LastSeenAt, so an idle or capped session
// ends on the next request whatever the cookie says. The cookie's Max-Age
// is the policy end, as with CookieStore, so the browser drops it no later
// than the record ends.
//
// A signed-out visitor's session gets a record too (empty UserID), created
// on its first save; it is never listed or removed by the user operations.
// When the session id rotates (sign-in, remember-me revival, logout) the
// save removes the record of the old id, so a planted or stale id no
// longer names any data.
//
// The zero value is not usable; construct with NewServerStore.
type ServerStore struct {
	config  auth.SessionConfig
	records atomic.Pointer[recordsHolder]
}

// recordsHolder boxes the interface so atomic.Pointer can swap it whole.
type recordsHolder struct{ store auth.ServerSessionStore }

// NewServerStore builds a ServerStore that keeps sessions in records. The
// session scheme passes later auth.Manager.SetServerSessionStore calls on
// to it (SetServerSessionStore), so the payload always lives in the record
// the revocation checks read. Returns auth.ErrNoServerSessionStore when
// records is nil.
func NewServerStore(config auth.SessionConfig, records auth.ServerSessionStore) (*ServerStore, error) {
	if records == nil {
		return nil, auth.ErrNoServerSessionStore
	}
	s := &ServerStore{config: config}
	s.records.Store(&recordsHolder{store: records})
	return s, nil
}

// SetServerSessionStore points the store at records. A nil records leaves
// the store unable to load or save sessions (every Save returns
// auth.ErrNoServerSessionStore) until one is installed again.
func (s *ServerStore) SetServerSessionStore(records auth.ServerSessionStore) {
	s.records.Store(&recordsHolder{store: records})
}

func (s *ServerStore) loadRecords() auth.ServerSessionStore {
	h := s.records.Load()
	if h == nil {
		return nil
	}
	return h.store
}

// Create returns a new, unsaved session. An empty id generates one.
func (s *ServerStore) Create(id string) (auth.Session, error) {
	return &ServerSession{
		BaseSession: auth.NewSession(id),
		store:       s,
		ctx:         context.Background(),
	}, nil
}

// Get loads the session whose id the request's cookie carries. Anything
// that does not name a live record under the lifetime policy yields a
// fresh empty session:
//
//   - an id that is not a framework session id (never looked up);
//   - a record past the policy's idle timeout or absolute cap, which is
//     removed; the replacement reports AuthenticationExpired when the
//     record was signed in (or the store itself reported it expired);
//   - no record at all: the replacement reports RecordDeleted, which the
//     session scheme reports as auth.ErrSessionRevoked (a revocation
//     deletes the record);
//   - a store that cannot be read: the visitor is treated as signed out.
func (s *ServerStore) Get(r *http.Request, id string) (auth.Session, error) {
	records := s.loadRecords()
	if records == nil || !validSessionID(id) {
		return s.Create("")
	}
	ctx := r.Context()
	rec, err := records.Get(ctx, id)
	if err != nil {
		fresh, createErr := s.Create("")
		if createErr != nil {
			return fresh, createErr
		}
		switch {
		case errors.Is(err, auth.ErrSessionExpired):
			fresh.(*ServerSession).authenticationExpired = true
		case errors.Is(err, auth.ErrSessionNotFound):
			fresh.(*ServerSession).recordDeleted = true
		}
		return fresh, nil
	}

	if end := s.config.ExpiresAt(rec.CreatedAt, rec.LastSeenAt); !end.IsZero() && sessionclock.Now().After(end) {
		_ = records.Delete(ctx, id)
		fresh, createErr := s.Create("")
		if createErr != nil {
			return fresh, createErr
		}
		fresh.(*ServerSession).authenticationExpired = rec.UserID != ""
		return fresh, nil
	}

	data, flash, err := decodeRecordPayload(rec.Data)
	if err != nil {
		return s.Create("")
	}
	session := &ServerSession{
		BaseSession: auth.NewSession(rec.ID),
		store:       s,
		ctx:         context.WithoutCancel(ctx),
		savedID:     rec.ID,
		createdAt:   rec.CreatedAt,
		issuedAt:    rec.LastSeenAt,
	}
	session.SetData(data)
	session.SetFlashData(flash)
	return session, nil
}

// Save writes the session to its record and sends the id cookie.
//
//   - A destroyed session removes the record it was loaded from (or last
//     saved to) and deletes the cookie.
//   - An unmodified session writes nothing.
//   - Otherwise the record takes the payload and slides to the lifetime
//     policy's end (UpdateData, update-if-present). A session id with no
//     record yet is created (Put) only for a signed-out session; a
//     signed-in session's record is written at sign-in, so a missing one
//     means it was revoked and Save fails instead of recreating it. The
//     record of an id the session rotated away from is removed first; a
//     failure to remove it fails the save.
//
// Any store failure is returned and no cookie is written.
func (s *ServerStore) Save(w http.ResponseWriter, session auth.Session) error {
	ss, ok := session.(*ServerSession)
	if !ok {
		return auth.ErrInvalidSession
	}
	records := s.loadRecords()
	if records == nil {
		return auth.ErrNoServerSessionStore
	}
	ctx := ss.context()

	if ss.IsDestroyed() {
		if ss.savedID != "" {
			if err := records.Delete(ctx, ss.savedID); err != nil {
				return fmt.Errorf("velocity/auth/session: delete session record: %w", err)
			}
			ss.savedID = ""
		}
		http.SetCookie(w, s.config.CookiePolicy().Cookie(s.config.Name, "", -1, s.config.HttpOnly))
		return nil
	}
	if !ss.IsModified() {
		return nil
	}

	id := ss.ID()
	if id == "" {
		return auth.ErrInvalidSession
	}
	now := sessionclock.Now()
	createdAt := ss.createdAt
	if createdAt.IsZero() {
		createdAt = now
	}
	payload, err := encodeRecordPayload(ss.GetData(), ss.GetFlashData())
	if err != nil {
		return err
	}
	recordEnd := s.config.RecordExpiresAt(createdAt, now)

	// Retire the record of an id the session rotated away from before
	// writing under the new id, and fail closed when that is not possible:
	// a captured cookie naming the old id must not keep a live record.
	if ss.savedID != "" && ss.savedID != id {
		if err := records.Delete(ctx, ss.savedID); err != nil && !errors.Is(err, auth.ErrSessionNotFound) {
			return fmt.Errorf("velocity/auth/session: retire previous session record: %w", err)
		}
		ss.savedID = ""
	}

	err = records.UpdateData(ctx, id, payload, now, recordEnd)
	if errors.Is(err, auth.ErrSessionNotFound) && id != ss.savedID && ss.Get(auth.UserIDSessionKey) == nil {
		err = records.Put(ctx, &auth.StoredSession{
			ID:         id,
			Data:       payload,
			CreatedAt:  createdAt,
			LastSeenAt: now,
			ExpiresAt:  recordEnd,
		})
	}
	if err != nil {
		return fmt.Errorf("velocity/auth/session: save session record: %w", err)
	}

	// The cookie carries the id alone and ends when the policy ends the
	// session; the record outlives it by the grace. With no idle timeout
	// it is a browser-session cookie, as with CookieStore.
	cookie := s.config.CookiePolicy().Cookie(s.config.Name, id, 0, s.config.HttpOnly)
	if s.config.IdleTimeout() > 0 {
		end := s.config.ExpiresAt(createdAt, now)
		maxAge := int(end.Sub(now).Round(time.Second) / time.Second)
		if maxAge < 1 {
			maxAge = 1
		}
		cookie.MaxAge = maxAge
		cookie.Expires = end
	}
	http.SetCookie(w, cookie)

	ss.savedID = id
	ss.createdAt = createdAt
	ss.issuedAt = now
	ss.MarkClean()
	return nil
}

// Destroy removes the record for id.
func (s *ServerStore) Destroy(id string) error {
	records := s.loadRecords()
	if records == nil {
		return auth.ErrNoServerSessionStore
	}
	return records.Delete(context.Background(), id)
}

// GarbageCollect is a no-op: records end with their ExpiresAt in the
// ServerSessionStore.
func (s *ServerStore) GarbageCollect(maxLifetime time.Duration) error {
	return nil
}

// ServerSession is a session held by ServerStore.
type ServerSession struct {
	*auth.BaseSession
	store *ServerStore

	// ctx is the context store calls run under: the loading request's
	// context without its cancellation (a save must finish even when the
	// client has gone), or the background context for a created session.
	ctx context.Context

	// savedID is the id of the record this session was loaded from or last
	// saved to; empty when it has none yet.
	savedID string

	// createdAt is the record's CreatedAt, the anchor of the absolute cap;
	// zero until first saved. Regenerate clears it: a sign-in starts a new
	// session.
	createdAt time.Time

	// issuedAt is when the id cookie was last issued (the record's
	// LastSeenAt at load, or the last Save).
	issuedAt time.Time

	authenticationExpired bool
	recordDeleted         bool
}

func (s *ServerSession) context() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// IssuedAt returns when the session's cookie was last issued, or the zero
// time for a session never saved. The session scheme reads it to re-issue
// the cookie, and slide the record, on activity.
func (s *ServerSession) IssuedAt() time.Time {
	return s.issuedAt
}

// AuthenticationExpired reports that the request named a session the
// lifetime policy had ended and this session is its empty replacement.
func (s *ServerSession) AuthenticationExpired() bool {
	return s.authenticationExpired
}

// RecordDeleted reports that the request's cookie named a session whose
// record no longer exists (revoked, or reaped by the store) and this session
// is its empty replacement.
func (s *ServerSession) RecordDeleted() bool {
	return s.recordDeleted
}

// Regenerate gives the session a fresh id, keeping its data, and restarts
// its absolute lifetime. The next Save writes the data under the new id and
// removes the old id's record.
func (s *ServerSession) Regenerate() error {
	if err := s.BaseSession.Regenerate(); err != nil {
		return err
	}
	s.createdAt = time.Time{}
	return nil
}

// Save saves the session through its store.
func (s *ServerSession) Save(w http.ResponseWriter) error {
	return s.store.Save(w, s)
}

// validSessionID reports whether id has the shape of a framework session
// id, so a forged cookie never reaches the store.
func validSessionID(id string) bool {
	if len(id) != base64.URLEncoding.EncodedLen(sessionIDBytes) {
		return false
	}
	raw, err := base64.URLEncoding.DecodeString(id)
	return err == nil && len(raw) == sessionIDBytes
}

// encodeRecordPayload returns the session's data and flash as a fresh
// JSON-shaped tree, so every ServerSessionStore holds the same shape and
// the record shares nothing with the in-memory session.
func encodeRecordPayload(data, flash map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(map[string]any{recordDataKey: data, recordFlashKey: flash})
	if err != nil {
		return nil, fmt.Errorf("velocity/auth/session: encode session: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("velocity/auth/session: encode session: %w", err)
	}
	return out, nil
}

// decodeRecordPayload returns fresh copies of the data and flash maps a
// record's Data holds. A record without a payload (written at sign-in,
// before the first save) yields empty maps.
func decodeRecordPayload(stored map[string]any) (data, flash map[string]any, err error) {
	var payload struct {
		Data  map[string]any `json:"data"`
		Flash map[string]any `json:"flash"`
	}
	if stored != nil {
		raw, err := json.Marshal(stored)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, nil, err
		}
	}
	if payload.Data == nil {
		payload.Data = make(map[string]any)
	}
	if payload.Flash == nil {
		payload.Flash = make(map[string]any)
	}
	return payload.Data, payload.Flash, nil
}

// Compile-time checks.
var (
	_ auth.SessionStore               = (*ServerStore)(nil)
	_ auth.ServerSessionStoreReceiver = (*ServerStore)(nil)
)
