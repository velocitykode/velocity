package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache/drivers"
	cacheredis "github.com/velocitykode/velocity/cache/redis"
	"github.com/velocitykode/velocity/contract"
)

// backendFactory returns a shared cache backend plus a cleanup. Every
// CacheStore built over the same backend models one app instance in a
// fleet sharing one Redis.
type backendFactory struct {
	name string
	new  func(t *testing.T) contract.Cache
}

func sharedBackends() []backendFactory {
	return []backendFactory{
		{name: "memory", new: func(t *testing.T) contract.Cache {
			b := drivers.NewMemoryStore("sessions")
			t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
			return b
		}},
		{name: "redis", new: func(t *testing.T) contract.Cache {
			mr := miniredis.RunT(t)
			b, err := cacheredis.NewRedisStore(context.Background(), "sessions", mr.Host(), mr.Server().Addr().Port, "", 0, false)
			if err != nil {
				t.Fatalf("NewRedisStore: %v", err)
			}
			t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
			return b
		}},
	}
}

func newCacheStore(t *testing.T, backend contract.Cache) *CacheStore {
	t.Helper()
	s, err := NewCacheStore(backend)
	if err != nil {
		t.Fatalf("NewCacheStore: %v", err)
	}
	return s
}

func cacheSession(id, userID string) *auth.StoredSession {
	now := time.Now()
	return &auth.StoredSession{
		ID:         id,
		UserID:     userID,
		Data:       map[string]any{"k": "v"},
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(time.Hour),
		IPAddress:  "127.0.0.1",
		UserAgent:  "test/1.0",
	}
}

func listedIDs(t *testing.T, s *CacheStore, userID string) map[string]bool {
	t.Helper()
	list, err := s.ListForUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	out := make(map[string]bool, len(list))
	for _, m := range list {
		out[m.ID] = true
	}
	return out
}

func TestNewCacheStore_NilBackend(t *testing.T) {
	if _, err := NewCacheStore(nil); !errors.Is(err, ErrCacheStoreNilBackend) {
		t.Fatalf("expected ErrCacheStoreNilBackend, got %v", err)
	}
}

// TestNewCacheStore_UnsupportedBackend proves a backend without the
// replace/set capabilities is refused at construction: the file cache
// driver implements neither.
func TestNewCacheStore_UnsupportedBackend(t *testing.T) {
	fs, err := drivers.NewFileStore("sessions", t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Shutdown(context.Background()) })
	if _, err := NewCacheStore(fs); !errors.Is(err, ErrCacheStoreUnsupported) {
		t.Fatalf("expected ErrCacheStoreUnsupported, got %v", err)
	}
}

