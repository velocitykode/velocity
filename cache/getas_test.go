package cache_test

import (
	"testing"
	"time"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/cache/drivers"
)

type getAsUser struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// newGetAsStores returns one store per driver so GetAs is exercised on both the
// concrete-type path (memory) and the JSON-serializing path (file).
func newGetAsStores(t *testing.T) map[string]cache.Store {
	t.Helper()
	fs, err := drivers.NewFileStore("getas", t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return map[string]cache.Store{
		"memory": drivers.NewMemoryStore("getas"),
		"file":   fs,
	}
}

func TestGetAs_Struct(t *testing.T) {
	for name, store := range newGetAsStores(t) {
		t.Run(name, func(t *testing.T) {
			want := getAsUser{Name: "ada", Age: 36}
			if err := store.Put("u", want, time.Minute); err != nil {
				t.Fatalf("Put: %v", err)
			}
			got, ok := cache.GetAs[getAsUser](store, "u")
			if !ok {
				t.Fatal("GetAs: expected hit")
			}
			if got != want {
				t.Fatalf("GetAs struct: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestGetAs_Int(t *testing.T) {
	for name, store := range newGetAsStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := store.Put("n", 42, time.Minute); err != nil {
				t.Fatalf("Put: %v", err)
			}
			// int and int64 must both come back as the requested integer type,
			// not the float64 that bare JSON decoding would yield.
			gotI, ok := cache.GetAs[int](store, "n")
			if !ok || gotI != 42 {
				t.Fatalf("GetAs[int]: got %v ok=%v, want 42", gotI, ok)
			}
			got64, ok := cache.GetAs[int64](store, "n")
			if !ok || got64 != 42 {
				t.Fatalf("GetAs[int64]: got %v ok=%v, want 42", got64, ok)
			}
		})
	}
}

func TestGetAs_Miss(t *testing.T) {
	for name, store := range newGetAsStores(t) {
		t.Run(name, func(t *testing.T) {
			if v, ok := cache.GetAs[getAsUser](store, "absent"); ok {
				t.Fatalf("GetAs on absent key: expected miss, got %+v", v)
			}
		})
	}
}

func TestGetAs_TypeMismatch(t *testing.T) {
	for name, store := range newGetAsStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := store.Put("s", "not-a-number", time.Minute); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if v, ok := cache.GetAs[int](store, "s"); ok {
				t.Fatalf("GetAs[int] on string: expected miss, got %v", v)
			}
		})
	}
}
