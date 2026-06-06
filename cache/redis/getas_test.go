package redis

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/cache"
)

type getAsUser struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// TestGetAs_Redis exercises cache.GetAs against the JSON-serializing redis
// store: a struct must come back concrete (not map) and an int must come back
// as the requested integer type (not float64).
func TestGetAs_Redis(t *testing.T) {
	store, mr := newTestRedisStore(t, "getas")
	defer mr.Close()
	defer func() { _ = store.Shutdown(context.Background()) }()
	ctx := context.Background()

	want := getAsUser{Name: "ada", Age: 36}
	if err := store.PutCtx(ctx, "u", want, time.Minute); err != nil {
		t.Fatalf("PutCtx: %v", err)
	}
	got, ok := cache.GetAsWithContext[getAsUser](ctx, store, "u")
	if !ok || got != want {
		t.Fatalf("GetAs struct: got %+v ok=%v, want %+v", got, ok, want)
	}

	if err := store.PutCtx(ctx, "n", 42, time.Minute); err != nil {
		t.Fatalf("PutCtx: %v", err)
	}
	n, ok := cache.GetAsWithContext[int64](ctx, store, "n")
	if !ok || n != 42 {
		t.Fatalf("GetAs[int64]: got %v ok=%v, want 42", n, ok)
	}
}
