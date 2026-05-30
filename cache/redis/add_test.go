package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// TestRedisStore_Add covers the SETNX-based Add primitive on the Redis driver.
func TestRedisStore_Add(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()
	store, err := NewRedisStore(context.Background(), "addtest", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	defer func() { _ = store.Shutdown(context.Background()) }()

	t.Run("InsertsWhenAbsent", func(t *testing.T) {
		inserted, err := store.Add("k1", "v", time.Hour)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if !inserted {
			t.Fatal("Add must return true when the key is absent")
		}
	})

	t.Run("RejectsWhenPresent", func(t *testing.T) {
		if err := store.Put("k2", "first", time.Hour); err != nil {
			t.Fatalf("Put: %v", err)
		}
		inserted, err := store.Add("k2", "second", time.Hour)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if inserted {
			t.Fatal("Add must return false when the key already exists")
		}
		got, _ := store.Get("k2")
		if got != "first" {
			t.Errorf("Add must not overwrite; Get = %v, want first", got)
		}
	})

	t.Run("AddCtxRespectsCancelledCtx", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := store.AddCtx(ctx, "ctx-cancel", "v", time.Hour)
		if err == nil {
			t.Fatal("AddCtx on cancelled ctx must surface an error")
		}
	})
}
