package velocity

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/orm"
)

// A binding New could not install, because the service it binds to was
// absent at boot, is installed at the boundary after a module supplies the
// service: a service that arrives late is bound as one that was there.

// storelessCacheConfig is a boot config whose cache has no default store:
// its driver's factory fails for the length of the test.
func storelessCacheConfig(t *testing.T) Config {
	t.Helper()
	prev := cache.Drivers().Override("redis", func(context.Context, cache.StoreConfig) (cache.Store, error) {
		return nil, errors.New("no cache backend")
	})
	t.Cleanup(func() { cache.Drivers().Override("redis", prev) })
	cfg := testAppConfig()
	cfg.Cache.Driver = "redis"
	return cfg
}

func TestRebind_LoginThrottlerInstalledWhenACacheArrivesLate(t *testing.T) {
	var next *cache.Manager
	scheme := &fakeLoginThrottlerScheme{}
	a, err := New(WithConfig(storelessCacheConfig(t)), WithModules(swapIn(func(s *app.Services) {
		s.Auth.(*auth.Manager).RegisterScheme("late", scheme)
		next = initCache(CacheConfig{Driver: "memory", Prefix: "late"}, s.Log)
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
		t.Fatal("no login throttler after a module installed a cache with a default store")
	}
	if got := a.loginThrottler.cache(); got != want {
		t.Fatalf("throttler counts in %p, want the late cache's default store %p", got, want)
	}
	if scheme.throttler != a.loginThrottler {
		t.Fatalf("the auth manager's schemes hold %T, want the framework's cache-backed throttler", scheme.throttler)
	}
}

// A throttler a module gave the auth manager is kept when a cache arrives
// late: after New the framework only swaps its own throttler's store and
// never sets a throttler on the manager again.
func TestRebind_LoginThrottlerAModuleSetIsKeptWhenACacheArrivesLate(t *testing.T) {
	own := auth.NoopLoginThrottler{}
	scheme := &fakeLoginThrottlerScheme{}
	a, err := New(WithConfig(storelessCacheConfig(t)), WithModules(swapIn(func(s *app.Services) {
		manager := s.Auth.(*auth.Manager)
		manager.RegisterScheme("late", scheme)
		manager.SetLoginThrottler(own)
		s.Cache = initCache(CacheConfig{Driver: "memory", Prefix: "late"}, s.Log)
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	if scheme.throttler != contract.LoginThrottler(own) {
		t.Fatalf("the auth manager's schemes hold %T, want the module's own throttler", scheme.throttler)
	}
}

// A late cache that has no default store either leaves the framework's
// throttler without a store, and the app still boots.
func TestRebind_LoginThrottlerHasNoStoreWithoutADefaultStore(t *testing.T) {
	cfg := storelessCacheConfig(t)
	a, err := New(WithConfig(cfg), WithModules(swapIn(func(s *app.Services) {
		s.Cache = initCache(cfg.Cache, s.Log)
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	if a.loginThrottler == nil {
		t.Fatal("New installed no login throttler")
	}
	if got := a.loginThrottler.cache(); got != nil {
		t.Fatalf("throttler counts in %T over a cache with no default store, want no store", got)
	}
}

// A database a module set on the notification channel itself is kept when
// the app's database arrives late.
func TestRebind_NotificationDatabaseChannelAModuleSetIsKeptWhenADatabaseArrivesLate(t *testing.T) {
	own, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = own.Close() })
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
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
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	if db, _ := a.dbChannel.DB(); db != own {
		t.Fatalf("channel holds %p, want the module's own %p", db, own)
	}
}

func TestRebind_ORMDefaultInstalledWhenADatabaseArrivesLate(t *testing.T) {
	if got := orm.Default(); got != nil {
		t.Fatalf("orm.Default() = %p before the test, want none", got)
	}
	var next *orm.Manager
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
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

// A default the module installed itself is kept when its database arrives
// late.
func TestRebind_ORMDefaultAModuleSetIsKeptWhenADatabaseArrivesLate(t *testing.T) {
	own, err := initDB(DBConfig{Connection: "sqlite", Database: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("initDB: %v", err)
	}
	t.Cleanup(func() {
		_ = own.Shutdown(context.Background())
		orm.ResetDefault()
	})
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
		m, err := initDB(DBConfig{Connection: "sqlite", Database: ":memory:"}, s.Log)
		if err != nil {
			t.Fatalf("initDB: %v", err)
		}
		orm.SetDefault(own)
		s.DB = m
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := orm.Default(); got != own {
		t.Fatalf("orm.Default() = %p, want the manager the module made the default %p", got, own)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestRebind_NotificationDatabaseChannelBoundWhenADatabaseArrivesLate(t *testing.T) {
	var next *orm.Manager
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) {
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
	if a.dbChannel == nil {
		t.Fatal("New recorded no notification database channel")
	}
	db, driver := a.dbChannel.DB()
	if db != next.DB() || driver != next.DriverName() {
		t.Fatalf("channel holds %p (%q), want the late database's %p (%q)", db, driver, next.DB(), next.DriverName())
	}
}
