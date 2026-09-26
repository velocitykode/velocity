package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache/drivers"
	cacheredis "github.com/velocitykode/velocity/cache/redis"
)

// recordBackends are the shipped server session record stores: the
// in-process MemoryStore and the CacheStore over the memory and redis
// cache drivers (redis runs on miniredis).
func recordBackends() []struct {
	name string
	new  func(t *testing.T) auth.ServerSessionStore
} {
	return []struct {
		name string
		new  func(t *testing.T) auth.ServerSessionStore
	}{
		{"memory records", func(t *testing.T) auth.ServerSessionStore {
			s := NewMemoryStore()
			t.Cleanup(func() { _ = s.Close(context.Background()) })
			return s
		}},
		{"cache records over memory", func(t *testing.T) auth.ServerSessionStore {
			b := drivers.NewMemoryStore("sessions")
			t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
			return newCacheStore(t, b)
		}},
		{"cache records over redis", func(t *testing.T) auth.ServerSessionStore {
			mr := miniredis.RunT(t)
			b, err := cacheredis.NewRedisStore(context.Background(), "sessions", mr.Host(), mr.Server().Addr().Port, "", 0, false)
			if err != nil {
				t.Fatalf("NewRedisStore: %v", err)
			}
			t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
			return newCacheStore(t, b)
		}},
	}
}

// loadSession loads the session id names from store, as a request carrying
// its cookie does.
func loadSession(t *testing.T, store *ServerStore, id string) auth.Session {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	s, err := store.Get(r, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.ID() != id {
		t.Fatalf("Get(%q) returned a fresh session %q: the record is gone", id, s.ID())
	}
	return s
}

// Two requests on one session that both loaded it before either saved keep
// each other's changes: a request that drained the flash is not undone by
// an earlier snapshot written back, and handlers writing different keys do
// not erase each other.
func TestServerStore_OverlappingRequestsKeepEachOthersChanges(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, err := NewServerStore(testConfig(), backend.new(t))
			if err != nil {
				t.Fatalf("NewServerStore: %v", err)
			}
			first, _ := store.Create("")
			first.Put("cart", "one item")
			first.Flash("status", "saved")
			if err := first.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save: %v", err)
			}
			id := first.ID()

			a := loadSession(t, store, id) // a background request
			b := loadSession(t, store, id) // the page that shows the flash

			if got := b.GetFlash("status"); got != "saved" {
				t.Fatalf("flash = %v, want saved", got)
			}
			b.Put("seen", "yes")
			if err := b.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save b: %v", err)
			}
			a.Put("draft", "hello")
			if err := a.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save a: %v", err)
			}

			after := loadSession(t, store, id)
			if got := after.GetFlash("status"); got != nil {
				t.Fatalf("the drained flash came back: %v", got)
			}
			if after.Get("seen") != "yes" || after.Get("draft") != "hello" || after.Get("cart") != "one item" {
				t.Fatalf("lost update: seen=%v draft=%v cart=%v", after.Get("seen"), after.Get("draft"), after.Get("cart"))
			}
		})
	}
}

// Requests on one session saving at the same time each keep their key.
func TestServerStore_ConcurrentSavesKeepEveryKey(t *testing.T) {
	const writers = 8
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, err := NewServerStore(testConfig(), backend.new(t))
			if err != nil {
				t.Fatalf("NewServerStore: %v", err)
			}
			first, _ := store.Create("")
			first.Flash("status", "saved")
			if err := first.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save: %v", err)
			}
			id := first.ID()

			sessions := make([]auth.Session, writers)
			for i := range sessions {
				sessions[i] = loadSession(t, store, id)
			}
			var wg sync.WaitGroup
			errs := make(chan error, writers)
			for i, s := range sessions {
				wg.Add(1)
				go func(i int, s auth.Session) {
					defer wg.Done()
					if i == 0 {
						_ = s.GetFlash("status")
					}
					s.Put(fmt.Sprintf("k%d", i), i)
					errs <- s.Save(httptest.NewRecorder())
				}(i, s)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("Save: %v", err)
				}
			}

			after := loadSession(t, store, id)
			for i := 0; i < writers; i++ {
				if after.Get(fmt.Sprintf("k%d", i)) == nil {
					t.Fatalf("key k%d lost to a concurrent save", i)
				}
			}
			if got := after.GetFlash("status"); got != nil {
				t.Fatalf("the drained flash came back: %v", got)
			}
		})
	}
}

// interleavingBackend runs during before its first record write, to
// completion: the write another instance makes between a Touch's read and
// its write.
type interleavingBackend struct {
	cacheBackend
	once   sync.Once
	during func()
}

func (b *interleavingBackend) CompareAndSwapCtx(ctx context.Context, key string, expected, value interface{}, ttl time.Duration) (bool, error) {
	b.once.Do(b.during)
	return b.cacheBackend.CompareAndSwapCtx(ctx, key, expected, value, ttl)
}

