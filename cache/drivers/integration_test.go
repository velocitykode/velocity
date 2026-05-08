//go:build integration

// Cache driver integration tests — run with: make test-integration
//
// These tests verify that MemoryStore, FileStore, and RedisStore all honour
// the same Cache contract. Unit tests cover each driver in isolation;
// integration parity catches semantic drift — e.g. one driver decoding
// integers as float64 (JSON round-trip) while another keeps int64, which
// silently breaks Increment for callers that depended on the in-process
// type being preserved.
//
// Redis is a real server (not miniredis) — miniredis doesn't implement
// every command go-redis uses, so parity against it isn't parity against
// what prod actually runs.
package drivers

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var cacheRequiredEnv = []string{
	"REDIS_HOST",
	"REDIS_PORT",
}

func TestMain(m *testing.M) {
	var missing []string
	for _, name := range cacheRequiredEnv {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr,
			"cache integration tests require env vars (missing: %s) — use `make test-integration`\n",
			strings.Join(missing, ", "))
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// cacheFixture pairs a cache Store with a cleanup hook. Every fixture
// starts empty so the parity suite can't leak state between subtests.
type cacheFixture struct {
	name  string
	store interface {
		Put(key string, value interface{}, ttl time.Duration) error
		Get(key string) (interface{}, bool)
		Forever(key string, value interface{}) error
		Forget(key string) error
		Has(key string) bool
		Increment(key string, value int64) (int64, error)
		Decrement(key string, value int64) (int64, error)
		Flush() error
	}
	cleanup func()
}

func cacheFixtures(t *testing.T) []cacheFixture {
	t.Helper()

	mem := NewMemoryStore("")
	mem.Start()

	file, err := NewFileStore("", t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	port, err := strconv.Atoi(os.Getenv("REDIS_PORT"))
	if err != nil {
		t.Fatalf("REDIS_PORT: %v", err)
	}
	// Use a per-test DB to stay isolated from other work on the same Redis.
	redisDB := 0
	if v := os.Getenv("REDIS_DB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			redisDB = n
		}
	}
	redisPrefix := fmt.Sprintf("integration-test-%d:", os.Getpid())
	redis, err := NewRedisStore(
		context.Background(),
		redisPrefix,
		os.Getenv("REDIS_HOST"),
		port,
		os.Getenv("REDIS_PASSWORD"),
		redisDB,
		false,
	)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}

	// Start every fixture clean so tests can't observe each other's state.
	_ = redis.Flush()

	return []cacheFixture{
		{
			name:    "memory",
			store:   mem,
			cleanup: func() { _ = mem.Shutdown(context.Background()) },
		},
		{
			name:    "file",
			store:   file,
			cleanup: func() { _ = file.Shutdown(context.Background()) },
		},
		{
			name:  "redis",
			store: redis,
			cleanup: func() {
				_ = redis.Flush()
				_ = redis.Shutdown(context.Background())
			},
		},
	}
}

// TestParity_PutGetForget runs the base contract on every driver.
func TestParity_PutGetForget(t *testing.T) {
	for _, fx := range cacheFixtures(t) {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			t.Cleanup(fx.cleanup)

			if _, ok := fx.store.Get("missing"); ok {
				t.Fatalf("precondition: key must not exist before Put")
			}

			if err := fx.store.Put("k", "v", time.Hour); err != nil {
				t.Fatalf("Put: %v", err)
			}
			got, ok := fx.store.Get("k")
			if !ok {
				t.Fatalf("Get must return found=true after Put")
			}
			if got != "v" {
				t.Errorf("Get = %v, want %q", got, "v")
			}
			if !fx.store.Has("k") {
				t.Error("Has must return true after Put")
			}

			if err := fx.store.Forget("k"); err != nil {
				t.Fatalf("Forget: %v", err)
			}
			if _, ok := fx.store.Get("k"); ok {
				t.Error("Get must return found=false after Forget")
			}
			if fx.store.Has("k") {
				t.Error("Has must return false after Forget")
			}
		})
	}
}

