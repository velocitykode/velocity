package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/buildonce"
	"github.com/velocitykode/velocity/internal/errchain"
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

// ServerSessionStore returns the record store the sessions are kept in. A
// session scheme given this store (schemes.WithSessionStore) takes it as
// its own server session store, so sign-in writes the record the session
// is then saved into.
func (s *ServerStore) ServerSessionStore() auth.ServerSessionStore {
	return s.loadRecords()
}

func (s *ServerStore) loadRecords() auth.ServerSessionStore {
	h := s.records.Load()
	if h == nil {
		return nil
	}
	return h.store
}

// Create returns a new, unsaved session. An empty id generates one.
func (s *ServerStore) Create(id string) (contract.Session, error) {
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
//     removed if the record the store holds at the removal is still past
//     it (DeleteIf: a record renewed after this read is kept); the
//     replacement reports AuthenticationExpired when the record was signed
//     in (or the store itself reported it expired);
//   - no record at all: the replacement reports RecordDeleted, which the
//     session scheme reports as auth.ErrSessionRevoked (a revocation
//     deletes the record). The record is the only authority, so one the
//     backend dropped (TTL past the policy end plus its grace, eviction,
//     a restart of an in-memory cache) reads the same way: an honest
//     browser drops the id cookie at the policy end, before the record's
//     grace runs out, so this is mostly a replayed or long-open cookie;
//     the scheme fails secure and never revives it by remember-me;
//   - a store that cannot be read: the visitor is treated as signed out.
func (s *ServerStore) Get(r *http.Request, id string) (contract.Session, error) {
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
		case errchain.Is(err, auth.ErrSessionExpired):
			fresh.(*ServerSession).authenticationExpired = true
		case errchain.Is(err, auth.ErrSessionNotFound):
			fresh.(*ServerSession).recordDeleted = true
		}
		return fresh, nil
	}

	if end := s.config.ExpiresAt(rec.CreatedAt, rec.LastSeenAt); !end.IsZero() && sessionclock.Now().After(end) {
		// The policy ended the session, so its record is retired, but not
		// on this read: the store decides again on the record it holds
		// when the removal lands (DeleteIf), so a record a request that
		// loaded it in time renewed after the read is kept. The load is
		// refused either way.
		_, _ = records.DeleteIf(ctx, id, func(meta *auth.SessionMeta) bool {
			end := s.config.ExpiresAt(meta.CreatedAt, meta.LastSeenAt)
			return !end.IsZero() && sessionclock.Now().After(end)
		})
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
	// A second, unshared copy of what the record held: Save writes only
	// what this request changed relative to it.
	session.loadedData, session.loadedFlash, _ = decodeRecordPayload(rec.Data)
	return session, nil
}

// Save writes the session to its record and sends the id cookie.
//
//   - A destroyed session deletes the cookie and removes the record it was
//     loaded from (or last saved to). The cookie deletion is written even
//     when removing the record fails; that failure is still returned.
//   - An unmodified session writes nothing.
//   - Otherwise the record takes the payload and slides to the lifetime
//     policy's end (UpdateData, update-if-present). A session saved to the
//     record it was loaded from writes only its own changes: each data and
//     flash key it set, changed or removed since the load is applied to
//     the payload the record holds when the write lands, so a request that
//     overlapped this one keeps what it wrote (a drained flash stays
//     drained, a key another request set stays set). A session id with no
//     record yet is created (Put) only for a signed-out session; a
//     signed-in session's record is written at sign-in, so a missing one
//     means it was revoked and Save fails instead of recreating it. The
//     record of an id the session rotated away from is removed first; a
//     failure to remove it fails the save.
//
// Any store failure is returned; except for a destroyed session's deletion,
// no cookie is written then.
func (s *ServerStore) Save(w http.ResponseWriter, session contract.Session) error {
	ss, ok := session.(*ServerSession)
	if !ok {
		return auth.ErrInvalidSession
	}
	records := s.loadRecords()
	if records == nil {
		return auth.ErrNoServerSessionStore
	}
	// The save is one transition of the session: it runs alone (see
	// ServerSession.turns), so it snapshots after the transition before
	// it finished and its own completion is the latest there is. Whether
	// there is anything to save is asked inside the turn too, never in
	// front of it: a save in flight has already cleared the modified mark
	// and may yet fail and put it back, so a second save that looked
	// before waiting would report a save that did not happen.
	var err error
	if turnErr := ss.turns.Do(ss.context(), func() { err = s.save(w, ss, records) }); turnErr != nil {
		return errchain.Errorf("velocity/auth/session: save session record: %w", turnErr)
	}
	return err
}

