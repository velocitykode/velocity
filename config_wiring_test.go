package velocity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/storage"
)

// New hands Config.Auth.LoginThrottle to the throttler it installs: every
// cap, the decay and both delay bounds.
func TestNew_LoginThrottleConfigReachesThrottler(t *testing.T) {
	a, _ := newLoggerWiringApp(t, func(c *Config) {
		c.Auth.LoginThrottle = auth.LoginThrottleConfig{
			MaxAttempts:              3,
			MaxAttemptsPerIdentifier: 7,
			MaxAttemptsPerIP:         11,
			Decay:                    90 * time.Second,
			IdentifierDelay:          250 * time.Millisecond,
			IdentifierDelayMax:       2 * time.Second,
		}
	})
	th := a.loginThrottler
	if th == nil {
		t.Fatal("New installed no login throttler")
	}
	if th.maxAttempts != 3 || th.identifierMaxAttempts != 7 || th.ipMaxAttempts != 11 {
		t.Errorf("caps = %d/%d/%d, want 3/7/11", th.maxAttempts, th.identifierMaxAttempts, th.ipMaxAttempts)
	}
	if th.decay != 90*time.Second {
		t.Errorf("decay = %v, want 90s", th.decay)
	}
	if th.delayBase != 250*time.Millisecond || th.delayMax != 2*time.Second {
		t.Errorf("delay = (%v, %v), want (250ms, 2s)", th.delayBase, th.delayMax)
	}
}

// A negative IdentifierDelay in the config turns the delay off in the
// installed throttler.
func TestNew_LoginThrottleNegativeDelayDisables(t *testing.T) {
	a, _ := newLoggerWiringApp(t, func(c *Config) {
		c.Auth.LoginThrottle = auth.LoginThrottleConfig{IdentifierDelay: -1}
	})
	if a.loginThrottler == nil {
		t.Fatal("New installed no login throttler")
	}
	if got := a.loginThrottler.delayBase; got != 0 {
		t.Fatalf("delayBase = %v, want 0", got)
	}
}

// AUTH_LOGIN_MAX_ATTEMPTS is a positive integer; anything else is 0.
func TestConfigFromEnv_LoginMaxAttempts(t *testing.T) {
	for raw, want := range map[string]int{
		"5": 5, "12": 12, "": 0, "0": 0, "-2": 0, "abc": 0, "3.5": 0,
	} {
		t.Setenv("AUTH_LOGIN_MAX_ATTEMPTS", raw)
		if got := ConfigFromEnv().Auth.LoginThrottle.MaxAttempts; got != want {
			t.Fatalf("AUTH_LOGIN_MAX_ATTEMPTS=%q: MaxAttempts = %d, want %d", raw, got, want)
		}
	}
}

// AUTH_LOGIN_IDENTIFIER_DELAY_MAX takes a Go duration or bare seconds;
// invalid, negative and zero are 0 (zero does not disable the ceiling).
func TestConfigFromEnv_LoginIdentifierDelayMax(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"3s": 3 * time.Second, "4": 4 * time.Second, " 500ms ": 500 * time.Millisecond,
		"": 0, "x": 0, "-1s": 0, "0": 0,
	} {
		t.Setenv("AUTH_LOGIN_IDENTIFIER_DELAY_MAX", raw)
		if got := ConfigFromEnv().Auth.LoginThrottle.IdentifierDelayMax; got != want {
			t.Fatalf("AUTH_LOGIN_IDENTIFIER_DELAY_MAX=%q: IdentifierDelayMax = %v, want %v", raw, got, want)
		}
	}
}

// configEnvKeys are the cache, queue, storage and database keys the
// struct-shaped config reads.
var configEnvKeys = []string{
	"CACHE_DRIVER", "CACHE_MEMORY_MAX_ENTRIES", "CACHE_MAX_VALUE_BYTES", "CACHE_PATH", "CACHE_PREFIX",
	"REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD", "REDIS_DATABASE", "REDIS_TLS",
	"QUEUE_DRIVER", "QUEUE_REDIS_HOST", "QUEUE_REDIS_PORT", "QUEUE_REDIS_PASSWORD", "QUEUE_REDIS_DB",
	"QUEUE_ENCRYPT", "QUEUE_SIGNING_KEY",
	"STORAGE_DRIVER", "FILESYSTEM_LOCAL_ROOT",
	"AWS_BUCKET", "AWS_DEFAULT_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_URL",
	"DB_CONNECTION", "DB_HOST", "DB_PORT", "DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS", "DB_CONN_MAX_LIFETIME",
}

// clearConfigEnv empties every key in configEnvKeys for the test.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range configEnvKeys {
		t.Setenv(k, "")
	}
}

