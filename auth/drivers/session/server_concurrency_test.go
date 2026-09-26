package session

import (
	"context"
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
	"github.com/velocitykode/velocity/contract"
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

// interleavingBackend runs during before its first ReplaceCtx, in another
// goroutine, and gives it a moment to land: the write another instance
// makes between a Touch's read and its write.
type interleavingBackend struct {
	cacheBackend
	once   sync.Once
	during func()
	done   chan struct{}
}

func (b *interleavingBackend) ReplaceCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	b.once.Do(func() {
		go func() {
			defer close(b.done)
			b.during()
		}()
		select {
		case <-b.done:
		case <-time.After(50 * time.Millisecond):
		}
	})
	return b.cacheBackend.ReplaceCtx(ctx, key, value, ttl)
}

// Lock passes the backend's lock through, when it has one.
func (b *interleavingBackend) Lock(key string, ttl ...time.Duration) contract.CacheLock {
	return b.cacheBackend.(interface {
		Lock(key string, ttl ...time.Duration) contract.CacheLock
	}).Lock(key, ttl...)
}

// A Touch (activity renewal) keeps the record's payload as it is when the
// Touch lands, not as it was when the Touch read the record: a save by
// another instance in between is not written back over.
func TestCacheStore_TouchKeepsDataSavedDuringIt(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			shared := bf.new(t)
			other := newCacheStore(t, shared)
			ib := &interleavingBackend{cacheBackend: shared.(cacheBackend), done: make(chan struct{})}
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
			<-ib.done

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
