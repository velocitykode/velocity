package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/sessionclock"
)

// Goroutines of one request share one session value: a stored flash is
// delivered to exactly one of them too, by GetFlash and by FlushFlash.
func TestServerSession_OneSessionValueDeliversAStoredFlashOnce(t *testing.T) {
	const readers = 8
	const rounds = 200
	for _, flush := range []bool{false, true} {
		name := "GetFlash"
		if flush {
			name = "FlushFlash"
		}
		t.Run(name, func(t *testing.T) {
			records := NewMemoryStore()
			t.Cleanup(func() { _ = records.Close(context.Background()) })
			for round := range rounds {
				store, id := flashFixture(t, records, map[string]any{"status": "saved"})
				s := loadSession(t, store, id)
				var delivered atomic.Int32
				var wg sync.WaitGroup
				start := make(chan struct{})
				for range readers {
					wg.Go(func() {
						<-start
						var got any
						if flush {
							got = s.FlushFlash()["status"]
						} else {
							got = s.GetFlash("status")
						}
						if got == "saved" {
							delivered.Add(1)
						}
					})
				}
				// A save of the same session races the reads: it must not
				// write the flash back.
				wg.Go(func() {
					<-start
					s.Put("touched", "yes")
					if err := s.Save(httptest.NewRecorder()); err != nil {
						t.Errorf("Save: %v", err)
					}
				})
				close(start)
				wg.Wait()
				if got := delivered.Load(); got != 1 {
					t.Fatalf("round %d: one session value delivered the flash %d times, want exactly 1", round, got)
				}
				if err := s.Save(httptest.NewRecorder()); err != nil {
					t.Fatalf("Save: %v", err)
				}
				if got := loadSession(t, store, id).GetFlash("status"); got != nil {
					t.Fatalf("round %d: the consumed flash came back after the saves: %v", round, got)
				}
			}
		})
	}
}

// Where a flash value comes from is recorded, not inferred from its value:
// a request that flashes the very value it loaded holds its own message,
// and gets it even when another request consumed the stored one meanwhile.
func TestServerSession_RequestFlashEqualToTheLoadedValueIsItsOwn(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			for _, flush := range []bool{false, true} {
				store, id := flashFixture(t, backend.new(t), map[string]any{"status": "saved"})
				a := loadSession(t, store, id)
				a.Flash("status", "saved")

				if got := loadSession(t, store, id).GetFlash("status"); got != "saved" {
					t.Fatalf("the other request's read = %v, want the stored flash", got)
				}

				var got any
				if flush {
					got = a.FlushFlash()["status"]
				} else {
					got = a.GetFlash("status")
				}
				if got != "saved" {
					t.Fatalf("flush=%v: the request's own flash = %v, want saved", flush, got)
				}
			}
		})
	}
}

// indexFailBackend fails the user index's extension once armed, after the
// record write it follows has landed.
type indexFailBackend struct {
	cacheBackend
	fail atomic.Bool
}

func (b *indexFailBackend) SetAddCtx(ctx context.Context, key string, ttl time.Duration, members ...string) error {
	if b.fail.Load() {
		return errors.New("test: index offline")
	}
	return b.cacheBackend.SetAddCtx(ctx, key, ttl, members...)
}

// A record write that landed is reported as landed: when only the index
// extension after it fails, UpdateData and Touch succeed (the failure is
// logged), so a flash read whose removal was written delivers the value
// instead of reporting nothing while the record no longer holds it.
func TestCacheStore_CommittedWriteSucceedsWhenTheIndexExtensionFails(t *testing.T) {
	logs := fallbacklogtest.Capture(t)
	inner := drivers.NewMemoryStore("sessions")
	t.Cleanup(func() { _ = inner.Shutdown(context.Background()) })
	backend := &indexFailBackend{cacheBackend: inner}
	records := newCacheStore(t, backend)
	ctx := context.Background()

	store, id := signedInFixture(t, records, "u1")
	seed := loadSession(t, store, id)
	seed.Flash("status", "saved")
	if err := seed.Save(httptest.NewRecorder()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	backend.fail.Store(true)
	s := loadSession(t, store, id)
	if got := s.GetFlash("status"); got != "saved" {
		t.Fatalf("GetFlash with the index offline = %v, want saved: the removal was written", got)
	}
	if err := records.Touch(ctx, id, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Touch with the index offline: %v, want nil for a record write that landed", err)
	}
	if err := s.Save(httptest.NewRecorder()); err != nil {
		t.Fatalf("Save with the index offline: %v", err)
	}
	backend.fail.Store(false)

	if got := loadSession(t, store, id).GetFlash("status"); got != nil {
		t.Fatalf("the flash was delivered and is still stored: %v", got)
	}
	if logs.Count("WARN", "velocity/auth/session: the session record was written, but its user index was not extended") == 0 {
		t.Fatalf("the index failure was not logged:\n%s", logs.String())
	}
}

// signInDuringRevokeBackend runs signIn when the user index is read: after
// DeleteAllForUser rotated the generation, before it walks the index.
type signInDuringRevokeBackend struct {
	cacheBackend
	once   sync.Once
	signIn func()
}

func (b *signInDuringRevokeBackend) SetMembersCtx(ctx context.Context, key string) ([]string, error) {
	b.once.Do(b.signIn)
	return b.cacheBackend.SetMembersCtx(ctx, key)
}

// A sign-in that lands after a sign-out-everywhere rotated the generation
// is not part of that revocation: its record and its listing stay, while
// every session issued before the rotation goes.
func TestCacheStore_DeleteAllForUserKeepsASessionIssuedAfterTheRotation(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			inner := bf.new(t).(cacheBackend)
			ctx := context.Background()
			other := newCacheStore(t, inner)
			backend := &signInDuringRevokeBackend{cacheBackend: inner}
			backend.signIn = func() {
				if err := other.Put(ctx, cacheSession("after", "u1")); err != nil {
					t.Errorf("the sign-in after the rotation: %v", err)
				}
			}
			s := newCacheStore(t, backend)
			if err := s.Put(ctx, cacheSession("before", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}

			if err := s.DeleteAllForUser(ctx, "u1"); err != nil {
				t.Fatalf("DeleteAllForUser: %v", err)
			}
			if _, err := s.Get(ctx, "before"); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("the session issued before the revocation: %v, want ErrSessionNotFound", err)
			}
			if _, err := s.Get(ctx, "after"); err != nil {
				t.Fatalf("the session issued after the rotation was removed: %v", err)
			}
			if got := listedIDs(t, s, "u1"); !got["after"] || got["before"] || len(got) != 1 {
				t.Fatalf("the user's listing = %v, want only the session issued after the rotation", got)
			}
		})
	}
}