// A Touch (activity renewal) keeps the record's payload as it is when the
// Touch lands, not as it was when the Touch read the record: a save by
// another instance in between is not written back over.
func TestCacheStore_TouchKeepsDataSavedDuringIt(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			shared := bf.new(t)
			other := newCacheStore(t, shared)
			ib := &interleavingBackend{cacheBackend: shared.(cacheBackend)}
			touching := newCacheStore(t, ib)

			ctx := context.Background()
			rec := cacheSession("touch-race", "u1")
			rec.Data = map[string]any{"flash": map[string]any{"status": "saved"}}
			if err := other.Put(ctx, rec); err != nil {
				t.Fatalf("Put: %v", err)
			}
			ib.during = func() {
				drain := func(map[string]any) (map[string]any, error) {
					return map[string]any{"flash": map[string]any{}}, nil
				}
				if err := other.UpdateData(ctx, "touch-race", drain, time.Now(), time.Now().Add(time.Hour)); err != nil {
					t.Errorf("UpdateData: %v", err)
				}
			}
			if err := touching.Touch(ctx, "touch-race", time.Now(), time.Now().Add(time.Hour)); err != nil {
				t.Fatalf("Touch: %v", err)
			}

			got, err := other.Get(ctx, "touch-race")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			flash, _ := got.Data["flash"].(map[string]any)
			if len(flash) != 0 {
				t.Fatalf("Touch wrote its stale snapshot back over a concurrent save: %v", got.Data)
			}
		})
	}
}

// An UpdateData that pauses between its read and its write (for longer
// than any lock lease) while another instance saves the record never
// writes its earlier read back: its swap fails and the update runs again
// on the record as the other save left it, so the drained flash stays
// drained and both changes land.
func TestCacheStore_UpdateDataNeverWritesOverALaterSave(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			shared := bf.new(t)
			paused := newCacheStore(t, shared)
			other := newCacheStore(t, shared)
			ctx := context.Background()
			rec := cacheSession("paused-write", "u1")
			rec.Data = map[string]any{"flash": map[string]any{"status": "saved"}}
			if err := paused.Put(ctx, rec); err != nil {
				t.Fatalf("Put: %v", err)
			}
			runs := 0
			update := func(current map[string]any) (map[string]any, error) {
				runs++
				if runs == 1 {
					// The pause: another instance saves a drained flash
					// after this update read the record.
					drain := func(map[string]any) (map[string]any, error) {
						return map[string]any{"flash": map[string]any{}}, nil
					}
					if err := other.UpdateData(ctx, "paused-write", drain, time.Now(), time.Now().Add(time.Hour)); err != nil {
						t.Errorf("other instance's UpdateData: %v", err)
					}
				}
				out := map[string]any{"seen": "paused"}
				for k, v := range current {
					out[k] = v
				}
				return out, nil
			}
			if err := paused.UpdateData(ctx, "paused-write", update, time.Now(), time.Now().Add(time.Hour)); err != nil {
				t.Fatalf("UpdateData: %v", err)
			}
			got, err := other.Get(ctx, "paused-write")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			flash, _ := got.Data["flash"].(map[string]any)
			if len(flash) != 0 || got.Data["seen"] != "paused" {
				t.Fatalf("the paused update wrote its earlier read over a later save: %v (update ran %d times)", got.Data, runs)
			}
			if runs != 2 {
				t.Fatalf("update ran %d times, want 2 (once on the stale read, once on the saved record)", runs)
			}
		})
	}
}

// losingBackend makes every compare-and-swap lose, as if another writer
// rewrote the record between every read and write.
type losingBackend struct {
	cacheBackend
}

func (losingBackend) CompareAndSwapCtx(context.Context, string, interface{}, interface{}, time.Duration) (bool, error) {
	return false, nil
}

// A record that keeps changing under a write fails the write after a
// bounded number of attempts instead of retrying forever, and nothing is
// written.
func TestCacheStore_WriteGivesUpOnARecordThatKeepsChanging(t *testing.T) {
	shared := sharedBackends()[0].new(t)
	writer := newCacheStore(t, losingBackend{cacheBackend: shared.(cacheBackend)})
	ctx := context.Background()
	if err := writer.Put(ctx, cacheSession("contended", "u1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	runs := 0
	update := func(current map[string]any) (map[string]any, error) {
		runs++
		return map[string]any{"k": "changed"}, nil
	}
	err := writer.UpdateData(ctx, "contended", update, time.Now(), time.Now().Add(time.Hour))
	if !errors.Is(err, errRecordContended) {
		t.Fatalf("UpdateData on a record that keeps changing = %v, want errRecordContended", err)
	}
	if runs != recordWriteAttempts {
		t.Fatalf("update ran %d times, want %d", runs, recordWriteAttempts)
	}
	got, err := writer.Get(ctx, "contended")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Data["k"] != "v" {
		t.Fatalf("an abandoned write changed the record: %v", got.Data)
	}
}