// save is Save's body, run holding the session's turn.
func (s *ServerStore) save(w http.ResponseWriter, ss *ServerSession, records auth.ServerSessionStore) error {
	if ss.IsDestroyed() {
		ss.mu.Lock()
		savedID := ss.savedID
		ss.mu.Unlock()
		// The browser's copy goes whatever happens to the record: a failed
		// removal is the server's teardown failing, and keeping the cookie
		// would leave the session usable wherever the record survived.
		http.SetCookie(w, s.config.CookiePolicy().Cookie(s.config.Name, "", -1, s.config.HttpOnly))
		if savedID != "" {
			if err := records.Delete(ss.context(), savedID); err != nil { //store-rmw-ok: destroy retires the id for good: the cookie is deleted and no request writes a record under a destroyed id again
				return errchain.Errorf("velocity/auth/session: delete session record: %w", err)
			}
			ss.mu.Lock()
			ss.savedID = ""
			ss.mu.Unlock()
		}
		return nil
	}
	if !ss.IsModified() {
		// The transition this save waited for wrote everything.
		return nil
	}
	// The modified flag is cleared here, before the snapshot, and not when
	// the write has landed: a change made while the store call runs marks
	// the session modified again and is written by the next save, where a
	// flag cleared at the end would wipe that mark and the next save would
	// skip the change. A write that fails, or panics, puts the mark back.
	ss.MarkClean()
	written := false
	// On a deferred call: a store or an encoder that panics leaves the
	// session as unsaved as one that returns an error does.
	defer func() {
		if !written {
			ss.MarkModified()
		}
	}()
	if err := s.write(w, ss, records); err != nil {
		return err
	}
	written = true
	return nil
}

// write is the save of a live, modified session, run holding its turn.
func (s *ServerStore) write(w http.ResponseWriter, ss *ServerSession, records auth.ServerSessionStore) error {
	ctx := ss.context()
	snap := ss.saveBase()
	savedID, createdAt, loadedData, loadedFlash, flashNow := snap.savedID, snap.createdAt, snap.loadedData, snap.loadedFlash, snap.flash

	id := ss.ID()
	if id == "" {
		return auth.ErrInvalidSession
	}
	now := sessionclock.Now()
	if createdAt.IsZero() {
		createdAt = now
	}
	payload, err := encodeRecordPayload(ss.GetData(), flashNow)
	if err != nil {
		return err
	}
	data, flash := payloadSections(payload)
	recordEnd := s.config.RecordExpiresAt(createdAt, now)

	// A signed-in session regenerated outside a sign-in (the scheme's
	// Login writes the new id's record itself) has no record under its
	// new id: the record moves.
	if savedID != "" && savedID != id && ss.Get(auth.UserIDSessionKey) != nil {
		if _, err := records.Get(ctx, id); errchain.Is(err, auth.ErrSessionNotFound) {
			if err := moveRecord(ctx, records, savedID, &auth.StoredSession{
				ID:         id,
				Data:       payload,
				CreatedAt:  createdAt,
				LastSeenAt: now,
				ExpiresAt:  recordEnd,
			}); err != nil {
				return errchain.Errorf("velocity/auth/session: move session record: %w", err)
			}
			s.finishSave(w, ss, snap, id, createdAt, now, data, flash)
			return nil
		}
	}

	// Retire the record of an id the session rotated away from before
	// writing under the new id, and fail closed when that is not possible:
	// a captured cookie naming the old id must not keep a live record.
	if savedID != "" && savedID != id {
		if err := records.Delete(ctx, savedID); err != nil && !errchain.Is(err, auth.ErrSessionNotFound) { //store-rmw-ok: the session left this id for a fresh one (the sign-in wrote the new record): nothing is written under the retired id again
			return errchain.Errorf("velocity/auth/session: retire previous session record: %w", err)
		}
		savedID = ""
		ss.mu.Lock()
		ss.savedID = ""
		ss.mu.Unlock()
	}

	update := func(map[string]any) (map[string]any, error) { return payload, nil }
	if savedID == id && loadedData != nil {
		update = func(current map[string]any) (map[string]any, error) {
			curData, curFlash := payloadSections(current)
			applyChanges(curData, loadedData, data)
			applyChanges(curFlash, loadedFlash, flash)
			// A flash the request set itself is written whatever the base
			// holds: set to the very value the session loaded, it is equal
			// to the base and no change to applyChanges, while the record
			// may have lost that value to another request's read since.
			for k := range snap.own {
				if v, ok := flash[k]; ok {
					curFlash[k] = v
				}
			}
			return map[string]any{recordDataKey: curData, recordFlashKey: curFlash}, nil
		}
	}
	err = records.UpdateData(ctx, id, update, now, recordEnd)
	if errchain.Is(err, auth.ErrSessionNotFound) && id != savedID && ss.Get(auth.UserIDSessionKey) == nil {
		err = records.Put(ctx, &auth.StoredSession{
			ID:         id,
			Data:       payload,
			CreatedAt:  createdAt,
			LastSeenAt: now,
			ExpiresAt:  recordEnd,
		})
	}
	if err != nil {
		return errchain.Errorf("velocity/auth/session: save session record: %w", err)
	}

	s.finishSave(w, ss, snap, id, createdAt, now, data, flash)
	return nil
}