// TestParity_ExpiredKeyMisses verifies that a key Put with a tiny TTL is
// gone after the TTL elapses. The test sleep (250ms) is justified here:
// Redis enforces TTL in real time, so we must wait out real time; file
// and memory drivers check expiration lazily on Get.
func TestParity_ExpiredKeyMisses(t *testing.T) {
	for _, fx := range cacheFixtures(t) {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			t.Cleanup(fx.cleanup)

			if err := fx.store.Put("ephemeral", "v", 50*time.Millisecond); err != nil {
				t.Fatalf("Put: %v", err)
			}
			time.Sleep(250 * time.Millisecond) // TTL modeling — see testing/sync.go policy doc.
			if _, ok := fx.store.Get("ephemeral"); ok {
				t.Errorf("expired key must miss after TTL elapses")
			}
		})
	}
}

// TestParity_IncrementIsAtomic verifies that Increment returns the new
// numeric total and persists it. This catches drivers that:
//   - return the old value (off-by-one)
//   - lose the integer type through JSON round-trip and then fail the
//     next Increment because they see a float64
func TestParity_IncrementIsAtomic(t *testing.T) {
	for _, fx := range cacheFixtures(t) {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			t.Cleanup(fx.cleanup)

			n, err := fx.store.Increment("counter", 3)
			if err != nil {
				t.Fatalf("Increment 1: %v", err)
			}
			if n != 3 {
				t.Errorf("first Increment returned %d, want 3", n)
			}
			n, err = fx.store.Increment("counter", 4)
			if err != nil {
				t.Fatalf("Increment 2: %v", err)
			}
			if n != 7 {
				t.Errorf("second Increment returned %d, want 7", n)
			}
			n, err = fx.store.Decrement("counter", 2)
			if err != nil {
				t.Fatalf("Decrement: %v", err)
			}
			if n != 5 {
				t.Errorf("Decrement returned %d, want 5", n)
			}
		})
	}
}

// TestParity_ForeverSurvivesReads verifies Forever keys survive repeated
// reads and a Flush-less driver shutdown/restart cycle is out of scope —
// here we just confirm they don't expire within the test run.
func TestParity_ForeverSurvivesReads(t *testing.T) {
	for _, fx := range cacheFixtures(t) {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			t.Cleanup(fx.cleanup)

			if err := fx.store.Forever("k", "persistent"); err != nil {
				t.Fatalf("Forever: %v", err)
			}
			for i := 0; i < 3; i++ {
				got, ok := fx.store.Get("k")
				if !ok {
					t.Fatalf("Forever key missed on read %d", i)
				}
				if got != "persistent" {
					t.Errorf("Forever key: got %v, want %q", got, "persistent")
				}
			}
		})
	}
}

// TestParity_FlushClearsAll ensures Flush is real — a driver that no-ops
// Flush would let stale keys survive and silently poison the next run.
func TestParity_FlushClearsAll(t *testing.T) {
	for _, fx := range cacheFixtures(t) {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			t.Cleanup(fx.cleanup)

			if err := fx.store.Put("a", 1, time.Hour); err != nil {
				t.Fatalf("Put a: %v", err)
			}
			if err := fx.store.Put("b", 2, time.Hour); err != nil {
				t.Fatalf("Put b: %v", err)
			}
			if err := fx.store.Flush(); err != nil {
				t.Fatalf("Flush: %v", err)
			}
			if _, ok := fx.store.Get("a"); ok {
				t.Error("Flush must remove key a")
			}
			if _, ok := fx.store.Get("b"); ok {
				t.Error("Flush must remove key b")
			}
		})
	}
}