// fieldCheck names one Config field and its expected value.
type fieldCheck struct {
	name string
	got  func(Config) any
	want any
}

func runFieldChecks(t *testing.T, cfg Config, checks []fieldCheck) {
	t.Helper()
	for _, c := range checks {
		if got := c.got(cfg); got != c.want {
			t.Errorf("%s = %#v, want %#v", c.name, got, c.want)
		}
	}
}

func defaultStore(c Config) cache.StoreConfig { return c.Cache.Stores["default"] }

// Each cache, queue, storage and database key lands in its own field.
// Every value is distinct, so two swapped fields fail.
func TestConfigFromEnv_StructKeysLandInTheirFields(t *testing.T) {
	clearConfigEnv(t)
	for k, v := range map[string]string{
		"CACHE_DRIVER":             "redis",
		"CACHE_MEMORY_MAX_ENTRIES": "500",
		"CACHE_MAX_VALUE_BYTES":    "4096",
		"CACHE_PATH":               "/var/cache/app",
		"CACHE_PREFIX":             "app_cache",
		"REDIS_HOST":               "redis.internal",
		"REDIS_PORT":               "6380",
		"REDIS_PASSWORD":           "cache-pw",
		"REDIS_DATABASE":           "3",
		"REDIS_TLS":                "true",
		"QUEUE_DRIVER":             "redis",
		"QUEUE_REDIS_HOST":         "queue.internal",
		"QUEUE_REDIS_PORT":         "6390",
		"QUEUE_REDIS_PASSWORD":     "queue-pw",
		"QUEUE_REDIS_DB":           "5",
		"QUEUE_ENCRYPT":            "true",
		"QUEUE_SIGNING_KEY":        "sign-key",
		"STORAGE_DRIVER":           "s3",
		"FILESYSTEM_LOCAL_ROOT":    "/srv/files",
		"AWS_BUCKET":               "bucket-1",
		"AWS_DEFAULT_REGION":       "eu-west-2",
		"AWS_ACCESS_KEY_ID":        "AKIDEXAMPLE",
		"AWS_SECRET_ACCESS_KEY":    "aws-secret",
		"AWS_URL":                  "https://cdn.example.com",
		"DB_CONNECTION":            "postgres",
		"DB_HOST":                  "db.internal",
		"DB_PORT":                  "5433",
		"DB_MAX_OPEN_CONNS":        "40",
		"DB_MAX_IDLE_CONNS":        "4",
		"DB_CONN_MAX_LIFETIME":     "90",
	} {
		t.Setenv(k, v)
	}
	cfg := ConfigFromEnv()
	if _, ok := cfg.Cache.Stores["default"]; !ok || len(cfg.Cache.Stores) != 1 || cfg.Cache.Default != "default" {
		t.Fatalf("Cache = %+v, want one store named default", cfg.Cache)
	}
	runFieldChecks(t, cfg, []fieldCheck{
		{"Cache.Prefix", func(c Config) any { return c.Cache.Prefix }, "app_cache"},
		{"Cache store Driver", func(c Config) any { return defaultStore(c).Driver }, "redis"},
		{"Cache store MaxEntries", func(c Config) any { return defaultStore(c).MaxEntries }, 500},
		{"Cache store MaxValueBytes", func(c Config) any { return defaultStore(c).MaxValueBytes }, int64(4096)},
		{"Cache store Path", func(c Config) any { return defaultStore(c).Path }, "/var/cache/app"},
		{"Cache store Host", func(c Config) any { return defaultStore(c).Host }, "redis.internal"},
		{"Cache store Port", func(c Config) any { return defaultStore(c).Port }, 6380},
		{"Cache store Password", func(c Config) any { return defaultStore(c).Password }, "cache-pw"},
		{"Cache store Database", func(c Config) any { return defaultStore(c).Database }, 3},
		{"Cache store TLS", func(c Config) any { return defaultStore(c).TLS }, true},

		{"Queue.Driver", func(c Config) any { return c.Queue.Driver }, "redis"},
		{"Queue.Redis.Host", func(c Config) any { return c.Queue.Redis.Host }, "queue.internal"},
		{"Queue.Redis.Port", func(c Config) any { return c.Queue.Redis.Port }, "6390"},
		{"Queue.Redis.Password", func(c Config) any { return c.Queue.Redis.Password }, "queue-pw"},
		{"Queue.Redis.DB", func(c Config) any { return c.Queue.Redis.DB }, "5"},
		{"Queue.Redis.TLS", func(c Config) any { return c.Queue.Redis.TLS }, true},
		{"Queue.Encrypt", func(c Config) any { return c.Queue.Encrypt }, true},
		{"Queue.SigningKey", func(c Config) any { return c.Queue.SigningKey }, "sign-key"},

		{"Storage.Default", func(c Config) any { return c.Storage.Default }, "s3"},
		{"Storage local Driver", func(c Config) any { return c.Storage.Disks["local"].Driver }, "local"},
		{"Storage local Root", func(c Config) any { return c.Storage.Disks["local"].Root }, "/srv/files"},
		{"Storage s3 Driver", func(c Config) any { return c.Storage.Disks["s3"].Driver }, "s3"},
		{"Storage s3 Bucket", func(c Config) any { return c.Storage.Disks["s3"].Bucket }, "bucket-1"},
		{"Storage s3 Region", func(c Config) any { return c.Storage.Disks["s3"].Region }, "eu-west-2"},
		{"Storage s3 Key", func(c Config) any { return c.Storage.Disks["s3"].Key }, "AKIDEXAMPLE"},
		{"Storage s3 Secret", func(c Config) any { return c.Storage.Disks["s3"].Secret }, "aws-secret"},
		{"Storage s3 URL", func(c Config) any { return c.Storage.Disks["s3"].URL }, "https://cdn.example.com"},

		{"DB.Driver", func(c Config) any { return c.DB.Driver }, "postgres"},
		{"DB.Host", func(c Config) any { return c.DB.Host }, "db.internal"},
		{"DB.Port", func(c Config) any { return c.DB.Port }, "5433"},
		{"DB.MaxOpenConns", func(c Config) any { return c.DB.MaxOpenConns }, 40},
		{"DB.MaxIdleConns", func(c Config) any { return c.DB.MaxIdleConns }, 4},
		{"DB.ConnMaxLifetime", func(c Config) any { return c.DB.ConnMaxLifetime }, 90 * time.Second},
	})
}

