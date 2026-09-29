package drain_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/drain"
)

// internal/drain sits under the router's graph (through the scheduler),
// so it imports only the standard library, internal/goroutine and async.
func TestDrainImportsOnlyItsLeaves(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	allowed := map[string]bool{
		"github.com/velocitykode/velocity/internal/goroutine": true,
		"github.com/velocitykode/velocity/async":              true,
	}
	for _, imp := range strings.Fields(string(out)) {
		if strings.Contains(imp, ".") && !allowed[imp] {
			t.Errorf("internal/drain imports %s; it may import only the standard library, internal/goroutine and async", imp)
		}
	}
}

// Drain closes the drained channel after the stop, and Await returns nil
// for it; a stop's goroutine is Nested while it runs, and only then.
func TestCoordinator_DrainAndNested(t *testing.T) {
	var c drain.Coordinator
	if c.Ended() != nil {
		t.Fatal("Ended before a stop: want nil")
	}
	d := c.Begin()
	var nested bool
	c.Drain(d, func() { nested = c.Nested() })
	if !nested {
		t.Error("the stop was not Nested while it ran")
	}
	if c.Nested() {
		t.Error("Nested after the stop returned")
	}
	if !drain.Closed(d) || c.Ended() != d {
		t.Fatal("drained not closed, or Ended is not it")
	}
	if err := c.Await(context.Background(), d, nil); err != nil {
		t.Errorf("Await on a closed drain = %v", err)
	}
}

// At its ctx, Await returns the ctx error and starts force, as stop work,
// without waiting on it; with a nil force it only returns.
func TestCoordinator_AwaitForcesAtItsDeadline(t *testing.T) {
	var c drain.Coordinator
	d := c.Begin()
	var forced atomic.Bool
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Await(ctx, d, func() { forced.Store(c.Nested()) }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Await = %v, want the deadline", err)
	}
	for range 200 {
		if forced.Load() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !forced.Load() {
		t.Error("force did not run as stop work")
	}
	if err := c.Await(ctx, d, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Await without force = %v, want the deadline", err)
	}
}