// moveRecord gives a signed-in session's record its new id: successor (the
// payload and timestamps the caller set) takes the owner, address and user
// agent of the record under oldID, is created, and the old record is then
// retired by a conditional delete that reports whether a live record was
// still there. When it was not (the session was revoked, signed out
// everywhere or expired in between, or another request moved it first),
// the successor is removed again and the save fails with
// auth.ErrSessionNotFound: a revoked session never gets a successor, and of
// N requests moving one record exactly one keeps its successor.
func moveRecord(ctx context.Context, records auth.ServerSessionStore, oldID string, successor *auth.StoredSession) error {
	old, err := records.Get(ctx, oldID)
	if err != nil {
		return err
	}
	if old.UserID == "" {
		// A signed-out visitor's record never vouches for a signed-in
		// session: only a sign-in writes a record with its owner.
		return auth.ErrSessionNotFound
	}
	successor.UserID = old.UserID
	successor.IPAddress = old.IPAddress
	successor.UserAgent = old.UserAgent
	if err := records.Put(ctx, successor); err != nil {
		return err
	}
	retired, err := records.DeleteIf(ctx, oldID, func(*auth.SessionMeta) bool { return true })
	if err == nil && retired {
		return nil
	}
	_ = records.Delete(ctx, successor.ID) //store-rmw-ok: rolls back the successor this save just put under a fresh random id no other request holds
	if err == nil {
		err = auth.ErrSessionNotFound
	}
	return err
}

// finishSave sends the id cookie of a session just written to its record
// and adopts what was written as the session's saved state.
func (s *ServerStore) finishSave(w http.ResponseWriter, ss *ServerSession, snap saveSnapshot, id string, createdAt, now time.Time, data, flash map[string]any) {
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

	ss.mu.Lock()
	// What the snapshot held is in the record now and no longer this
	// request's alone; a flash set after the snapshot (Flash is not a
	// transition and can land while the store call runs) still is.
	for k, at := range ss.flashLocal {
		if at <= snap.seq {
			delete(ss.flashLocal, k)
		}
	}
	ss.savedID = id
	ss.loadedData, ss.loadedFlash = data, flash
	ss.createdAt = createdAt
	ss.issuedAt = now
	ss.mu.Unlock()
}