// ListForUser drops a membership on what it read only if no record of the
// user is there when it looks again: a record renewed between the read
// that found it expired and the unlink keeps its place in the listing.
func TestCacheStore_ListForUserKeepsTheMembershipOfARenewedRecord(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			backend := bf.new(t)
			s := newCacheStore(t, backend)
			other := newCacheStore(t, backend)
			ctx := context.Background()
			if err := s.Put(ctx, cacheSession("sid", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			late := time.Now().Add(2 * time.Hour)
			var renewed atomic.Bool
			s.clock = func() time.Time {
				if renewed.CompareAndSwap(false, true) {
					rec := cacheSession("sid", "u1")
					rec.ExpiresAt = late.Add(time.Hour)
					if err := other.Put(ctx, rec); err != nil {
						t.Errorf("renewing Put: %v", err)
					}
				}
				return late
			}
			metas, err := s.ListForUser(ctx, "u1")
			if err != nil {
				t.Fatalf("ListForUser: %v", err)
			}
			if len(metas) != 0 {
				t.Fatalf("the listing reported the record it read as expired: %v", metas)
			}
			if !listedIDs(t, s, "u1")["sid"] {
				t.Fatal("the renewed record lost its place in the user's listing")
			}
		})
	}
}

// errProcessDied is what diesInIndexRemoval panics with.
var errProcessDied = errors.New("test: the process died")

// diesInIndexRemoval models a process that dies right after a member
// removal naming id reached the backend: the removal lands and nothing
// after it in that call runs.
type diesInIndexRemoval struct {
	cacheBackend
	id string
}

func (b *diesInIndexRemoval) SetRemoveCtx(ctx context.Context, key string, members ...string) error {
	err := b.cacheBackend.SetRemoveCtx(ctx, key, members...)
	for _, m := range members {
		if m == b.id {
			panic(errProcessDied)
		}
	}
	return err
}

// A record renewed between the read that found it expired and the unlink is
// live. Its membership is never taken out of the index, so there is no
// removal for the listing instance to die after: the record stays listed
// for the instances that are still up, which can revoke it by id. (Taking
// the membership out and putting it back, an instance that died between the
// two left the live record unlisted.)
func TestCacheStore_ARenewedRecordIsNeverTakenOutOfTheIndex(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			inner := bf.new(t).(cacheBackend)
			s := newCacheStore(t, &diesInIndexRemoval{cacheBackend: inner, id: "sid"})
			other := newCacheStore(t, inner)
			ctx := context.Background()
			if err := other.Put(ctx, cacheSession("sid", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			late := time.Now().Add(2 * time.Hour)
			var renewed atomic.Bool
			s.clock = func() time.Time {
				if renewed.CompareAndSwap(false, true) {
					rec := cacheSession("sid", "u1")
					rec.ExpiresAt = late.Add(time.Hour)
					if err := other.Put(ctx, rec); err != nil {
						t.Errorf("renewing Put: %v", err)
					}
				}
				return late
			}
			// The listing reads the record as expired; it was renewed after
			// that read.
			func() {
				defer func() {
					if p := recover(); p != nil && p != errProcessDied {
						panic(p)
					}
				}()
				_, _ = s.ListForUser(ctx, "u1")
			}()
			if !renewed.Load() {
				t.Fatal("premise: the record was not renewed during the listing")
			}

			if _, err := other.Get(ctx, "sid"); err != nil {
				t.Fatalf("premise: the renewed record is not live: %v", err)
			}
			if got := listedIDs(t, other, "u1"); !got["sid"] {
				t.Fatalf("the user's listing = %v: the membership of the live, renewed record was taken out, and the instance died before putting it back", got)
			}
		})
	}
}

