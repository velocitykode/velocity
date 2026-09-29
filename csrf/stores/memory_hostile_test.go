package stores

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// hostileCtx is a caller's own context type: cancelling a context derived
// from it runs the stop func its AfterFunc returned, which runs code.
type hostileCtx struct {
	context.Context
	code *hostile.Code
}

// Value answers nothing, so the standard library treats the context as a
// foreign type and registers through AfterFunc.
func (hostileCtx) Value(any) any { return nil }

func (c hostileCtx) AfterFunc(func()) func() bool {
	return func() bool {
		c.code.Run()
		return true
	}
}

// Cancelling the cleanup goroutine's context runs the caller's context
// code, so Start and Shutdown do it with the lifecycle lock released: a
// context that panics, blocks or calls back into the store leaves Start
// and Shutdown working, and a retry stops the cleanup goroutine.
func TestMemoryStore_LifecycleRunsContextCodeUnlocked(t *testing.T) {
	entries := []struct {
		name string
		call func(s *MemoryStore)
	}{
		{"Shutdown", func(s *MemoryStore) { _ = s.Shutdown(context.Background()) }},
		{"Start again", func(s *MemoryStore) { s.Start(context.Background()) }},
	}
	for _, mode := range hostile.Modes() {
		for _, e := range entries {
			t.Run(mode.String()+"/"+e.name, func(t *testing.T) {
				s := NewMemoryStore()
				code := hostile.New(t, mode, func() {
					s.Start(context.Background())
					_ = s.Shutdown(context.Background())
				})
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				s.Start(hostileCtx{Context: parent, code: code})

				call := func() { e.call(s) }
				if mode == hostile.Block {
					go func() { //safe-goroutine: the test releases the block below
						e.call(s)
					}()
					<-code.Entered()
					// Another lifecycle call must not wait on the blocked
					// context code.
					call = func() {
						s.Start(context.Background())
						_ = s.Shutdown(context.Background())
					}
				}
				if p := hostile.Within(t, hostile.Deadline, call); p != nil && mode != hostile.Panic {
					t.Fatalf("%s panicked: %v", e.name, p)
				}
				code.Release()
				code.Disarm()

				// A retry works: Shutdown returns and leaves no cleanup
				// goroutine registered.
				hostile.Within(t, hostile.Deadline, func() {
					if err := s.Shutdown(context.Background()); err != nil {
						t.Errorf("Shutdown: %v", err)
					}
				})
				hostile.Within(t, hostile.Deadline, func() {
					s.lifecycleMu.Lock()
					left := s.cancel
					s.lifecycleMu.Unlock()
					if left != nil {
						t.Error("a cleanup goroutine is still registered after Shutdown")
					}
				})
			})
		}
	}
}