// Destroy removes the record for id.
func (s *ServerStore) Destroy(id string) error {
	records := s.loadRecords()
	if records == nil {
		return auth.ErrNoServerSessionStore
	}
	return records.Delete(context.Background(), id) //store-rmw-ok: an explicit destroy of the id by the caller, decided on no read of the record
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

	// mu guards savedID, createdAt, issuedAt, loadedData and loadedFlash:
	// a shared write (UpdateShared, a CSRF token mint, a flash read) reads
	// and updates them from whichever goroutine of the request makes it. It is never held
	// across a store call. loadedData and loadedFlash are replaced, never
	// changed in place, so a Save holding the previous maps reads them
	// unlocked.
	mu sync.Mutex

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

	// loadedData and loadedFlash are the payload as this session last
	// read or wrote it (JSON-shaped, shared with nothing), the base Save
	// measures this request's changes against; nil for a session with no
	// record yet.
	loadedData  map[string]any
	loadedFlash map[string]any

	// turns runs the session's transitions one at a time: a Save, the
	// record step of a flash read, UpdateShared, Regenerate, Clear and
	// Invalidate. Each reads the session's state, may call the record
	// store and writes the state back, and two of them overlapping around
	// a store call is how a stale base, a twice-delivered flash or a
	// refilled bag come about. A transition holds the turn from its first
	// read of the state to its last write, the adoption of what it wrote
	// included; nothing is decided in front of the turn.
	//
	// It is not a lock: nothing is held while a transition's store call
	// runs (mu is taken only around the state reads and writes inside
	// it). Every caller waits for the turn, whoever it is: a record store
	// that calls back into the session it is serving, from inside a store
	// method, asks for a turn its own caller holds and waits on itself.
	// That is unsupported and not detected (auth.ServerSessionStore says
	// so); no framework code does it.
	//
	// The invariant, with transitions serial: the base (loadedData,
	// loadedFlash) is what the session last read from or wrote to its
	// record, less the flash it consumed there; only a finishing Save, a
	// successful flash read and a successful UpdateShared replace it, and
	// none can be stale. Save writes the difference between the session
	// and the base, plus the flash keys the request set itself.
	turns buildonce.Serial

	// flashLocal names the flash keys this request set itself (Flash),
	// each with the number of its set (seq counts them): provenance is
	// recorded, never inferred from values. A key in it is consumed in
	// memory; a key in loadedFlash and not in it came from the record and
	// is consumed on the record. Flash is not a transition (it calls no
	// store), so a set can land while a Save's store call runs: the number
	// tells the finishing Save whether its snapshot included the set. A
	// key leaves flashLocal when the request consumes it, when Clear or
	// Invalidate empties the bag, and when a Save whose snapshot included
	// its set has written it (the value is the record's from then on).
	flashLocal map[string]uint64
	seq        uint64

	authenticationExpired bool
	recordDeleted         bool
}

// saveSnapshot is what one Save measures and writes against, read in one
// step under the session mutex.
type saveSnapshot struct {
	savedID     string
	createdAt   time.Time
	loadedData  map[string]any
	loadedFlash map[string]any
	// flash is the bag at the snapshot, own the keys in it the request
	// set itself, and seq the number of the last flash set before it.
	flash map[string]any
	own   map[string]struct{}
	seq   uint64
}

// saveBase takes a Save's snapshot: the base, the flash bag and the keys
// of the bag that are the request's own, as they are at one instant under
// the session mutex. The caller holds the session's turn.
func (s *ServerSession) saveBase() saveSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := saveSnapshot{
		savedID:     s.savedID,
		createdAt:   s.createdAt,
		loadedData:  s.loadedData,
		loadedFlash: s.loadedFlash,
		flash:       s.GetFlashData(),
		seq:         s.seq,
	}
	if len(s.flashLocal) > 0 {
		snap.own = make(map[string]struct{}, len(s.flashLocal))
		for k := range s.flashLocal {
			snap.own[k] = struct{}{}
		}
	}
	return snap
}

// errKeepShared aborts an UpdateShared or flash-consume write that would
// leave the record as it is: nothing is written.
var errKeepShared = errors.New("velocity/auth/session: keep the value the record holds")