// MemoryStore.DeleteIf holds the store mutex from its condition through
// the removal. The hook between the two tries to land a renewal: if the
// critical section had ended there, the renewal would land and the record
// would then be removed on the answer given about the idle one. The mutex
// is still held, so the renewal cannot land before the removal.
func TestMemoryStore_DeleteIfHoldsTheLockFromTheConditionThroughTheRemoval(t *testing.T) {
	s := NewMemoryStore()
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	ctx := context.Background()
	if err := s.Put(ctx, &auth.StoredSession{ID: "sid", UserID: "u1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	renewedAt := time.Now().Add(30 * time.Minute)
	idle := func(meta *auth.SessionMeta) bool { return meta.LastSeenAt.Before(renewedAt) }

	hookRan, renewed := false, false
	s.beforeConditionalRemove = func() {
		hookRan = true
		// The renewal a concurrent Touch would make, if the store let one
		// in between the condition and the removal.
		if s.mu.TryLock() {
			if rec, ok := s.byID["sid"]; ok {
				rec.LastSeenAt = renewedAt
				renewed = true
			}
			s.mu.Unlock()
		}
	}
	deleted, err := s.DeleteIf(ctx, "sid", func(meta *auth.SessionMeta) bool {
		if s.mu.TryLock() {
			s.mu.Unlock()
			t.Error("the condition ran without the store mutex")
		}
		return idle(meta)
	})
	s.beforeConditionalRemove = nil
	if err != nil {
		t.Fatalf("DeleteIf: %v", err)
	}
	if !hookRan {
		t.Fatal("the hook between the condition and the removal never ran")
	}
	if renewed {
		// The critical section ended before the removal: the renewed
		// record must then have survived, or it was removed on the old
		// answer.
		if _, getErr := s.Get(ctx, "sid"); deleted || getErr != nil {
			t.Fatalf("a renewal landed between the condition and the removal, and the renewed record was removed on the answer given about the idle one (deleted=%v, record: %v)", deleted, getErr)
		}
		t.Fatal("the store mutex was released between the condition and the removal")
	}
	if !deleted {
		t.Fatal("DeleteIf did not remove the idle record")
	}
	if err := s.Touch(ctx, "sid", renewedAt, renewedAt.Add(time.Hour)); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("a renewal after the removal = %v, want ErrSessionNotFound", err)
	}
}

// renewBeforeDeleteBackend runs renew to completion before its first
// compare-and-delete: the write another instance lands between a
// conditional delete's decision and its removal.
type renewBeforeDeleteBackend struct {
	cacheBackend
	once  sync.Once
	renew func()
}

func (b *renewBeforeDeleteBackend) CompareAndDeleteCtx(ctx context.Context, key string, expected interface{}) (bool, error) {
	b.once.Do(b.renew)
	return b.cacheBackend.CompareAndDeleteCtx(ctx, key, expected)
}

// CacheStore.DeleteIf removes only the record it asked cond about: a
// renewal that lands after cond's answer makes the removal fail, cond is
// asked again about the renewed record, and the record stays.
func TestCacheStore_DeleteIfAsksAgainAfterARenewalLandsBeforeTheRemoval(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			inner := bf.new(t).(cacheBackend)
			ctx := context.Background()
			other := newCacheStore(t, inner)
			backend := &renewBeforeDeleteBackend{cacheBackend: inner}
			s := newCacheStore(t, backend)
			if err := s.Put(ctx, cacheSession("sid", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			renewedAt := time.Now().Add(30 * time.Minute).Truncate(time.Second)
			backend.renew = func() {
				if err := other.Touch(ctx, "sid", renewedAt, renewedAt.Add(time.Hour)); err != nil {
					t.Errorf("the renewal: %v", err)
				}
			}
			asked := 0
			deleted, err := s.DeleteIf(ctx, "sid", func(meta *auth.SessionMeta) bool {
				asked++
				return meta.LastSeenAt.Before(renewedAt)
			})
			if err != nil || deleted {
				t.Fatalf("DeleteIf = %v, %v; want false, nil: the record was renewed before the removal", deleted, err)
			}
			if asked != 2 {
				t.Fatalf("cond was asked %d time(s), want 2 (the idle record, then the renewed one)", asked)
			}
			rec, err := s.Get(ctx, "sid")
			if err != nil || !rec.LastSeenAt.Equal(renewedAt) {
				t.Fatalf("the renewed record after the refused removal: %+v, %v", rec, err)
			}
			if !listedIDs(t, s, "u1")["sid"] {
				t.Fatal("the renewed record lost its place in the user's listing")
			}
		})
	}
}

