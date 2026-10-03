package velocity

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/scheduler"
)

// startSwapModule runs start in its Start: a module that replaces owned
// Services fields.
type startSwapModule struct {
	start func(s *app.Services) error
}

func (m startSwapModule) Init(*app.Services) error       { return nil }
func (m startSwapModule) Start(s *app.Services) error    { return m.start(s) }
func (m startSwapModule) Shutdown(context.Context) error { return nil }
func swapIn(fn func(s *app.Services)) startSwapModule {
	return startSwapModule{start: func(s *app.Services) error { fn(s); return nil }}
}
func swapThenFail(fn func(s *app.Services), err error) app.Module {
	return startSwapModule{start: func(s *app.Services) error { fn(s); return err }}
}

// valueCache is a cache manager held by value with a map in it: Go cannot
// compare it with ==.
type valueCache struct {
	contract.CacheManager
	tags map[string]string
}

func TestRebind_RefusesANonComparableField(t *testing.T) {
	_, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		s.Cache = valueCache{CacheManager: s.Cache, tags: map[string]string{}}
	})))
	if err == nil {
		t.Fatal("New accepted a non-comparable Services.Cache")
	}
	if !strings.Contains(err.Error(), "Services.Cache") || !strings.Contains(err.Error(), "not comparable") {
		t.Fatalf("error does not name the field: %v", err)
	}
}

// unwrapper decorates inner and exposes it through Unwrap.
type unwrapper struct{ inner any }

func (u *unwrapper) Unwrap() any { return u.inner }

// panicUnwrapper's Unwrap panics.
type panicUnwrapper struct{}

func (*panicUnwrapper) Unwrap() any { panic("unwrap boom") }

// valueUnwrapper is an unwrapper of a value type that cannot be compared.
type valueUnwrapper struct {
	tags  map[string]string
	inner any
}

func (u valueUnwrapper) Unwrap() any { return u.inner }