// UpdateShared implements csrf/stores.SharedBag. When the session was
// saved to its record (and its id has not changed since), update runs on
// the value the record holds under key inside one UpdateData step, atomic
// against every other write to the record from every request and instance
// sharing it; a store that retries on a conflict (CacheStore) runs update
// again on the newer value, and only the attempt that is written counts.
// The value is stored JSON-shaped, as a Save stores it; a result equal to
// what the record holds writes nothing. The state the record is left in is
// then adopted by the session, as its value under key and as the base Save
// measures this request's changes against, so this request's Save neither
// writes the key again over a later change nor removes it; the session's
// other keys and flags are left as they are.
//
// A session not saved under its id yet (created, or regenerated) has no
// record another request could share: update runs on the session's own
// value, and the record is created by its Save. A session whose record is
// gone (revoked or expired) gets an error wrapping
// contract.ErrSessionRecordGone and is left as it is: the record is never
// recreated here. An error from update, or any other store failure, is
// returned and the session is left as it is.
//
// update runs under the record store's lock and must not call the store.
//
// UpdateShared is a transition of the session (see turns): the record
// write and the adoption of its result by the session run as one step, so
// a save cannot land between the two and leave the session's base naming a
// value the record no longer holds, and the adoption cannot refill a
// session a Clear or an Invalidate emptied meanwhile. A caller waiting for
// the turn returns at ctx.
func (s *ServerSession) UpdateShared(ctx context.Context, key string, update func(current any, exists bool) (next any, keep bool, err error)) (any, bool, error) {
	if ctx == nil {
		ctx = s.context()
	}
	var (
		held    any
		present bool
		err     error
	)
	if turnErr := s.turns.Do(ctx, func() { held, present, err = s.updateShared(ctx, key, update) }); turnErr != nil {
		return nil, false, errchain.Errorf("velocity/auth/session: update shared session value: %w", turnErr)
	}
	return held, present, err
}

// updateShared is UpdateShared's body, run holding the session's turn.
func (s *ServerSession) updateShared(ctx context.Context, key string, update func(current any, exists bool) (next any, keep bool, err error)) (any, bool, error) {
	id := s.ID()
	s.mu.Lock()
	persisted := s.savedID != "" && s.savedID == id && s.loadedData != nil
	createdAt := s.createdAt
	s.mu.Unlock()

	if !persisted {
		exists := s.Has(key)
		next, keep, err := update(s.Get(key), exists)
		if err != nil {
			return nil, false, err
		}
		if !keep {
			if exists {
				s.Remove(key)
			}
			return nil, false, nil
		}
		s.Put(key, next)
		return next, true, nil
	}
	records := s.store.loadRecords()
	if records == nil {
		return nil, false, auth.ErrNoServerSessionStore
	}

	var held any
	var present bool
	now := sessionclock.Now()
	err := records.UpdateData(ctx, id, func(current map[string]any) (map[string]any, error) {
		data, flash := payloadSections(current)
		cur, had := data[key]
		next, keep, err := update(cur, had)
		if err != nil {
			return nil, err
		}
		if !keep {
			held, present = nil, false
			if !had {
				return nil, errKeepShared
			}
			delete(data, key)
		} else {
			stored, err := jsonShaped(next)
			if err != nil {
				return nil, err
			}
			held, present = stored, true
			if had && reflect.DeepEqual(cur, stored) {
				return nil, errKeepShared
			}
			data[key] = stored
		}
		return map[string]any{recordDataKey: data, recordFlashKey: flash}, nil
	}, now, s.store.config.RecordExpiresAt(createdAt, now))
	switch {
	case err == nil, errchain.Is(err, errKeepShared):
	case errchain.Is(err, auth.ErrSessionNotFound), errchain.Is(err, auth.ErrSessionExpired):
		return nil, false, errchain.Errorf("velocity/auth/session: update shared session value: %w", contract.ErrSessionRecordGone)
	default:
		return nil, false, errchain.Errorf("velocity/auth/session: update shared session value: %w", err)
	}

	s.mu.Lock()
	if s.savedID == id && s.loadedData != nil {
		base := make(map[string]any, len(s.loadedData)+1)
		maps.Copy(base, s.loadedData)
		if present {
			base[key] = held
		} else {
			delete(base, key)
		}
		s.loadedData = base
	}
	s.mu.Unlock()
	if present {
		s.Put(key, held)
	} else {
		s.Remove(key)
	}
	return held, present, nil
}