var _ contract.Cache = (*indexFailBackend)(nil)

// overlapRevokeBackend runs overlap once, right after the first rotation
// of a user's generation token landed: the window in which another
// sign-out-everywhere and a sign-in after it complete while the first
// sign-out-everywhere has not walked the index yet.
type overlapRevokeBackend struct {
	cacheBackend
	once    sync.Once
	overlap func()
}

func (b *overlapRevokeBackend) ForeverCtx(ctx context.Context, key string, value interface{}) error {
	err := b.cacheBackend.ForeverCtx(ctx, key, value)
	b.once.Do(b.overlap)
	return err
}

// Three parties: A rotates the generation and pauses; B rotates it again
// and finishes; C signs in under B's token; A resumes. C's session carries
// the user's current token, not A's, and stays: A keeps every record whose
// token is the current one when it gets to it.
func TestCacheStore_OverlappingDeleteAllForUserKeepsALaterSignIn(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			inner := bf.new(t).(cacheBackend)
			ctx := context.Background()
			other := newCacheStore(t, inner)
			backend := &overlapRevokeBackend{cacheBackend: inner}
			backend.overlap = func() {
				if err := other.DeleteAllForUser(ctx, "u1"); err != nil {
					t.Errorf("the second sign-out-everywhere: %v", err)
				}
				if err := other.Put(ctx, cacheSession("after", "u1")); err != nil {
					t.Errorf("the sign-in after it: %v", err)
				}
			}
			a := newCacheStore(t, backend)
			if err := a.Put(ctx, cacheSession("before", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}

			if err := a.DeleteAllForUser(ctx, "u1"); err != nil {
				t.Fatalf("DeleteAllForUser: %v", err)
			}
			if _, err := a.Get(ctx, "before"); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("the session issued before both revocations: %v, want ErrSessionNotFound", err)
			}
			if _, err := a.Get(ctx, "after"); err != nil {
				t.Fatalf("the session issued after the later revocation was removed by the earlier one: %v", err)
			}
			if got := listedIDs(t, a, "u1"); !got["after"] || len(got) != 1 {
				t.Fatalf("the user's listing = %v, want only the later sign-in", got)
			}
		})
	}
}

// unlinkFailBackend fails the index's member removal, and its extension,
// while armed.
type unlinkFailBackend struct {
	cacheBackend
	failRemove atomic.Bool
	failAdd    atomic.Bool
	// onRemove runs once, after a member removal landed.
	onRemove func()
}

func (b *unlinkFailBackend) SetRemoveCtx(ctx context.Context, key string, members ...string) error {
	if b.failRemove.Load() {
		return errors.New("test: index offline")
	}
	err := b.cacheBackend.SetRemoveCtx(ctx, key, members...)
	if fn := b.onRemove; fn != nil {
		b.onRemove = nil
		fn()
	}
	return err
}

func (b *unlinkFailBackend) SetAddCtx(ctx context.Context, key string, ttl time.Duration, members ...string) error {
	if b.failAdd.Load() {
		return errors.New("test: index offline")
	}
	return b.cacheBackend.SetAddCtx(ctx, key, ttl, members...)
}