// TestCacheStore_TwoInstances_OverlappingOperations runs login, touch,
// delete and revoke-all from two store instances over one backend, the
// way two app replicas share one Redis, and checks the invariants the
// ticket names: every issued session is listed, every pre-revocation
// session is rejected afterwards, no update is lost.
func TestCacheStore_TwoInstances_OverlappingOperations(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			backend := bf.new(t)
			a := newCacheStore(t, backend)
			b := newCacheStore(t, backend)
			ctx := context.Background()
			const user = "u1"
			const n = 40

			// Phase 1: concurrent logins on both instances, interleaved
			// with touches and single deletes of every other session.
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					inst := a
					if i%2 == 1 {
						inst = b
					}
					id := fmt.Sprintf("s-%d", i)
					if err := inst.Put(ctx, cacheSession(id, user)); err != nil {
						t.Errorf("Put %s: %v", id, err)
						return
					}
					other := a
					if inst == a {
						other = b
					}
					if err := other.Touch(ctx, id, time.Now()); err != nil {
						t.Errorf("Touch %s from other instance: %v", id, err)
					}
					if i%4 == 0 {
						if err := other.Delete(ctx, id); err != nil {
							t.Errorf("Delete %s: %v", id, err)
						}
					}
				}(i)
			}
			wg.Wait()

			// Every issued-and-not-deleted session is listed from both
			// instances; every deleted one is gone from both.
			for _, inst := range []*CacheStore{a, b} {
				got := listedIDs(t, inst, user)
				for i := 0; i < n; i++ {
					id := fmt.Sprintf("s-%d", i)
					deleted := i%4 == 0
					if deleted && got[id] {
						t.Errorf("deleted %s still listed", id)
					}
					if !deleted && !got[id] {
						t.Errorf("issued %s missing from listing", id)
					}
					if _, err := inst.Get(ctx, id); deleted != errors.Is(err, auth.ErrSessionNotFound) {
						t.Errorf("Get %s: deleted=%v err=%v", id, deleted, err)
					}
				}
			}

			// Phase 2: revoke-all on instance A while instance B keeps
			// touching and logging in. Every session issued before the
			// revoke must be rejected on both instances afterwards; a
			// login issued after it must be live and listed.
			preRevoke := make([]string, 0, n)
			for i := 0; i < n; i++ {
				if i%4 != 0 {
					preRevoke = append(preRevoke, fmt.Sprintf("s-%d", i))
				}
			}
			revoked := make(chan struct{})
			wg.Add(1)
			go func() {
				defer wg.Done()
				for _, id := range preRevoke {
					// Outcome is either a successful touch (before the
					// revoke) or not-found (after); never an error and
					// never a resurrection.
					if err := b.Touch(ctx, id, time.Now()); err != nil && !errors.Is(err, auth.ErrSessionNotFound) {
						t.Errorf("Touch %s during revoke: %v", id, err)
					}
				}
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := a.DeleteAllForUser(ctx, user); err != nil {
					t.Errorf("DeleteAllForUser: %v", err)
				}
				close(revoked)
			}()
			wg.Wait()
			<-revoked

			for _, inst := range []*CacheStore{a, b} {
				for _, id := range preRevoke {
					if _, err := inst.Get(ctx, id); !errors.Is(err, auth.ErrSessionNotFound) {
						t.Errorf("pre-revocation %s still accepted: %v", id, err)
					}
					if err := inst.Touch(ctx, id, time.Now()); !errors.Is(err, auth.ErrSessionNotFound) {
						t.Errorf("pre-revocation %s touchable: %v", id, err)
					}
				}
			}
			if got := listedIDs(t, a, user); len(got) != 0 {
				t.Fatalf("listing after revoke-all: %v", got)
			}

			if err := b.Put(ctx, cacheSession("post", user)); err != nil {
				t.Fatalf("Put after revoke: %v", err)
			}
			if _, err := a.Get(ctx, "post"); err != nil {
				t.Fatalf("post-revocation login rejected on the other instance: %v", err)
			}
			if got := listedIDs(t, a, user); !got["post"] || len(got) != 1 {
				t.Fatalf("listing after new login = %v, want only post", got)
			}
		})
	}
}

// TestCacheStore_RevokeAllAuthoritativeWithIncompleteIndex drops a session
// from the user's index behind the store's back, then revokes all. The
// generation check on Get must still reject it: the index is a listing
// aid, not the source of truth for revocation.
func TestCacheStore_RevokeAllAuthoritativeWithIncompleteIndex(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			backend := bf.new(t)
			s := newCacheStore(t, backend)
			ctx := context.Background()
			if err := s.Put(ctx, cacheSession("orphan", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			sets := backend.(contract.CacheSetStore)
			if err := sets.SetRemoveCtx(ctx, cacheUserKey("u1"), "orphan"); err != nil {
				t.Fatalf("SetRemoveCtx: %v", err)
			}
			if _, err := s.Get(ctx, "orphan"); err != nil {
				t.Fatalf("orphan must still be live before revoke: %v", err)
			}
			if err := s.DeleteAllForUser(ctx, "u1"); err != nil {
				t.Fatalf("DeleteAllForUser: %v", err)
			}
			if _, err := s.Get(ctx, "orphan"); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("orphan survived revoke-all: %v", err)
			}
			if err := s.Touch(ctx, "orphan", time.Now()); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("orphan touchable after revoke-all: %v", err)
			}
			// The stale meta key was evicted by the rejecting Get.
			if _, ok := backend.GetStringCtx(ctx, cacheMetaKey("orphan")); ok {
				t.Fatal("rejected record left in the backend")
			}
		})
	}
}