// Flash sets a flash value. The value is this request's own until a save
// writes it to the record: GetFlash and FlushFlash return it from memory.
func (s *ServerSession) Flash(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if s.flashLocal == nil {
		s.flashLocal = make(map[string]uint64)
	}
	s.flashLocal[key] = s.seq
	s.BaseSession.Flash(key, value)
}

// Clear empties the session's data and flash. It is a transition of the
// session and waits for the one in progress (a flash read's record step, a
// save).
func (s *ServerSession) Clear() {
	empty := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.flashLocal = nil
		s.BaseSession.Clear()
	}
	_ = s.turns.Do(context.Background(), empty)
}

// Invalidate destroys the session (see auth.BaseSession.Invalidate). It is
// a transition of the session and waits for the one in progress, however
// long that takes: an invalidation is never given up on.
func (s *ServerSession) Invalidate() error {
	var err error
	invalidate := func() {
		s.mu.Lock()
		s.flashLocal = nil
		s.mu.Unlock()
		// Outside mu: it reads the entropy source.
		err = s.BaseSession.Invalidate()
	}
	_ = s.turns.Do(context.Background(), invalidate)
	return err
}

// persistedLocked reports whether the session is saved under its id, so
// its record is one other requests share. Caller holds s.mu.
func (s *ServerSession) persistedLocked(id string) bool {
	return s.savedID != "" && s.savedID == id && s.loadedData != nil
}

// localFlashLocked reports whether key's flash is the session's to consume
// in memory: the request set it itself, the session did not load it, or
// the session has no record another request shares. Caller holds s.mu.
func (s *ServerSession) localFlashLocked(id, key string) bool {
	if _, own := s.flashLocal[key]; own {
		return true
	}
	_, fromRecord := s.loadedFlash[key]
	return !fromRecord || !s.persistedLocked(id)
}

// GetFlash returns the flash value under key and removes it. A value the
// session loaded from its record is consumed on the record, in one
// UpdateData step: the value returned is the one the record held when the
// removal landed, so of the requests of one session that read the same
// flash at once exactly one gets it and the others get nil, on every
// instance sharing the store. A key the session did not load costs no
// store call.
//
// A value set during this request (Flash, also over a loaded key, also
// with the value the record holds) is the request's own and is consumed in
// memory, as is every flash of a session not saved under its id yet
// (created or regenerated: it has no record another request shares). When
// the store cannot run the step (the record is gone, or the store fails)
// nothing is consumed and nil is returned.
//
// Goroutines of one request sharing the session get the value once too:
// the record step is a transition of the session and runs alone (see
// turns), and the key leaves the session when the step succeeded.
func (s *ServerSession) GetFlash(key string) any {
	s.mu.Lock()
	if s.localFlashLocked(s.ID(), key) {
		delete(s.flashLocal, key)
		value := s.BaseSession.GetFlash(key)
		s.mu.Unlock()
		return value
	}
	s.mu.Unlock()

	var out any
	if err := s.turns.Do(s.context(), func() { out = s.consumeStoredFlash(key) }); err != nil {
		return nil
	}
	return out
}

// consumeStoredFlash is GetFlash's record step, run holding the session's
// turn: no save, no other flash read's record step, no Clear and no
// Invalidate of this session runs meanwhile, so the session is changed
// only after the record step succeeded and a failure leaves nothing to put
// back.
func (s *ServerSession) consumeStoredFlash(key string) any {
	id := s.ID()
	s.mu.Lock()
	if s.localFlashLocked(id, key) {
		// The transition this one waited for changed where the value
		// comes from (it consumed the key, saved, cleared or destroyed
		// the session).
		delete(s.flashLocal, key)
		value := s.BaseSession.GetFlash(key)
		s.mu.Unlock()
		return value
	}
	createdAt := s.createdAt
	s.mu.Unlock()
	if _, ok := s.GetFlashData()[key]; !ok {
		// The bag no longer holds it (the session was cleared).
		return nil
	}

	var held any
	var present bool
	if err := s.consumeFlash(id, createdAt, func(flash map[string]any) bool {
		held, present = flash[key]
		delete(flash, key)
		return present
	}); err != nil {
		return nil
	}

	s.mu.Lock()
	if s.persistedLocked(id) {
		base := make(map[string]any, len(s.loadedFlash))
		maps.Copy(base, s.loadedFlash)
		delete(base, key)
		s.loadedFlash = base
	}
	// A value the request set under the key while the step ran is its
	// own and stays in the bag.
	if _, own := s.flashLocal[key]; !own {
		s.BaseSession.GetFlash(key)
	}
	s.mu.Unlock()
	if !present {
		return nil
	}
	return held
}