// Index upkeep that fails after the records were removed is said, once per
// failed step, and never changes the outcome: the removal happened.
func TestCacheStore_FailedIndexUpkeepIsLoggedAndTheRemovalStands(t *testing.T) {
	logs := fallbacklogtest.Capture(t)
	inner := drivers.NewMemoryStore("sessions")
	t.Cleanup(func() { _ = inner.Shutdown(context.Background()) })
	backend := &unlinkFailBackend{cacheBackend: inner}
	s := newCacheStore(t, backend)
	other := newCacheStore(t, inner)
	ctx := context.Background()

	// The member removal fails during a sign-out-everywhere.
	if err := s.Put(ctx, cacheSession("one", "u1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	backend.failRemove.Store(true)
	if err := s.DeleteAllForUser(ctx, "u1"); err != nil {
		t.Fatalf("DeleteAllForUser with the index offline = %v, want nil: the records were removed", err)
	}
	backend.failRemove.Store(false)
	if _, err := s.Get(ctx, "one"); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("the revoked session: %v, want ErrSessionNotFound", err)
	}
	const removeLine = "velocity/auth/session: session records were removed, but their ids were not taken out of the user index"
	if n := logs.Count("WARN", removeLine); n != 1 {
		t.Fatalf("the failed member removal was logged %d times, want once:\n%s", n, logs.String())
	}

	// The repair of a membership fails: a record put under the id as its
	// membership is removed, after the unlink looked and found none.
	if err := s.Put(ctx, cacheSession("two", "u1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	backend.onRemove = func() {
		if err := other.Put(ctx, cacheSession("two", "u1")); err != nil {
			t.Errorf("the Put during the removal: %v", err)
		}
		backend.failAdd.Store(true)
	}
	if err := s.Delete(ctx, "two"); err != nil {
		t.Fatalf("Delete with the index offline: %v", err)
	}
	backend.failAdd.Store(false)
	const repairLine = "velocity/auth/session: a session record written during a removal was not put back into the user index"
	if n := logs.Count("WARN", repairLine); n != 1 {
		t.Fatalf("the failed membership repair was logged %d times, want once:\n%s", n, logs.String())
	}
}

// hookedRecords runs before once, ahead of its first UpdateData, and can
// fail every UpdateData after running a hook.
type hookedRecords struct {
	auth.ServerSessionStore
	ran      atomic.Bool
	before   func()
	failWith func() error
}

func (h *hookedRecords) UpdateData(ctx context.Context, id string, update func(map[string]any) (map[string]any, error), lastSeen, expiresAt time.Time) error {
	// before makes store calls of its own, which come back through here:
	// it runs on the first call only and must not wait on itself.
	if h.before != nil && h.ran.CompareAndSwap(false, true) {
		h.before()
	}
	if h.failWith != nil {
		if err := h.failWith(); err != nil {
			return err
		}
	}
	return h.ServerSessionStore.UpdateData(ctx, id, update, lastSeen, expiresAt)
}

// pausingRecords holds its first UpdateData, once armed, until release is
// closed: the store call of one transition, with the test in control of
// what else happens to the session meanwhile. entered is closed when the
// held call arrives. With fail set the held call then fails.
type pausingRecords struct {
	auth.ServerSessionStore
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	fail    bool
	// after holds the call once the record write has landed, before it
	// returns to the session: the window between a transition's write and
	// its adoption of what it wrote.
	after bool
	// panics makes the held call panic instead of returning.
	panics bool
}

func newPausingRecords(inner auth.ServerSessionStore) *pausingRecords {
	return &pausingRecords{ServerSessionStore: inner, entered: make(chan struct{}), release: make(chan struct{})}
}

func (p *pausingRecords) UpdateData(ctx context.Context, id string, update func(map[string]any) (map[string]any, error), lastSeen, expiresAt time.Time) error {
	if !p.armed.CompareAndSwap(true, false) {
		return p.ServerSessionStore.UpdateData(ctx, id, update, lastSeen, expiresAt)
	}
	if p.after {
		err := p.ServerSessionStore.UpdateData(ctx, id, update, lastSeen, expiresAt)
		close(p.entered)
		<-p.release
		return err
	}
	close(p.entered)
	<-p.release
	if p.panics {
		panic("test: the record store panicked")
	}
	if p.fail {
		return errRecordsOffline
	}
	return p.ServerSessionStore.UpdateData(ctx, id, update, lastSeen, expiresAt)
}

// transitionSettled reports that a transition started on another goroutine
// while one is held in its store call has gone as far as it can: it
// finished, or it is waiting for the session's turn.
func transitionSettled(s *ServerSession, done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
	}
	return s.turns.Waiting() > 0
}

// overlap runs first on its own goroutine, waits until it is held in its
// store call, runs second on another goroutine until it finished or waits
// for the session's turn, then lets first's store call go and waits for
// both.
func overlap(t *testing.T, s *ServerSession, records *pausingRecords, first, second func()) {
	t.Helper()
	records.armed.Store(true)
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(firstDone); first() }()
	hostile.Within(t, hostile.Deadline, func() { <-records.entered })
	go func() { defer close(secondDone); second() }()
	hostile.Eventually(t, hostile.Deadline, "the second transition finishing or waiting for the session's turn", func() bool {
		return transitionSettled(s, secondDone)
	})
	close(records.release)
	hostile.Within(t, hostile.Deadline, func() { <-firstDone; <-secondDone })
}

// pausedFixture loads the session id names through a store whose record
// store can hold a store call.
func pausedFixture(t *testing.T, inner auth.ServerSessionStore, id string) (*ServerStore, *pausingRecords, *ServerSession) {
	t.Helper()
	records := newPausingRecords(inner)
	store, err := NewServerStore(testConfig(), records)
	if err != nil {
		t.Fatalf("NewServerStore: %v", err)
	}
	return store, records, loadSession(t, store, id).(*ServerSession)
}

// A save held in its store call, and on another goroutine a flash read and
// a second save of the same session value. The transitions run one after
// the other, so the first save's snapshot is never made the base again
// after the flash was consumed: the session's next save does not take the
// key's absence from its bag for a pending delete and remove a flash
// another request set meanwhile.
func TestServerSession_OverlappingSavesKeepAFlashSetByAnotherRequest(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			inner := backend.new(t)
			_, id := flashFixture(t, inner, map[string]any{"status": "saved"})
			store, records, s := pausedFixture(t, inner, id)

			var read any
			s.Put("a", "first save")
			overlap(t, s, records, func() {
				if err := s.Save(httptest.NewRecorder()); err != nil {
					t.Errorf("save A: %v", err)
				}
			}, func() {
				read = s.GetFlash("status")
				s.Put("b", "second save")
				if err := s.Save(httptest.NewRecorder()); err != nil {
					t.Errorf("save B: %v", err)
				}
			})
			if read != "saved" {
				t.Fatalf("GetFlash beside save A = %v, want saved", read)
			}

			// Another request sets a fresh flash under the key.
			other := loadSession(t, store, id)
			other.Flash("status", "fresh")
			if err := other.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save other: %v", err)
			}

			// The first session saves once more; it holds no flash.
			s.Put("c", "third save")
			if err := s.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("third save: %v", err)
			}
			if got := loadSession(t, store, id).GetFlash("status"); got != "fresh" {
				t.Fatalf("the other request's flash after the session saved again = %v, want fresh", got)
			}
		})
	}
}

