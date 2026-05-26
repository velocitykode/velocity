package drivers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// TestRedisStore_Flush_EmptyPrefix_Refuses verifies that calling Flush
// on a RedisStore with an empty prefix returns ErrCannotFlushUnprefixed
// instead of wiping the entire DB via SCAN "*" + DEL. Previously the
// code degraded to pattern "*" with no guard, so a shared Redis would
// lose every other application's data on a single misconfigured
// CACHE_PREFIX env var.
func TestRedisStore_Flush_EmptyPrefix_Refuses(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	store, err := NewRedisStore(context.Background(), "", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	defer func() { _ = store.Shutdown(context.Background()) }()

	// Seed the DB with both an app key and a "foreign" key from a
	// separate application to prove that even keys the store could
	// legitimately own are protected when the prefix is missing.
	if err := store.Put("appkey", "v", time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}
	mr.Set("other-app-key", "other-app-value")

	err = store.Flush()
	if !errors.Is(err, ErrCannotFlushUnprefixed) {
		t.Fatalf("Flush with empty prefix: err=%v, want ErrCannotFlushUnprefixed", err)
	}
	if !mr.Exists("other-app-key") {
		t.Fatal("Flush must not delete foreign keys when prefix is empty")
	}
	if !mr.Exists("appkey") {
		t.Fatal("Flush must not delete cache keys when refusing")
	}
}

// TestRedisStore_Flush_WithPrefix_OnlyClearsPrefixedKeys verifies the
// safe path: a non-empty prefix limits Flush to SCAN MATCH "prefix:*",
// leaving non-prefixed keys intact.
func TestRedisStore_Flush_WithPrefix_OnlyClearsPrefixedKeys(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	store, err := NewRedisStore(context.Background(), "myapp", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	defer func() { _ = store.Shutdown(context.Background()) }()

	if err := store.Put("k1", "v1", time.Hour); err != nil {
		t.Fatalf("Put k1: %v", err)
	}
	if err := store.Put("k2", "v2", time.Hour); err != nil {
		t.Fatalf("Put k2: %v", err)
	}
	mr.Set("other-app:key", "value-owned-by-someone-else")

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if store.Has("k1") || store.Has("k2") {
		t.Error("Flush must remove all prefixed keys")
	}
	if !mr.Exists("other-app:key") {
		t.Error("Flush must not touch keys outside the configured prefix")
	}
}

// TestRedisStore_FlushAllUnsafe_WipesEverything is the opt-in escape
// hatch contract: operators who genuinely own the whole DB can call
// FlushAllUnsafe to wipe every key including unprefixed ones.
func TestRedisStore_FlushAllUnsafe_WipesEverything(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	store, err := NewRedisStore(context.Background(), "", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	defer func() { _ = store.Shutdown(context.Background()) }()

	if err := store.Put("a", "v", time.Hour); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	mr.Set("b", "x")

	if err := store.FlushAllUnsafe(); err != nil {
		t.Fatalf("FlushAllUnsafe: %v", err)
	}
	if mr.Exists("a") || mr.Exists("b") {
		t.Error("FlushAllUnsafe must wipe every key")
	}
}
