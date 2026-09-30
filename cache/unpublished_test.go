package cache

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// startedStore is a store with a Start hook; it records its Shutdown.
type startedStore struct {
	Store
	startPanics bool
	shutdowns   atomic.Int32
	err         error
}

func (s *startedStore) Start() {
	if s.startPanics {
		panic("start panicked")
	}
}

func (s *startedStore) Shutdown(context.Context) error {
	s.shutdowns.Add(1)
	return s.err
}

func registerStore(t *testing.T, factory func() Store) string {
	t.Helper()
	name := "unpublished-" + strings.ReplaceAll(t.Name(), "/", "-")
	driverRegistry.Register(name, func(context.Context, StoreConfig) (Store, error) { return factory(), nil })
	t.Cleanup(func() { driverRegistry.Override(name, nil) })
	return name
}

// A store whose Start panics is shut down, not abandoned: the panic is the
// lookup's error, and the next lookup builds the store anew.
func TestStore_StartPanicDisposesTheStore(t *testing.T) {
	var stores []*startedStore
	name := registerStore(t, func() Store {
		s := &startedStore{Store: drivers.NewMemoryStore(""), startPanics: len(stores) == 0}
		stores = append(stores, s)
		return s
	})
	m := NewManager(&Config{Stores: map[string]StoreConfig{"main": {Driver: name}}})

	var err error
	if p := hostile.Within(t, hostile.Deadline, func() { _, err = m.Store("main") }); p != nil {
		t.Fatalf("Store panicked: %v", p)
	}
	var pe *panicerr.Error
	if !errors.As(err, &pe) || pe.Recovered() != "start panicked" {
		t.Fatalf("Store = %v, want the Start panic as its error", err)
	}
	if n := stores[0].shutdowns.Load(); n != 1 {
		t.Errorf("store whose Start panicked: Shutdown calls = %d, want 1", n)
	}
	got, err := m.Store("main")
	if err != nil || got != stores[1] {
		t.Fatalf("Store after the failed start = %v, %v; want a fresh store", got, err)
	}
	_ = m.Shutdown(context.Background())
}

// A store built across a Shutdown is shut down and the lookup's error holds
// that store's own Shutdown error, which is never dropped.
func TestStore_AcrossShutdownJoinsTheDisposeError(t *testing.T) {
	code := hostile.New(t, hostile.Block, nil)
	errDispose := errors.New("dispose failed")
	var built *startedStore
	name := registerStore(t, func() Store {
		code.Run()
		built = &startedStore{Store: drivers.NewMemoryStore(""), err: errDispose}
		return built
	})
	m := NewManager(&Config{Stores: map[string]StoreConfig{"main": {Driver: name}}})
	done := make(chan error, 1)
	go func() {
		_, err := m.Store("main")
		done <- err
	}()
	if !code.AwaitEntered(t) {
		return
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	code.Release()
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-done })
	if err == nil {
		t.Fatal("Store across a Shutdown returned no error")
	}
	if !errors.Is(err, errDispose) {
		t.Errorf("Store = %v, want it to hold the store's Shutdown error", err)
	}
	if n := built.shutdowns.Load(); n != 1 {
		t.Errorf("built store Shutdown calls = %d, want 1", n)
	}
}
