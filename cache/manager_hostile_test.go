package cache

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

var hostileDriverSeq atomic.Int64

// registerHostileDriver registers a store factory that runs code before it
// builds a memory store, and removes it when the test ends.
func registerHostileDriver(t *testing.T, code *hostile.Code, builds *atomic.Int32) string {
	t.Helper()
	name := "hostile-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + string(rune('a'+hostileDriverSeq.Add(1)%26))
	driverRegistry.Register(name, func(_ context.Context, cfg StoreConfig) (Store, error) {
		if builds != nil {
			builds.Add(1)
		}
		code.Run()
		return drivers.NewMemoryStore(cfg.Prefix), nil
	})
	t.Cleanup(func() { driverRegistry.Override(name, nil) })
	return name
}

func hostileManager(driver string) *Manager {
	return NewManager(&Config{
		Default: "main",
		Stores: map[string]StoreConfig{
			"main":  {Driver: driver},
			"other": {Driver: "memory"},
		},
	})
}

// The store factory is user code (a third-party driver, or one whose
// startup warning goes through the app logger). For every manager entry
// point it may call back into, a factory that panics, blocks, or makes
// that call must not deadlock the manager, and the store builds afterwards.
func TestManager_StoreBuildWithHostileFactory(t *testing.T) {
	entries := map[string]func(m *Manager){
		// From inside the build it returns an error at once (see
		// TestManager_StoreFromInsideItsOwnBuild); beside a blocked build
		// it waits for that build, so the block rows skip it.
		"Store same name": func(m *Manager) { _, _ = m.Store("main") },
		"Store other":     func(m *Manager) { _, _ = m.Store("other") },
		"SetLogger":       func(m *Manager) { m.SetLogger(nil) },
		"Shutdown":        func(m *Manager) { _ = m.Shutdown(context.Background()) },
		"Get":             func(m *Manager) { _, _ = m.Get("k") },
	}
	for _, mode := range hostile.Modes() {
		for name, entry := range entries {
			t.Run(mode.String()+"/"+name, func(t *testing.T) {
				fallbacklogtest.Capture(t)
				var m *Manager
				code := hostile.New(t, mode, func() { entry(m) })
				m = hostileManager(registerHostileDriver(t, code, nil))
				build := func() { _, _ = m.Store("main") }

				switch mode {
				case hostile.Block:
					go build()
					<-code.Entered()
					if name != "Store same name" && name != "Get" {
						hostile.Within(t, hostile.Deadline, func() { entry(m) })
					}
				default:
					hostile.Within(t, hostile.Deadline, build)
				}
				code.Release()
				code.Disarm()
				hostile.Within(t, hostile.Deadline, func() {
					var store Store
					var err error
					// A build in flight at Release may still publish or
					// be discarded by a Shutdown; retry until it settles.
					for range 100 {
						if store, err = m.Store("main"); err == nil {
							break
						}
						time.Sleep(time.Millisecond)
					}
					if err != nil || store == nil {
						t.Errorf("Store after a retry = %v, %v", store, err)
					}
				})
			})
		}
	}
}

// A lookup of a store from inside its own build gets an error at once.
func TestManager_StoreFromInsideItsOwnBuild(t *testing.T) {
	var m *Manager
	var inner error
	code := hostile.New(t, hostile.Reenter, func() { _, inner = m.Store("main") })
	m = hostileManager(registerHostileDriver(t, code, nil))
	hostile.Within(t, hostile.Deadline, func() {
		if _, err := m.Store("main"); err != nil {
			t.Errorf("outer Store: %v", err)
		}
	})
	if inner == nil || !strings.Contains(inner.Error(), `velocity/cache: store "main": requested from inside its own build`) {
		t.Fatalf("inner Store err = %v, want the re-entry error", inner)
	}
}

// Concurrent first uses of one name build it once and all get that store.
func TestManager_StoreBuildsOnceUnderConcurrency(t *testing.T) {
	var builds atomic.Int32
	code := hostile.New(t, hostile.Block, nil)
	m := hostileManager(registerHostileDriver(t, code, &builds))
	var wg sync.WaitGroup
	stores := make(chan Store, 32)
	for range 32 {
		wg.Go(func() {
			s, err := m.Store("main")
			if err != nil {
				t.Error(err)
			}
			stores <- s
		})
	}
	<-code.Entered()
	time.Sleep(20 * time.Millisecond)
	code.Release()
	wg.Wait()
	close(stores)
	var first Store
	for s := range stores {
		if first == nil {
			first = s
		}
		if s != first {
			t.Fatal("callers got different stores")
		}
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("the factory ran %d times, want 1", n)
	}
}

// A store whose build outlives a Shutdown that started meanwhile is not
// published into the emptied manager.
func TestManager_StoreBuiltAcrossShutdownIsNotPublished(t *testing.T) {
	code := hostile.New(t, hostile.Block, nil)
	m := hostileManager(registerHostileDriver(t, code, nil))
	errc := make(chan error, 1)
	go func() {
		_, err := m.Store("main")
		errc <- err
	}()
	<-code.Entered()
	hostile.Within(t, hostile.Deadline, func() { _ = m.Shutdown(context.Background()) })
	code.Release()
	if err := <-errc; err == nil || !strings.Contains(err.Error(), "shut down while the store was built") {
		t.Fatalf("Store across Shutdown err = %v", err)
	}
	m.mu.RLock()
	n := len(m.stores)
	m.mu.RUnlock()
	if n != 0 {
		t.Fatalf("%d stores published after Shutdown", n)
	}
}
