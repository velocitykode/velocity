package velocity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	cacheredis "github.com/velocitykode/velocity/cache/redis"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
)

// hookedStore lets a test run code between the throttler's individual
// cache operations on one key, to model the window expiring in between.
type hookedStore struct {
	contract.CacheStore
	target   string
	afterAdd func()
}

func (s *hookedStore) AddCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	added, err := s.CacheStore.AddCtx(ctx, key, value, ttl)
	if key == s.target && s.afterAdd != nil {
		s.afterAdd()
	}
	return added, err
}

func expiryRedis(t *testing.T) (*cacheredis.RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	store, err := cacheredis.NewRedisStore(context.Background(), "expiry-test", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	return store, mr
}

// TestCacheLoginThrottler_ExpiryBetweenAddAndIncrement_DoesNotImmortalise
// models the window expiring between the add-if-absent and the atomic
// increment. Every store recreates the key from the increment with no
// TTL; without the re-put the bucket would deny that IP forever.
func TestCacheLoginThrottler_ExpiryBetweenAddAndIncrement_DoesNotImmortalise(t *testing.T) {
	t.Run("redis", func(t *testing.T) {
		store, mr := expiryRedis(t)
		key := auth.ThrottleKeyIPPrefix + "victim-ip"
		cacheKey := loginThrottleCachePrefix + key
		if err := store.PutCtx(context.Background(), cacheKey, int64(4), time.Millisecond); err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		wrapped := &hookedStore{CacheStore: store, target: cacheKey, afterAdd: func() {
			once.Do(func() { mr.FastForward(2 * time.Millisecond) })
		}}
		th := newCacheLoginThrottler(wrapped, 5, 20, 5, time.Minute)
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		for i := 0; i < 6; i++ {
			_, _ = th.Reserve(r, key)
		}
		if th.Allow(r, key) {
			t.Fatal("bucket should be over cap inside the window")
		}
		mr.FastForward(2 * time.Minute)
		if !th.Allow(r, key) {
			t.Fatal("IP budget still denied two decay windows later: expiry between add and increment left an immortal counter")
		}
		if within, _ := th.Reserve(r, key); !within {
			t.Fatal("Reserve still denied after the window should have cleared")
		}
	})

	t.Run("memory", func(t *testing.T) {
		store, err := newMemoryCacheManager().DefaultStore()
		if err != nil {
			t.Fatalf("DefaultStore: %v", err)
		}
		key := auth.ThrottleKeyIPPrefix + "victim-ip"
		cacheKey := loginThrottleCachePrefix + key
		if err := store.PutCtx(context.Background(), cacheKey, int64(4), 20*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		wrapped := &hookedStore{CacheStore: store, target: cacheKey, afterAdd: func() {
			once.Do(func() { time.Sleep(40 * time.Millisecond) })
		}}
		th := newCacheLoginThrottler(wrapped, 5, 20, 5, 100*time.Millisecond)
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		for i := 0; i < 6; i++ {
			_, _ = th.Reserve(r, key)
		}
		if th.Allow(r, key) {
			t.Fatal("bucket should be over cap inside the window")
		}
		time.Sleep(150 * time.Millisecond)
		if !th.Allow(r, key) {
			t.Fatal("IP budget still denied after the decay window: expiry between add and increment left an immortal counter")
		}
	})

	t.Run("RecordFailure has the same guard", func(t *testing.T) {
		store, mr := expiryRedis(t)
		key := auth.ThrottleKeyPairPrefix + "victim-pair"
		cacheKey := loginThrottleCachePrefix + key
		if err := store.PutCtx(context.Background(), cacheKey, int64(4), time.Millisecond); err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		wrapped := &hookedStore{CacheStore: store, target: cacheKey, afterAdd: func() {
			once.Do(func() { mr.FastForward(2 * time.Millisecond) })
		}}
		th := newCacheLoginThrottler(wrapped, 5, 20, 50, time.Minute)
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		for i := 0; i < 6; i++ {
			th.RecordFailure(r, key)
		}
		mr.FastForward(2 * time.Minute)
		if !th.Allow(r, key) {
			t.Fatal("pair budget still denied two decay windows later after RecordFailure")
		}
	})
}

// TestSessionScheme_CacheThrottler_WindowExpiryAfterReservation_KeepsSlot
// models the counter expiring right after the over-cap reservation, while
// a previous trial still holds the admission slot. The delay must come
// from the reservation's own count, not a re-read that now sees an empty
// window and would admit every waiting attempt past the held slot.
func TestSessionScheme_CacheThrottler_WindowExpiryAfterReservation_KeepsSlot(t *testing.T) {
	store, mr := expiryRedis(t)
	seed := httptest.NewRequest(http.MethodPost, "/login", nil)
	identifierKey := ""
	for _, key := range auth.ThrottleKeys(seed, map[string]any{"email": "victim@example.test"}, nil) {
		if strings.HasPrefix(key, auth.ThrottleKeyIdentifierPrefix) {
			identifierKey = key
		}
	}
	cacheKey := loginThrottleCachePrefix + identifierKey
	slotKey := cacheKey + loginThrottleTrialSuffix

	const parallel = 64
	arrived := make(chan struct{}, parallel)
	resume := make(chan struct{})
	th := newCacheLoginThrottler(store, 5, 20, 50, time.Minute)

	// Last second of an over-cap window, previous trial holding the slot.
	if err := store.PutCtx(context.Background(), cacheKey, int64(64), time.Second); err != nil {
		t.Fatal(err)
	}
	if !th.Admit(seed, identifierKey, 30*time.Second) {
		t.Fatal("setup: slot not claimed")
	}
	// From here every attempt pauses right after its over-cap reservation
	// was counted, before anything else reads the counter or touches the
	// slot; the window expires while they wait.
	th.store = &incrementPauseStore{CacheStore: store, counterKey: cacheKey, arrived: arrived, resume: resume}

	enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	release := make(chan struct{})
	close(release)
	users := &probeUsers{entered: make(chan struct{}, parallel), release: release}
	scheme, err := schemes.NewSessionScheme(users, auth.SessionConfig{Name: "expiry_session", Lifetime: 60, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	scheme.SetLoginThrottler(th)
	scheme.SetAttemptFloor(-1)
	_ = auth.GetDummyBcryptHash(0)

	var wg sync.WaitGroup
	oks := make([]bool, parallel)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/login", nil)
			r.RemoteAddr = fmt.Sprintf("198.51.100.%d:5000", i+1)
			r = schemes.WithSessionContext(r)
			password := "wrong"
			if i == parallel-1 {
				password = "test-only-correct-candidate"
			}
			oks[i], _ = scheme.Attempt(httptest.NewRecorder(), r, map[string]any{"email": "victim@example.test", "password": password})
		}(i)
	}
	timeout := time.After(5 * time.Second)
	for i := 0; i < parallel; i++ {
		select {
		case <-arrived:
		case <-timeout:
			t.Fatalf("only %d reservations reached the slot", i)
		}
	}
	mr.FastForward(2 * time.Second) // counter window expires; slot (30s) still held
	if _, present := store.GetCtx(context.Background(), cacheKey); present {
		t.Fatal("setup: counter should have expired")
	}
	if added, _ := store.AddCtx(context.Background(), slotKey, int64(1), 30*time.Second); added {
		t.Fatal("setup: slot should still be held")
	}
	close(resume)
	wg.Wait()
	if got := users.verified(); got != 0 {
		t.Fatalf("%d over-cap trials bypassed the held slot after the counter expired", got)
	}
	if oks[parallel-1] {
		t.Fatal("correct password logged in past the held slot")
	}
}

// incrementPauseStore pauses every attempt right after its reservation
// increment on the counter key until the test resumes it.
type incrementPauseStore struct {
	contract.CacheStore
	counterKey string
	arrived    chan<- struct{}
	resume     <-chan struct{}
}

func (s *incrementPauseStore) IncrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	n, err := s.CacheStore.IncrementCtx(ctx, key, value)
	if key == s.counterKey {
		s.arrived <- struct{}{}
		<-s.resume
	}
	return n, err
}