// A save held in its store call with n=1, and on another goroutine the
// session moves to n=2 and saves. Whatever order the two writes land in,
// the session and its record agree afterwards: the next save leaves the
// record at the session's value.
func TestServerSession_OverlappingSavesLeaveTheRecordAtTheSessionsData(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			inner := backend.new(t)
			_, id := flashFixture(t, inner, nil)
			store, records, s := pausedFixture(t, inner, id)

			s.Put("n", 1)
			overlap(t, s, records, func() {
				if err := s.Save(httptest.NewRecorder()); err != nil {
					t.Errorf("save A: %v", err)
				}
			}, func() {
				s.Put("n", 2)
				if err := s.Save(httptest.NewRecorder()); err != nil {
					t.Errorf("save B: %v", err)
				}
			})

			s.Put("other", "changed")
			if err := s.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("save C: %v", err)
			}
			if got := loadSession(t, store, id).Get("n"); got != float64(2) {
				t.Fatalf("the record's n after the saves = %v, want the session's 2", got)
			}
		})
	}
}

// A save held in its store call with the request's own flash "saved", and
// on another goroutine the request flashes "fresh" and saves. The later
// value is what the record holds afterwards: the first save's own-key
// write does not land over it.
func TestServerSession_OverlappingSavesKeepTheLaterOwnFlash(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			inner := backend.new(t)
			_, id := flashFixture(t, inner, map[string]any{"status": "saved"})
			store, records, s := pausedFixture(t, inner, id)

			s.Flash("status", "saved")
			overlap(t, s, records, func() {
				if err := s.Save(httptest.NewRecorder()); err != nil {
					t.Errorf("save A: %v", err)
				}
			}, func() {
				s.Flash("status", "fresh")
				if err := s.Save(httptest.NewRecorder()); err != nil {
					t.Errorf("save B: %v", err)
				}
			})
			if got := loadSession(t, store, id).GetFlash("status"); got != "fresh" {
				t.Fatalf("the stored flash after both saves = %v, want the later fresh", got)
			}
		})
	}
}

// A flash read held in a record step that then fails, and on another
// goroutine an Invalidate (or a Clear) of the same session value. The
// failed read puts nothing back into the session the other transition
// emptied, in whichever order they ran.
func TestServerSession_FailedFlashReadBesideClearOrInvalidate(t *testing.T) {
	ends := map[string]func(s *ServerSession){
		"Clear":      func(s *ServerSession) { s.Clear() },
		"Invalidate": func(s *ServerSession) { _ = s.Invalidate() },
	}
	for name, end := range ends {
		for _, flush := range []bool{false, true} {
			inner := NewMemoryStore()
			t.Cleanup(func() { _ = inner.Close(context.Background()) })
			_, id := flashFixture(t, inner, map[string]any{"status": "saved"})
			_, records, s := pausedFixture(t, inner, id)
			records.fail = true
			s.Flash("own", "mine")

			overlap(t, s, records, func() {
				if flush {
					if out := s.FlushFlash(); out != nil {
						t.Errorf("%s: FlushFlash with the store offline = %v, want nil", name, out)
					}
				} else if got := s.GetFlash("status"); got != nil {
					t.Errorf("%s: GetFlash with the store offline = %v, want nil", name, got)
				}
			}, func() { end(s) })
			if bag := s.GetFlashData(); len(bag) != 0 {
				t.Fatalf("%s (flush=%v): the emptied bag holds %v after the failed read", name, flush, bag)
			}
		}
	}
}