// TestParity_Increment_NonNumeric exercises the error path that happy-path
// parity never sees: calling Increment on a key whose current value is a
// string. All drivers must error — silent coercion to zero would let a
// caller who accidentally Put a string quietly reset their counter, which
// is the exact silent-corruption class integration parity is built to
// catch. Driver-level error strings differ (Memory/File use the velocity/
// cache prefix; Redis surfaces the upstream "ERR value is not an integer
// or out of range"), so the assertion is "non-nil error" — the fact that
// a non-numeric current value never silently becomes zero.
func TestParity_Increment_NonNumeric(t *testing.T) {
	for _, fx := range cacheFixtures(t) {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			t.Cleanup(fx.cleanup)

			if err := fx.store.Put("counter", "not-a-number", time.Hour); err != nil {
				t.Fatalf("Put: %v", err)
			}

			got, err := fx.store.Increment("counter", 5)
			if err == nil {
				t.Fatalf("%s must reject Increment on non-numeric; got %d", fx.name, got)
			}

			// For memory and file the error originates in-process, so we
			// can pin the exact wording. Redis's error string is upstream-
			// defined (and has changed between Redis versions), so we just
			// require some mention of "integer" or "numeric".
			msg := strings.ToLower(err.Error())
			switch fx.name {
			case "memory", "file":
				if !strings.Contains(msg, "not numeric") {
					t.Errorf("%s error %q missing 'not numeric'", fx.name, err)
				}
			case "redis":
				if !strings.Contains(msg, "integer") && !strings.Contains(msg, "numeric") {
					t.Errorf("redis error %q missing 'integer'/'numeric'", err)
				}
			}

			// The write must NOT have occurred — a caller retrying after
			// the error should see the original string value, not "5".
			v, ok := fx.store.Get("counter")
			if !ok {
				t.Fatal("Get after failed Increment must still find the original value")
			}
			if v != "not-a-number" {
				t.Errorf("Get after failed Increment = %v, want original string (write should not have occurred)", v)
			}
		})
	}
}

// TestError_Redis_OpsSurfaceAfterShutdown covers the mid-op failure mode
// that in-memory cache contracts don't exhibit. After Shutdown() closes the
// connection pool, every subsequent op must surface an error rather than
// silently no-op (which would let a caller think the write succeeded).
func TestError_Redis_OpsSurfaceAfterShutdown(t *testing.T) {
	port, err := strconv.Atoi(os.Getenv("REDIS_PORT"))
	if err != nil {
		t.Fatalf("REDIS_PORT: %v", err)
	}
	redisPrefix := fmt.Sprintf("integration-test-shutdown-%d:", os.Getpid())
	redis, err := NewRedisStore(
		context.Background(),
		redisPrefix,
		os.Getenv("REDIS_HOST"),
		port,
		os.Getenv("REDIS_PASSWORD"),
		0,
		false,
	)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}

	// Baseline: confirm the store works before we tear it down.
	if err := redis.Put("pre", "alive", time.Hour); err != nil {
		t.Fatalf("Put before shutdown: %v", err)
	}
	if _, ok := redis.Get("pre"); !ok {
		t.Fatal("Get must succeed before shutdown")
	}
	_ = redis.Flush()

	// Close the connection pool; every subsequent network op must error.
	if err := redis.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if err := redis.Put("post", "value", time.Hour); err == nil {
		t.Error("Put after Shutdown must error (connection pool closed)")
	}
	if _, ok := redis.Get("post"); ok {
		t.Error("Get after Shutdown must return found=false")
	}
	if _, err := redis.Increment("counter", 1); err == nil {
		t.Error("Increment after Shutdown must error")
	}
	if err := redis.Flush(); err == nil {
		t.Error("Flush after Shutdown must error (scan op needs an open pool)")
	}
}

// TestError_Redis_UnreachableHost verifies that NewRedisStore fails fast
// when the server is unreachable, rather than returning a store that looks
// healthy until the first op. The mid-op parity above pins post-Shutdown
// behavior; this pins the no-server-at-all path.
func TestError_Redis_UnreachableHost(t *testing.T) {
	// Port 1 is reserved by IANA and never a real Redis — a connection
	// attempt fails immediately on Linux/macOS.
	_, err := NewRedisStore(
		context.Background(),
		"unreachable:",
		"127.0.0.1",
		1,
		"",
		0,
		false,
	)
	if err == nil {
		t.Fatal("NewRedisStore must fail fast when server is unreachable")
	}
	if !strings.Contains(err.Error(), "failed to connect") {
		t.Errorf("error %q missing 'failed to connect'", err)
	}
}
