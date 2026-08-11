package velocity

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/authtest"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
)

func TestCacheLoginThrottlerContract(t *testing.T) {
	authtest.RunLoginThrottlerContractTests(t, func(t *testing.T) contract.LoginThrottler {
		t.Helper()
		return newTestLoginThrottler(t, 5)
	})
}

func TestCacheLoginThrottler_LimitTripsAfterMaxFailures(t *testing.T) {
	th := newTestLoginThrottler(t, 5)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)

	for i := 0; i < 4; i++ {
		if !th.Allow(r, "limit-key") {
			t.Fatalf("Allow before failure %d = false, want true", i+1)
		}
		th.RecordFailure(r, "limit-key")
	}
	if !th.Allow(r, "limit-key") {
		t.Fatal("Allow after maxAttempts-1 failures = false, want true")
	}
	th.RecordFailure(r, "limit-key")
	if th.Allow(r, "limit-key") {
		t.Fatal("Allow after maxAttempts failures = true, want false")
	}
}

func TestCacheLoginThrottler_ConcurrentRecordFailureTripsLimit(t *testing.T) {
	th := newTestLoginThrottler(t, 5)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			th.RecordFailure(r, "burst-key")
		}()
	}
	wg.Wait()

	if th.Allow(r, "burst-key") {
		t.Fatal("Allow after concurrent failure burst = true, want false")
	}
}

func TestCacheLoginThrottler_RecordSuccessResetsCounter(t *testing.T) {
	th := newTestLoginThrottler(t, 3)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)

	for i := 0; i < 3; i++ {
		th.RecordFailure(r, "reset-key")
	}
	if th.Allow(r, "reset-key") {
		t.Fatal("Allow before reset = true, want false")
	}

	th.RecordSuccess(r, "reset-key")
	if !th.Allow(r, "reset-key") {
		t.Fatal("Allow after RecordSuccess reset = false, want true")
	}
}

func TestInstallLoginThrottler_NilAndStorelessInputsAreNoop(t *testing.T) {
	logger := &captureLogger{}

	installLoginThrottler(nil, nil, logger)
	installLoginThrottler(nil, newMemoryCacheManager(), logger)

	storeless := cache.NewManager(&cache.Config{
		Default: "missing",
		Stores:  map[string]cache.StoreConfig{},
	})
	manager := auth.NewManager()
	installLoginThrottler(manager, storeless, logger)
}

func TestInstallLoginThrottler_InstallsCacheBackedDefault(t *testing.T) {
	t.Setenv("AUTH_LOGIN_MAX_ATTEMPTS", "2")
	t.Setenv("AUTH_LOGIN_DECAY", "60s")

	manager := auth.NewManager()
	scheme := &fakeLoginThrottlerScheme{}
	manager.RegisterScheme("web", scheme)

	installLoginThrottler(manager, newMemoryCacheManager(), nil)
	if scheme.throttler == nil {
		t.Fatal("installLoginThrottler did not propagate a throttler")
	}

	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	scheme.throttler.RecordFailure(r, "installed-default")
	scheme.throttler.RecordFailure(r, "installed-default")
	if scheme.throttler.Allow(r, "installed-default") {
		t.Fatal("installed default did not throttle after configured max failures")
	}
}

func newTestLoginThrottler(t *testing.T, maxAttempts int) *cacheLoginThrottler {
	t.Helper()
	return newTestDimensionedLoginThrottler(t, maxAttempts, 0, 0)
}

func newTestDimensionedLoginThrottler(t *testing.T, pair, identifier, ip int) *cacheLoginThrottler {
	t.Helper()
	store, err := newMemoryCacheManager().DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	return newCacheLoginThrottler(store, pair, identifier, ip, time.Minute)
}

func newMemoryCacheManager() *cache.Manager {
	return cache.NewManager(&cache.Config{
		Default: "default",
		Stores: map[string]cache.StoreConfig{
			"default": {Driver: cache.DriverMemory},
		},
	})
}

type fakeLoginThrottlerScheme struct {
	throttler contract.LoginThrottler
}

func (g *fakeLoginThrottlerScheme) SetLoginThrottler(t contract.LoginThrottler) {
	g.throttler = t
}

func (g *fakeLoginThrottlerScheme) Check(*http.Request) bool { return false }
func (g *fakeLoginThrottlerScheme) User(*http.Request) auth.Authenticatable {
	return nil
}
func (g *fakeLoginThrottlerScheme) ID(*http.Request) interface{} { return nil }
func (g *fakeLoginThrottlerScheme) Login(http.ResponseWriter, *http.Request, auth.Authenticatable, ...bool) error {
	return nil
}
func (g *fakeLoginThrottlerScheme) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (g *fakeLoginThrottlerScheme) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}
func (g *fakeLoginThrottlerScheme) Logout(http.ResponseWriter, *http.Request) error {
	return nil
}
func (g *fakeLoginThrottlerScheme) SetUserStore(auth.UserStore) {}