// With the keys unset (or unparseable where the key is numeric), each
// field takes its documented default.
func TestConfigFromEnv_StructKeysDefaults(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("REDIS_PORT", "x")
	t.Setenv("REDIS_TLS", "TRUE") // exact "true" only
	t.Setenv("CACHE_MAX_VALUE_BYTES", "4k")
	t.Setenv("DB_MAX_OPEN_CONNS", "many")
	t.Setenv("QUEUE_ENCRYPT", "1") // exact "true" only
	cfg := ConfigFromEnv()
	runFieldChecks(t, cfg, []fieldCheck{
		{"Cache.Prefix", func(c Config) any { return c.Cache.Prefix }, "velocity_cache"},
		{"Cache store Driver", func(c Config) any { return defaultStore(c).Driver }, "memory"},
		{"Cache store MaxEntries", func(c Config) any { return defaultStore(c).MaxEntries }, 0},
		{"Cache store MaxValueBytes", func(c Config) any { return defaultStore(c).MaxValueBytes }, int64(0)},
		{"Cache store Path", func(c Config) any { return defaultStore(c).Path }, ""},
		{"Cache store Host", func(c Config) any { return defaultStore(c).Host }, "127.0.0.1"},
		{"Cache store Port", func(c Config) any { return defaultStore(c).Port }, 6379},
		{"Cache store Password", func(c Config) any { return defaultStore(c).Password }, ""},
		{"Cache store Database", func(c Config) any { return defaultStore(c).Database }, 0},
		{"Cache store TLS", func(c Config) any { return defaultStore(c).TLS }, false},

		{"Queue.Driver", func(c Config) any { return c.Queue.Driver }, "memory"},
		{"Queue.Redis.Host", func(c Config) any { return c.Queue.Redis.Host }, "localhost"},
		{"Queue.Redis.Port", func(c Config) any { return c.Queue.Redis.Port }, "6379"},
		{"Queue.Redis.DB", func(c Config) any { return c.Queue.Redis.DB }, "0"},
		{"Queue.Redis.TLS", func(c Config) any { return c.Queue.Redis.TLS }, false},
		{"Queue.Encrypt", func(c Config) any { return c.Queue.Encrypt }, false},
		{"Queue.SigningKey", func(c Config) any { return c.Queue.SigningKey }, ""},

		{"Storage.Default", func(c Config) any { return c.Storage.Default }, "local"},
		{"Storage local Root", func(c Config) any { return c.Storage.Disks["local"].Root }, "./storage/app"},
		{"Storage disk count", func(c Config) any { return len(c.Storage.Disks) }, 1},

		{"DB.Driver", func(c Config) any { return c.DB.Driver }, ""},
		{"DB.Host", func(c Config) any { return c.DB.Host }, "127.0.0.1"},
		{"DB.Port", func(c Config) any { return c.DB.Port }, ""},
		{"DB.MaxOpenConns", func(c Config) any { return c.DB.MaxOpenConns }, 100},
		{"DB.MaxIdleConns", func(c Config) any { return c.DB.MaxIdleConns }, 10},
		{"DB.ConnMaxLifetime", func(c Config) any { return c.DB.ConnMaxLifetime }, time.Hour},
	})
}