func TestUnwrapHolds(t *testing.T) {
	target := &unwrapper{}
	direct := &unwrapper{inner: target}
	twoDeep := &unwrapper{inner: &unwrapper{inner: target}}
	cycA := &unwrapper{}
	cycB := &unwrapper{inner: cycA}
	cycA.inner = cycB
	deep := &unwrapper{}
	cur := deep
	for range maxUnwrapDepth + 2 {
		next := &unwrapper{}
		cur.inner = next
		cur = next
	}
	cur.inner = target
	cycV := &unwrapper{}
	cycV.inner = valueUnwrapper{tags: map[string]string{}, inner: cycV}

	tests := []struct {
		name    string
		v       any
		want    unwrapOutcome
		wantErr bool
	}{
		{"direct", direct, unwrapFound, false},
		{"two deep", twoDeep, unwrapFound, false},
		{"elsewhere", &unwrapper{inner: &unwrapper{}}, unwrapNotFound, false},
		{"no Unwrap", struct{}{}, unwrapNotFound, false},
		{"non-comparable holder", valueUnwrapper{tags: map[string]string{}, inner: target}, unwrapFound, false},
		{"non-comparable holder, two deep", valueUnwrapper{tags: map[string]string{}, inner: direct}, unwrapFound, false},
		{"non-comparable holder, elsewhere", valueUnwrapper{tags: map[string]string{}, inner: &unwrapper{}}, unwrapNotFound, false},
		{"non-comparable holder, no Unwrap", map[string]string{}, unwrapNotFound, false},
		{"non-comparable value inside the chain", &unwrapper{inner: valueUnwrapper{tags: map[string]string{}, inner: target}}, unwrapFound, false},
		{"non-comparable value two deep in the chain", &unwrapper{inner: &unwrapper{inner: valueUnwrapper{tags: map[string]string{}, inner: target}}}, unwrapFound, false},
		{"non-comparable value inside the chain, elsewhere", &unwrapper{inner: valueUnwrapper{tags: map[string]string{}, inner: &unwrapper{}}}, unwrapNotFound, false},
		{"cycle through a non-comparable value", cycV, unwrapUnknown, false},
		{"cycle", cycA, unwrapUnknown, false},
		{"past the depth cap", deep, unwrapUnknown, false},
		{"panicking", &panicUnwrapper{}, unwrapUnknown, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := unwrapHolds(tt.v, target)
			if got != tt.want {
				t.Fatalf("outcome = %v, want %v", got, tt.want)
			}
			if tt.wantErr {
				var pe *panicerr.Error
				if !errors.As(err, &pe) {
					t.Fatalf("err = %v, want the recovered panic", err)
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// shutdownApp shuts a down at the end of the test.
func shutdownApp(t *testing.T, a *App) {
	t.Helper()
	t.Cleanup(func() {
		if err := a.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
}

func TestRebind_ORMDefaultFollowsDB(t *testing.T) {
	var next *orm.Manager
	cfg := testAppConfig()
	cfg.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
	a, err := New(WithConfig(cfg), WithModules(swapIn(func(s *app.Services) {
		m, err := initDB(DBConfig{Connection: "sqlite", Database: ":memory:"}, s.Log)
		if err != nil {
			t.Fatalf("initDB: %v", err)
		}
		next = m
		s.DB = m
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	if got := orm.Default(); got != next {
		t.Fatalf("orm.Default() = %p, want the module's manager %p", got, next)
	}
}

func TestRebind_LoginThrottlerFollowsCache(t *testing.T) {
	var next *cache.Manager
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		next = initCache(CacheConfig{Driver: "memory", Prefix: "swapped"}, s.Log)
		s.Cache = next
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	want, err := next.DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	if a.loginThrottler == nil {
		t.Fatal("New installed no login throttler")
	}
	if got := a.loginThrottler.cache(); got != want {
		t.Fatalf("throttler counts in %p, want the replacement's default store %p", got, want)
	}
}

func TestRebind_SchedulerLockerFollowsCache(t *testing.T) {
	dir := t.TempDir()
	var next *cache.Manager
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		next = initCache(CacheConfig{Driver: "file", Path: dir, Prefix: "swapped"}, s.Log)
		s.Cache = next
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	l, ok := a.builtScheduler.Locker().(*cacheLocker)
	if !ok {
		t.Fatalf("scheduler Locker = %T, want the cache-backed Locker over the replacement", a.builtScheduler.Locker())
	}
	if l.cm != next {
		t.Fatalf("cache Locker over %p, want the replacement %p", l.cm, next)
	}
}

func TestRebind_SchedulerLockerAModuleSetIsKept(t *testing.T) {
	dir := t.TempDir()
	own := scheduler.NewInMemoryLocker()
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		s.Scheduler.(*scheduler.Scheduler).SetLocker(own)
		s.Cache = initCache(CacheConfig{Driver: "file", Path: dir, Prefix: "swapped"}, s.Log)
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	if got := a.builtScheduler.Locker(); got != own {
		t.Fatalf("scheduler Locker = %T, want the module's own", got)
	}
}

// receiverScheme records the login throttler and CSRF token rotator the
// auth manager hands a scheme registered with it.
type receiverScheme struct {
	auth.Scheme
	throttler contract.LoginThrottler
	rotator   contract.CSRFTokenRotator
}

func (s *receiverScheme) SetLoginThrottler(t contract.LoginThrottler)     { s.throttler = t }
func (s *receiverScheme) SetCSRFTokenRotator(r contract.CSRFTokenRotator) { s.rotator = r }

// noopThrottler is a module's own login throttler.
type noopThrottler struct{ auth.NoopLoginThrottler }

func TestRebind_AModuleSetLoginThrottlerSurvives(t *testing.T) {
	own := &noopThrottler{}
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		s.Auth.(*auth.Manager).SetLoginThrottler(own)
		s.Cache = initCache(CacheConfig{Driver: "memory", Prefix: "swapped"}, s.Log)
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	probe := &receiverScheme{}
	a.Services.Auth.(*auth.Manager).RegisterScheme("probe", probe)
	if probe.throttler != contract.LoginThrottler(own) {
		t.Fatalf("auth manager throttler = %T, want the module's own", probe.throttler)
	}
}

// plainCSRF is a CSRF protector with no token rotator.
type plainCSRF struct{ contract.CSRFProtector }

func TestRebind_ACSRFReplacementWithoutARotatorClearsIt(t *testing.T) {
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		s.CSRF = &plainCSRF{s.CSRF}
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	probe := &receiverScheme{}
	a.Services.Auth.(*auth.Manager).RegisterScheme("probe", probe)
	if probe.rotator != nil {
		t.Fatalf("auth manager still rotates %T, the displaced CSRF's tokens", probe.rotator)
	}
}

// TestLoginThrottler_StoreSwapWhileCounting moves the throttler's store
// while attempts are counted: each call uses one store whole (run with
// -race).
func TestLoginThrottler_StoreSwapWhileCounting(t *testing.T) {
	stores := make([]contract.CacheStore, 2)
	for i := range stores {
		s, err := initCache(CacheConfig{Driver: "memory"}, nil).DefaultStore()
		if err != nil {
			t.Fatalf("DefaultStore: %v", err)
		}
		stores[i] = s
	}
	th := newCacheLoginThrottler(stores[0], 0, 0, 0, 0)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				th.RecordFailure(r, "k")
				_ = th.Allow(r, "k")
			}
		}()
	}
	for i := range 500 {
		th.setStore(stores[i%2])
	}
	wg.Wait()
}

// dbChannelApp boots an app with an in-memory sqlite database whose
// module runs fn in its Start.
func dbChannelApp(t *testing.T, fn func(s *app.Services)) (*App, error) {
	t.Helper()
	cfg := testAppConfig()
	cfg.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
	return New(WithConfig(cfg), WithModules(swapIn(fn)))
}

func TestRebind_NotificationDatabaseChannelFollowsDB(t *testing.T) {
	var next *orm.Manager
	a, err := dbChannelApp(t, func(s *app.Services) {
		m, err := initDB(DBConfig{Connection: "sqlite", Database: ":memory:"}, s.Log)
		if err != nil {
			t.Fatalf("initDB: %v", err)
		}
		next = m
		s.DB = m
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	if a.dbChannel == nil {
		t.Fatal("New recorded no notification database channel")
	}
	db, driver := a.dbChannel.DB()
	if db != next.DB() || driver != next.DriverName() {
		t.Fatalf("channel holds %p (%q), want the replacement's %p (%q)", db, driver, next.DB(), next.DriverName())
	}
}

func TestRebind_NotificationDatabaseChannelAModuleSetIsKept(t *testing.T) {
	own, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = own.Close() })
	a, err := dbChannelApp(t, func(s *app.Services) {
		ch, err := s.Notification.(*notification.Manager).Channel("database")
		if err != nil {
			t.Fatalf("Channel: %v", err)
		}
		ch.(notificationDBChannel).SetDB(own, "sqlite")
		m, err := initDB(DBConfig{Connection: "sqlite", Database: ":memory:"}, s.Log)
		if err != nil {
			t.Fatalf("initDB: %v", err)
		}
		s.DB = m
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	if db, _ := a.dbChannel.DB(); db != own {
		t.Fatalf("channel holds %p, want the module's own %p", db, own)
	}
}

// panickingDB's DB panics.
type panickingDB struct{ contract.Database }

func (panickingDB) DB() *sql.DB { panic("db boom") }

func TestRebind_APanickingReplacementFailsTheBoundaryContained(t *testing.T) {
	_, err := dbChannelApp(t, func(s *app.Services) { s.DB = panickingDB{s.DB} })
	if err == nil || !strings.Contains(err.Error(), "notification database channel") {
		t.Fatalf("New = %v, want the contained panic naming the consumer", err)
	}
}

// The identity checks never compare two values Go cannot compare: such a
// value is never the same instance as another.
func TestContainsIdentity_ANonComparableValueIsNeverTheSame(t *testing.T) {
	v := valueUnwrapper{tags: map[string]string{}}
	p := &unwrapper{}
	if containsIdentity([]any{v, p}, v) {
		t.Fatal("a value that cannot be compared was found by identity")
	}
	if !containsIdentity([]any{v, p}, p) {
		t.Fatal("a pointer in the list was not found")
	}
}
