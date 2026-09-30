package cache

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// containedChild records its Shutdown and, when told to, panics in it.
type containedChild struct {
	Store
	panics bool
	calls  int
}

func (c *containedChild) Shutdown(context.Context) error {
	c.calls++
	if c.panics {
		panic("child shutdown panicked")
	}
	return nil
}

// A child whose Shutdown panics is contained: the manager's Shutdown does
// not panic, every other child is still shut down, and the panic is the
// returned error.
func TestManagerShutdown_ContainsAPanickingChild(t *testing.T) {
	m := NewManager(&Config{})
	bad := &containedChild{panics: true}
	good := []*containedChild{{}, {}, {}}
	m.stores["bad"] = bad
	for i, c := range good {
		m.stores[string(rune('a'+i))] = c
	}

	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("Shutdown panicked: %v", p)
			}
		}()
		err = m.Shutdown(context.Background())
	}()
	if bad.calls != 1 {
		t.Errorf("panicking child Shutdown calls = %d, want 1", bad.calls)
	}
	for i, c := range good {
		if c.calls != 1 {
			t.Errorf("child %d Shutdown calls = %d, want 1: a panicking sibling skipped it", i, c.calls)
		}
	}
	var pe *panicerr.Error
	if !errors.As(err, &pe) || pe.Recovered() != "child shutdown panicked" {
		t.Fatalf("Shutdown = %v, want the child's panic as its error", err)
	}
}