// An Invalidate racing a flash read whose record step fails at once:
// however the two interleave, the destroyed session's bag stays empty. A
// stress test: the window it guards (a read that starts between the two
// halves of an invalidation and puts its value back after the second) has
// no hook, and a read that restores after the session was destroyed
// refilled the bag within a few hundred rounds before the transitions ran
// one at a time.
func TestServerSession_InvalidateRacingFailedFlashReadsLeavesTheBagEmpty(t *testing.T) {
	const rounds = 2000
	inner := NewMemoryStore()
	t.Cleanup(func() { _ = inner.Close(context.Background()) })
	for round := range rounds {
		_, id := flashFixture(t, inner, map[string]any{"status": "saved"})
		records := &hookedRecords{ServerSessionStore: inner}
		store, err := NewServerStore(testConfig(), records)
		if err != nil {
			t.Fatalf("NewServerStore: %v", err)
		}
		s := loadSession(t, store, id).(*ServerSession)
		records.failWith = func() error { return errRecordsOffline }
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Go(func() {
			<-start
			_ = s.Invalidate()
		})
		wg.Go(func() {
			<-start
			if got := s.GetFlash("status"); got != nil {
				t.Errorf("round %d: GetFlash with the store offline = %v, want nil", round, got)
			}
		})
		close(start)
		hostile.Within(t, hostile.Deadline, wg.Wait)
		if !s.IsDestroyed() {
			t.Fatalf("round %d: the session is not destroyed", round)
		}
		if bag := s.GetFlashData(); len(bag) != 0 {
			t.Fatalf("round %d: the destroyed session's bag was refilled by the failed read: %v", round, bag)
		}
		if err := inner.Delete(context.Background(), id); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
}

// A request flashes the very value it loaded, another request consumes
// the stored one, and the first request saves: the save writes the
// request's own flash although it equals the base, so the message is
// there for the next read.
func TestServerSession_SaveWritesARequestFlashEqualToTheLoadedValue(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, id := flashFixture(t, backend.new(t), map[string]any{"status": "saved"})
			a := loadSession(t, store, id)
			a.Flash("status", "saved")

			if got := loadSession(t, store, id).GetFlash("status"); got != "saved" {
				t.Fatalf("the other request's read = %v, want the stored flash", got)
			}
			if err := a.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if got := loadSession(t, store, id).GetFlash("status"); got != "saved" {
				t.Fatalf("the flash the request set and saved = %v, want saved", got)
			}
			// Written once: the saving session reads nothing more.
			if got := a.GetFlash("status"); got != nil {
				t.Fatalf("the saving session read the delivered flash again: %v", got)
			}
		})
	}
}

// A shared write (the csrf mint's UpdateShared) held between its record
// write and its adoption by the session, and on another goroutine a save
// of the same session value. The shared write is one transition, adoption
// included, so the save cannot land in between and leave the session's
// base naming a value the record no longer holds: afterwards the session
// and its record agree.
func TestServerSession_UpdateSharedAdoptsBeforeASaveRuns(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			inner := backend.new(t)
			_, id := flashFixture(t, inner, nil)
			store, records, s := pausedFixture(t, inner, id)
			records.after = true

			s.Put("n", 1)
			overlap(t, s, records, func() {
				if _, _, err := s.UpdateShared(context.Background(), "n", func(any, bool) (any, bool, error) { return 2, true, nil }); err != nil {
					t.Errorf("UpdateShared: %v", err)
				}
			}, func() {
				if err := s.Save(httptest.NewRecorder()); err != nil {
					t.Errorf("Save: %v", err)
				}
			})

			s.Put("other", "changed")
			if err := s.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("the next save: %v", err)
			}
			if local, stored := s.Get("n"), loadSession(t, store, id).Get("n"); local != stored {
				t.Fatalf("after the shared write and the saves the session holds n=%v and its record n=%v", local, stored)
			}
		})
	}
}

// A save whose record store panics is as unsaved as one whose store
// returns an error: the panic reaches the caller, the turn is free, and
// the same save tried again, with no change in between, writes.
func TestServerSession_SaveThatPanicsStaysModified(t *testing.T) {
	inner := NewMemoryStore()
	t.Cleanup(func() { _ = inner.Close(context.Background()) })
	_, id := flashFixture(t, inner, nil)
	store, records, s := pausedFixture(t, inner, id)
	records.panics = true
	close(records.release)
	records.armed.Store(true)

	s.Put("draft", "hello")
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the store's panic did not reach Save's caller")
			}
		}()
		_ = s.Save(httptest.NewRecorder())
	}()
	if !s.IsModified() {
		t.Fatal("a save that panicked left the session marked saved")
	}
	hostile.Within(t, hostile.Deadline, func() {
		if err := s.Save(httptest.NewRecorder()); err != nil {
			t.Errorf("the retried save: %v", err)
		}
	})
	if got := loadSession(t, store, id).Get("draft"); got != "hello" {
		t.Fatalf("the record after the retried save holds draft=%v, want hello", got)
	}
}

// A save held in its store call that then fails, and on another goroutine
// a second save of the same session value with no change of its own. The
// second save does not look at the modified mark in front of the turn (the
// first has cleared it and will put it back): it waits, finds the session
// unsaved and saves it.
func TestServerSession_SecondSaveWaitsForTheFirstAndSavesWhatItLeft(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			inner := backend.new(t)
			_, id := flashFixture(t, inner, nil)
			store, records, s := pausedFixture(t, inner, id)
			records.fail = true

			var first, second error
			s.Put("draft", "hello")
			overlap(t, s, records, func() {
				first = s.Save(httptest.NewRecorder())
			}, func() {
				second = s.Save(httptest.NewRecorder())
			})
			if !errors.Is(first, errRecordsOffline) {
				t.Fatalf("the first save = %v, want the store's failure", first)
			}
			if second != nil {
				t.Fatalf("the second save: %v", second)
			}
			if got := loadSession(t, store, id).Get("draft"); got != "hello" {
				t.Fatalf("the second save reported success and the record holds draft=%v, want hello", got)
			}
		})
	}
}