// genReadFailBackend makes reads of a user's generation key fail (the
// contract read surface reports a backend error the same way as an absent
// key), modelling a Redis error on the generation lookup.
type genReadFailBackend struct {
	cacheBackend
	fail   *atomic.Bool
	genKey string
}

func (b *genReadFailBackend) GetStringCtx(ctx context.Context, key string) (string, bool) {
	if key == b.genKey && b.fail.Load() {
		return "", false
	}
	return b.cacheBackend.GetStringCtx(ctx, key)
}

// TestCacheStore_GenerationUnreadable_FailsClosed reproduces the review
// finding: with the generation key unreadable, a session must be rejected,
// never accepted as "created before the first revocation". The index is
// left incomplete so the generation check is the only line of defence.
func TestCacheStore_GenerationUnreadable_FailsClosed(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			backend := bf.new(t)
			issuer := newCacheStore(t, backend)
			ctx := context.Background()
			if err := issuer.Put(ctx, cacheSession("s1", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			_ = backend.(contract.CacheSetStore).SetRemoveCtx(ctx, cacheUserKey("u1"), "s1")

			fail := &atomic.Bool{}
			second := newCacheStore(t, backend)
			second.backend = &genReadFailBackend{cacheBackend: second.backend, fail: fail, genKey: cacheGenKey("u1")}

			if _, err := second.Get(ctx, "s1"); err != nil {
				t.Fatalf("live session rejected while the generation is readable: %v", err)
			}
			fail.Store(true)
			if _, err := second.Get(ctx, "s1"); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("unreadable generation accepted a session: %v", err)
			}
			if err := second.Touch(ctx, "s1", time.Now()); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("unreadable generation allowed Touch: %v", err)
			}
			// A login cannot be recorded under a token nobody can verify.
			if err := second.Put(ctx, cacheSession("s2", "u1")); err == nil {
				t.Fatal("Put succeeded with an unreadable generation")
			}
		})
	}
}

// TestCacheStore_ShortLivedLoginKeepsIndexAlive reproduces the review
// finding: a two-hour session followed by a two-second one must still be
// listed after the short one expires. Redis only, so the clock can be
// advanced past the index TTL.
func TestCacheStore_ShortLivedLoginKeepsIndexAlive(t *testing.T) {
	mr := miniredis.RunT(t)
	backend, err := cacheredis.NewRedisStore(context.Background(), "sessions", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })
	s := newCacheStore(t, backend)
	ctx := context.Background()

	long := cacheSession("long", "u1")
	long.ExpiresAt = time.Now().Add(2 * time.Hour)
	short := cacheSession("short", "u1")
	short.ExpiresAt = time.Now().Add(2 * time.Second)
	if err := s.Put(ctx, long); err != nil {
		t.Fatalf("Put long: %v", err)
	}
	if err := s.Put(ctx, short); err != nil {
		t.Fatalf("Put short: %v", err)
	}

	// Past the short session and the slack the short login would have
	// set on the index, but well inside the long session's life.
	skip := 2*time.Second + userIndexSlack + 5*time.Minute
	mr.FastForward(skip)
	s.clock = func() time.Time { return time.Now().Add(skip) }

	if _, err := s.Get(ctx, "long"); err != nil {
		t.Fatalf("long session not live: %v", err)
	}
	got := listedIDs(t, s, "u1")
	if !got["long"] {
		t.Fatalf("long session dropped from the listing after the short one expired: %v", got)
	}
	if got["short"] {
		t.Fatalf("expired short session still listed: %v", got)
	}

	// A non-expiring session pins the index forever.
	forever := cacheSession("forever", "u2")
	forever.ExpiresAt = time.Time{}
	_ = s.Put(ctx, forever)
	brief := cacheSession("brief", "u2")
	brief.ExpiresAt = s.clock().Add(2 * time.Second)
	_ = s.Put(ctx, brief)
	mr.FastForward(24 * time.Hour)
	s.clock = func() time.Time { return time.Now().Add(skip + 24*time.Hour) }
	if got := listedIDs(t, s, "u2"); !got["forever"] || got["brief"] {
		t.Fatalf("listing for u2 = %v, want only forever", got)
	}
}