// An unset DB_PORT defaults per DB_CONNECTION.
func TestConfigFromEnv_DBPortDefaultPerDriver(t *testing.T) {
	clearConfigEnv(t)
	for driver, want := range map[string]string{"mysql": "3306", "postgres": "5432", "sqlite": ""} {
		t.Setenv("DB_CONNECTION", driver)
		if got := ConfigFromEnv().DB.Port; got != want {
			t.Errorf("DB_CONNECTION=%s: DB.Port = %q, want %q", driver, got, want)
		}
	}
}

// Config.Validate runs the database, cache and storage Validate methods
// and wraps each failure in ErrInvalidConfig.
func TestConfigValidate_ReachesPackageValidators(t *testing.T) {
	if err := testAppConfig().Validate(); err != nil {
		t.Fatalf("base config: Validate = %v, want nil", err)
	}
	for _, tc := range []struct {
		name   string
		adjust func(*Config)
		// want is a fragment of the package validator's own message.
		want string
	}{
		{"db", func(c *Config) {
			c.DB = orm.ManagerConfig{Driver: "sqlite", MaxOpenConns: -1}
		}, "velocity/orm: MaxIdleConns and MaxOpenConns must be non-negative"},
		{"cache", func(c *Config) {
			c.Cache = cache.SingleStoreConfig("p", cache.StoreConfig{Driver: cache.DriverFile})
		}, "velocity/cache: store \"default\" uses the file driver but Path is empty"},
		{"storage", func(c *Config) {
			c.Storage = storage.Config{Default: "s3", Disks: map[string]storage.DiskConfig{"s3": {Driver: "s3"}}}
		}, "velocity/storage: disk \"s3\" uses the s3 driver but Bucket is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testAppConfig()
			tc.adjust(&cfg)
			err := cfg.Validate()
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Validate = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// initCache with a zero cache.Config gives a working memory default store.
func TestInitCache_ZeroConfigIsMemoryDefault(t *testing.T) {
	m := initCache(cache.Config{}, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	store, err := m.DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	if typ := fmt.Sprintf("%T", store); !strings.Contains(typ, "MemoryStore") {
		t.Errorf("default store is %s, want the memory store", typ)
	}
	if err := m.Put("k", "v", time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, ok := m.Get("k"); !ok || got != "v" {
		t.Fatalf("Get = (%v, %v), want (v, true)", got, ok)
	}
}

// bootstrapMaintenance builds an app from cfg, bootstraps it and returns
// the maintenance settings the Middleware callback saw.
func bootstrapMaintenance(t *testing.T, cfg Config) contract.MaintenanceConfig {
	t.Helper()
	cfg.Debug = false
	a, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	var got contract.MaintenanceConfig
	called := false
	a.Middleware(func(m *chain.MiddlewareStack) {
		called = true
		got = m.Maintenance()
	})
	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !called {
		t.Fatal("Middleware callback not called")
	}
	return got
}

// Bootstrap passes Config.Maintenance to the middleware stack, with an
// empty AppKey defaulted to Config.Key and a set one kept.
func TestBootstrap_MaintenanceAppKey(t *testing.T) {
	t.Run("defaulted to Key", func(t *testing.T) {
		cfg := testAppConfig()
		cfg.Key = "app-key"
		cfg.Maintenance = contract.MaintenanceConfig{Root: "/srv/app", ExcludePaths: []string{"/health"}}
		got := bootstrapMaintenance(t, cfg)
		if got.AppKey != "app-key" {
			t.Errorf("AppKey = %q, want app-key", got.AppKey)
		}
		if got.Root != "/srv/app" || len(got.ExcludePaths) != 1 || got.ExcludePaths[0] != "/health" {
			t.Errorf("Maintenance = %+v, want Root /srv/app and ExcludePaths [/health]", got)
		}
	})
	t.Run("kept when set", func(t *testing.T) {
		cfg := testAppConfig()
		cfg.Key = "app-key"
		cfg.Maintenance = contract.MaintenanceConfig{Root: "/srv/app", AppKey: "own-key"}
		if got := bootstrapMaintenance(t, cfg); got.AppKey != "own-key" {
			t.Errorf("AppKey = %q, want own-key", got.AppKey)
		}
	})
}