// storedFlashLocked reports whether the session holds a flash it loaded
// from a record another request shares. Caller holds s.mu.
func (s *ServerSession) storedFlashLocked(id string) bool {
	if !s.persistedLocked(id) {
		return false
	}
	for k := range s.loadedFlash {
		if _, own := s.flashLocal[k]; !own {
			return true
		}
	}
	return false
}

// FlushFlash returns the whole flash bag and clears it; nil when it is
// empty. When the session holds flash it loaded from its record, the
// record's flash section is taken whole in one UpdateData step (see
// GetFlash): the result is what the record held when the removal landed,
// with the values this request set itself laid over it, so of the requests
// of one session that flush at once exactly one gets each stored value. A
// session that holds no loaded flash asks the store nothing and returns
// what this request set. A store failure consumes nothing and returns nil.
func (s *ServerSession) FlushFlash() map[string]any {
	s.mu.Lock()
	if !s.storedFlashLocked(s.ID()) {
		s.flashLocal = nil
		out := s.BaseSession.FlushFlash()
		s.mu.Unlock()
		return out
	}
	s.mu.Unlock()

	var out map[string]any
	if err := s.turns.Do(s.context(), func() { out = s.flushStoredFlash() }); err != nil {
		return nil
	}
	return out
}

// flushStoredFlash is FlushFlash's record step, run holding the session's
// turn (see consumeStoredFlash).
func (s *ServerSession) flushStoredFlash() map[string]any {
	id := s.ID()
	s.mu.Lock()
	if !s.storedFlashLocked(id) {
		s.flashLocal = nil
		out := s.BaseSession.FlushFlash()
		s.mu.Unlock()
		return out
	}
	createdAt := s.createdAt
	s.mu.Unlock()
	if len(s.GetFlashData()) == 0 {
		// The bag is empty (the session was cleared).
		return nil
	}

	var out map[string]any
	if err := s.consumeFlash(id, createdAt, func(flash map[string]any) bool {
		out = maps.Clone(flash)
		clear(flash)
		return len(out) > 0
	}); err != nil {
		return nil
	}

	s.mu.Lock()
	bag := s.BaseSession.FlushFlash()
	if out == nil {
		out = make(map[string]any, len(s.flashLocal))
	}
	// The request's own values, as the bag holds them now, lie over what
	// the record held; the bag's copies of the loaded values go with it.
	for k := range s.flashLocal {
		if v, ok := bag[k]; ok {
			out[k] = v
		}
	}
	s.flashLocal = nil
	if s.persistedLocked(id) {
		s.loadedFlash = make(map[string]any)
	}
	s.mu.Unlock()
	if len(out) == 0 {
		return nil
	}
	return out
}

// consumeFlash runs take on the flash section the record of id holds,
// inside one UpdateData step. take removes what it consumes from its
// argument and reports whether it removed anything; when it did not, the
// record is left as it is (nothing is written). A store that retries on a
// conflict runs take again on the newer section, and only the run that is
// written counts.
func (s *ServerSession) consumeFlash(id string, createdAt time.Time, take func(flash map[string]any) bool) error {
	records := s.store.loadRecords()
	if records == nil {
		return auth.ErrNoServerSessionStore
	}
	now := sessionclock.Now()
	err := records.UpdateData(s.context(), id, func(current map[string]any) (map[string]any, error) {
		data, flash := payloadSections(current)
		if !take(flash) {
			return nil, errKeepShared
		}
		return map[string]any{recordDataKey: data, recordFlashKey: flash}, nil
	}, now, s.store.config.RecordExpiresAt(createdAt, now))
	if err != nil && !errchain.Is(err, errKeepShared) {
		return err
	}
	return nil
}

// jsonShaped returns v as the JSON round trip leaves it, the shape every
// value in a record has.
func jsonShaped(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, errchain.Errorf("velocity/auth/session: encode session value: %w", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errchain.Errorf("velocity/auth/session: encode session value: %w", err)
	}
	return out, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issuedAt
}

// AuthenticationExpired reports that the request named a session the
// lifetime policy had ended and this session is its empty replacement.
func (s *ServerSession) AuthenticationExpired() bool {
	return s.authenticationExpired
}

// RecordDeleted reports that the request's cookie named a session whose
// record no longer exists (revoked, or dropped by the backend, which cannot
// be told apart) and this session is its empty replacement.
func (s *ServerSession) RecordDeleted() bool {
	return s.recordDeleted
}

// Regenerate gives the session a fresh id, keeping its data, and restarts
// its absolute lifetime. The next Save writes the data under the new id and
// removes the old id's record.
//
// Regenerate spans two ids, so it is not one step on one record. What
// requests that loaded the same session and each regenerate it get:
//
//   - Sign-ins (the session scheme's Login, LoginByID, Attempt and
//     remember-me recall) are independent: each writes the record of its
//     own unpredictable new id, so N of them leave N live sessions of the
//     user, and the old id is retired (the retirements after the first
//     find nothing, which is not an error). Knowing the old id reveals no
//     successor.
//   - A signed-out visitor's session likewise: each Save creates its own
//     record.
//   - A signed-in session regenerated on its own (no sign-in wrote a
//     record for the new id) moves its record: Save creates the successor
//     with the old record's owner and retires the old record by a
//     conditional delete. Exactly one of N such requests keeps its
//     successor; the others' Save fails with auth.ErrSessionNotFound and
//     writes no cookie, as does the Save of a session revoked in between.
//
// Each successor carries the payload as its own request saw it: a save
// under a new id writes the whole payload, not a merge, so two successors
// may both hold a flash the old record held, and neither sees what the
// other wrote. Nothing is promised about data across successors.
//
// Regenerate is a transition of the session (see turns): a save in flight
// finishes first, with the id and the creation time it snapshotted, and
// only then do the id change and the lifetime restart, so the save's
// completion cannot put the old creation time back under the new id.
func (s *ServerSession) Regenerate() error {
	var err error
	regenerate := func() {
		// Outside mu: it reads the entropy source.
		if err = s.BaseSession.Regenerate(); err != nil {
			return
		}
		s.mu.Lock()
		s.createdAt = time.Time{}
		s.mu.Unlock()
	}
	_ = s.turns.Do(context.Background(), regenerate)
	return err
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
		return nil, errchain.Errorf("velocity/auth/session: encode session: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errchain.Errorf("velocity/auth/session: encode session: %w", err)
	}
	return out, nil
}

// payloadSections returns fresh top-level copies of the data and flash
// sections of a JSON-shaped payload (empty maps for a missing section).
func payloadSections(payload map[string]any) (data, flash map[string]any) {
	section := func(key string) map[string]any {
		out := make(map[string]any)
		if m, ok := payload[key].(map[string]any); ok {
			for k, v := range m {
				out[k] = v
			}
		}
		return out
	}
	return section(recordDataKey), section(recordFlashKey)
}

// applyChanges writes into dst the keys now set or changed relative to
// base, and removes from dst the keys base had and now lacks. Keys neither
// touched are left as dst holds them. Values are JSON-shaped trees, so
// equality is structural.
func applyChanges(dst, base, now map[string]any) {
	for k, v := range now {
		if old, ok := base[k]; !ok || !reflect.DeepEqual(old, v) {
			dst[k] = v
		}
	}
	for k := range base {
		if _, ok := now[k]; !ok {
			delete(dst, k)
		}
	}
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