// TestCacheStore_LoginOverlapsRevokeAll runs logins on one instance in
// three waves around a revoke-all on the other: wave 1 completes before
// the revoke starts, wave 2 runs concurrently with it, wave 3 starts after
// it finishes. Wave 1 must be rejected, wave 3 must be live and listed,
// and for every session of every wave Get and ListForUser must agree.
func TestCacheStore_LoginOverlapsRevokeAll(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			backend := bf.new(t)
			a := newCacheStore(t, backend)
			b := newCacheStore(t, backend)
			ctx := context.Background()
			const user = "u1"
			const perWave = 20

			login := func(wave int) []string {
				ids := make([]string, perWave)
				var wg sync.WaitGroup
				for i := range ids {
					ids[i] = fmt.Sprintf("w%d-%d", wave, i)
					wg.Add(1)
					go func(id string) {
						defer wg.Done()
						if err := b.Put(ctx, cacheSession(id, user)); err != nil {
							t.Errorf("Put %s: %v", id, err)
						}
					}(ids[i])
				}
				wg.Wait()
				return ids
			}

			wave1 := login(1)

			var wave2 []string
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				wave2 = login(2)
			}()
			go func() {
				defer wg.Done()
				if err := a.DeleteAllForUser(ctx, user); err != nil {
					t.Errorf("DeleteAllForUser: %v", err)
				}
			}()
			wg.Wait()

			wave3 := login(3)

			listed := listedIDs(t, a, user)
			check := func(ids []string, want *bool) {
				for _, id := range ids {
					_, err := a.Get(ctx, id)
					live := err == nil
					if !live && !errors.Is(err, auth.ErrSessionNotFound) {
						t.Errorf("%s: unexpected error %v", id, err)
					}
					if live != listed[id] {
						t.Errorf("%s: Get live=%v but listed=%v", id, live, listed[id])
					}
					if want != nil && live != *want {
						t.Errorf("%s: live=%v, want %v", id, live, *want)
					}
				}
			}
			no, yes := false, true
			check(wave1, &no)
			check(wave2, nil)
			check(wave3, &yes)
		})
	}
}

// TestCacheStore_TouchLosesRaceAgainstDelete deletes the record between// TestCacheStore_TouchLosesRaceAgainstDelete deletes the record between
// the read and the replace inside Touch and proves nothing is written
// back.
func TestCacheStore_TouchLosesRaceAgainstDelete(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			backend := bf.new(t)
			s := newCacheStore(t, backend)
			ctx := context.Background()
			if err := s.Put(ctx, cacheSession("racy", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			// Hook: the first GetStringCtx (Touch's read) is followed by a
			// Delete from a second instance before the ReplaceCtx write.
			other := newCacheStore(t, backend)
			hooked := &deleteAfterReadBackend{cacheBackend: s.backend, after: func() {
				_ = other.Delete(ctx, "racy")
			}}
			s.backend = hooked
			if err := s.Touch(ctx, "racy", time.Now()); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("Touch after losing the race: %v, want ErrSessionNotFound", err)
			}
			if _, err := other.Get(ctx, "racy"); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("record resurrected by Touch: %v", err)
			}
		})
	}
}

// deleteAfterReadBackend runs after once, right after the first successful
// GetStringCtx, modelling a revocation that lands inside Touch's window.
type deleteAfterReadBackend struct {
	cacheBackend
	once  sync.Once
	after func()
}