// A save held in its store call, and on another goroutine a Regenerate of
// the same session value. The regenerate waits for the save, so the save's
// completion cannot put the old creation time back: the successor's record
// starts its absolute lifetime when it is created.
func TestServerSession_RegenerateBesideASaveRestartsTheLifetime(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			now := time.Now().Truncate(time.Second)
			defer sessionclock.Set(func() time.Time { return now })()
			inner := backend.new(t)
			_, id := flashFixture(t, inner, nil)
			created := now
			now = now.Add(10 * time.Minute)
			_, records, s := pausedFixture(t, inner, id)

			s.Put("a", "first save")
			overlap(t, s, records, func() {
				if err := s.Save(httptest.NewRecorder()); err != nil {
					t.Errorf("Save: %v", err)
				}
			}, func() {
				if err := s.Regenerate(); err != nil {
					t.Errorf("Regenerate: %v", err)
				}
			})

			now = now.Add(10 * time.Minute)
			s.Put("b", "save under the new id")
			if err := s.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("the save under the new id: %v", err)
			}
			rec, err := inner.Get(context.Background(), s.ID())
			if err != nil {
				t.Fatalf("the successor record: %v", err)
			}
			if rec.CreatedAt.Equal(created) || !rec.CreatedAt.Equal(now) {
				t.Fatalf("the successor's CreatedAt = %v, want the time of its creation %v, not the old session's %v", rec.CreatedAt, now, created)
			}
		})
	}
}

// Session X is held in a save's store call on one goroutine. On another, a
// save of session Y calls, from inside Y's store, Invalidate on X. That
// goroutine is inside a transition, but not X's: it waits for X's turn
// like anyone else, so the invalidation runs after X's save finished and
// X's save cannot issue a live cookie once Invalidate has returned.
func TestServerSession_InvalidateFromAnotherSessionsTransitionWaits(t *testing.T) {
	inner := NewMemoryStore()
	t.Cleanup(func() { _ = inner.Close(context.Background()) })
	_, xID := flashFixture(t, inner, nil)
	_, yID := flashFixture(t, inner, nil)
	_, xRecords, x := pausedFixture(t, inner, xID)

	yRecords := &hookedRecords{ServerSessionStore: inner}
	yStore, err := NewServerStore(testConfig(), yRecords)
	if err != nil {
		t.Fatalf("NewServerStore: %v", err)
	}
	y := loadSession(t, yStore, yID)

	// x's save writes its cookie inside its turn, so the writer says, to an
	// Invalidate that has just returned, whether the save had got that far.
	// A flag set once Save has returned would not: the turn is released
	// inside Save, and the invalidation can take it before the saving
	// goroutine runs again.
	xWriter := &cookieSeenWriter{ResponseRecorder: httptest.NewRecorder()}
	var invalidatedBeforeXSaved atomic.Bool
	invalidated := make(chan struct{})
	yRecords.before = func() {
		_ = x.Invalidate()
		if !xWriter.wrote.Load() {
			invalidatedBeforeXSaved.Store(true)
		}
		close(invalidated)
	}

	xRecords.armed.Store(true)
	xDone, yDone := make(chan struct{}), make(chan struct{})
	x.Put("a", "x's save")
	go func() {
		defer close(xDone)
		if err := x.Save(xWriter); err != nil {
			t.Errorf("x's save: %v", err)
		}
	}()
	hostile.Within(t, hostile.Deadline, func() { <-xRecords.entered })
	y.Put("b", "y's save")
	go func() {
		defer close(yDone)
		if err := y.Save(httptest.NewRecorder()); err != nil {
			t.Errorf("y's save: %v", err)
		}
	}()
	hostile.Eventually(t, hostile.Deadline, "x's invalidation finishing or waiting for x's turn", func() bool {
		return transitionSettled(x, invalidated)
	})
	close(xRecords.release)
	hostile.Within(t, hostile.Deadline, func() { <-xDone; <-yDone })

	if invalidatedBeforeXSaved.Load() {
		t.Fatal("Invalidate returned while x's save was still in its store call: the save then issued x's cookie for a session already invalidated")
	}
	if !x.IsDestroyed() {
		t.Fatal("x is not destroyed")
	}
	if len(xWriter.Result().Cookies()) != 1 {
		t.Fatalf("x's save wrote %d cookies, want the one it issued inside its turn", len(xWriter.Result().Cookies()))
	}
}

// cookieSeenWriter records that the save it was handed reached its cookie
// write: http.SetCookie asks the writer for its headers to add the cookie.
type cookieSeenWriter struct {
	*httptest.ResponseRecorder
	wrote atomic.Bool
}

func (w *cookieSeenWriter) Header() http.Header {
	w.wrote.Store(true)
	return w.ResponseRecorder.Header()
}