func (b *deleteAfterReadBackend) GetStringCtx(ctx context.Context, key string) (string, bool) {
	v, ok := b.cacheBackend.GetStringCtx(ctx, key)
	if ok {
		b.once.Do(b.after)
	}
	return v, ok
}

func TestCacheStore_PutValidation(t *testing.T) {
	s := newCacheStore(t, drivers.NewMemoryStore("sessions"))
	ctx := context.Background()
	if err := s.Put(ctx, nil); err == nil {
		t.Fatal("nil session accepted")
	}
	if err := s.Put(ctx, &auth.StoredSession{UserID: "u"}); err == nil {
		t.Fatal("empty id accepted")
	}
	if err := s.Put(ctx, &auth.StoredSession{ID: "s"}); err == nil {
		t.Fatal("empty user id accepted")
	}
	if _, err := s.Get(ctx, ""); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("Get empty id: %v", err)
	}
	if err := s.Touch(ctx, "", time.Now()); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("Touch empty id: %v", err)
	}
	if err := s.Delete(ctx, ""); err != nil {
		t.Fatalf("Delete empty id: %v", err)
	}
	if err := s.DeleteAllForUser(ctx, ""); err != nil {
		t.Fatalf("DeleteAllForUser empty user: %v", err)
	}
	if list, err := s.ListForUser(ctx, ""); err != nil || list != nil {
		t.Fatalf("ListForUser empty user: %v %v", list, err)
	}
}

// TestCacheStore_PutReassignUser moves an id to a new owner and checks the
// old owner's listing no longer carries it.
func TestCacheStore_PutReassignUser(t *testing.T) {
	s := newCacheStore(t, drivers.NewMemoryStore("sessions"))
	ctx := context.Background()
	_ = s.Put(ctx, cacheSession("shared", "u1"))
	_ = s.Put(ctx, cacheSession("shared", "u2"))
	if got := listedIDs(t, s, "u1"); got["shared"] {
		t.Fatal("old owner still lists the reassigned id")
	}
	if got := listedIDs(t, s, "u2"); !got["shared"] {
		t.Fatal("new owner does not list the reassigned id")
	}
}

// TestCacheStore_ExpiredRecordEvicted advances the store clock past
// ExpiresAt and checks Get reports expiry once and then not-found.
func TestCacheStore_ExpiredRecordEvicted(t *testing.T) {
	s := newCacheStore(t, drivers.NewMemoryStore("sessions"))
	ctx := context.Background()
	_ = s.Put(ctx, cacheSession("old", "u1"))
	s.clock = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := s.Get(ctx, "old"); !errors.Is(err, auth.ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}
	if _, err := s.Get(ctx, "old"); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after eviction, got %v", err)
	}
	if got := listedIDs(t, s, "u1"); len(got) != 0 {
		t.Fatalf("expired session still listed: %v", got)
	}
}

// TestCacheStore_ContextCancelled checks every method honours ctx.
func TestCacheStore_ContextCancelled(t *testing.T) {
	s := newCacheStore(t, drivers.NewMemoryStore("sessions"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Get(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get: %v", err)
	}
	if err := s.Put(ctx, cacheSession("x", "u")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Touch(ctx, "x", time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Touch: %v", err)
	}
	if err := s.Delete(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.DeleteAllForUser(ctx, "u"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteAllForUser: %v", err)
	}
	if _, err := s.ListForUser(ctx, "u"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListForUser: %v", err)
	}
}

// TestCacheStore_GenerationTokenFailure surfaces a failing entropy source
// on DeleteAllForUser instead of silently rotating to an empty token.
func TestCacheStore_GenerationTokenFailure(t *testing.T) {
	s := newCacheStore(t, drivers.NewMemoryStore("sessions"))
	s.rand = failingReader{}
	if err := s.DeleteAllForUser(context.Background(), "u1"); err == nil {
		t.Fatal("expected error from failing entropy source")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }
