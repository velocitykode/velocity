package mail

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// completionChild is a child whose Shutdown runs code (block, re-enter),
// then returns err.
type completionChild struct {
	Mailer
	code  *hostile.Code
	err   error
	calls atomic.Int32
}

func (c *completionChild) Shutdown(context.Context) error {
	c.calls.Add(1)
	c.code.Run()
	return c.err
}

func completionManager() *Manager { return NewManager() }

func addCompletionChild(m *Manager, name string, c *completionChild) { m.SetChannel(name, c) }

// cancelled is a ctx that is already done: a Shutdown that must wait
// returns its error at once instead of waiting.
func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// Shutdown runs through teardown.Children (whose own tests cover the
// mechanism): an overlapping Shutdown waits for the run, a repeated one
// returns the run's retained result, and one from a child's Shutdown is
// refused with ErrStopFromOwnWork.
func TestShutdown_WiredThroughChildren(t *testing.T) {
	m := completionManager()
	errChild := errors.New("child failed")
	code := hostile.New(t, hostile.Block, nil)
	addCompletionChild(m, "slow", &completionChild{code: code, err: errChild})
	first := make(chan error, 1)
	go func() { first <- m.Shutdown(context.Background()) }()
	if !code.AwaitEntered(t) {
		return
	}
	if err := m.Shutdown(cancelled()); !errors.Is(err, context.Canceled) {
		t.Errorf("overlapping Shutdown = %v, want its ctx error", err)
	}
	code.Release()
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-first })
	if !errors.Is(err, errChild) {
		t.Fatalf("first Shutdown = %v, want the child's error", err)
	}
	if err := m.Shutdown(context.Background()); !errors.Is(err, errChild) {
		t.Errorf("repeated Shutdown = %v, want the retained result", err)
	}

	var nested error
	reenter := hostile.New(t, hostile.Reenter, func() { nested = m.Shutdown(context.Background()) })
	addCompletionChild(m, "reenters", &completionChild{code: reenter})
	hostile.Within(t, hostile.Deadline, func() { _ = m.Shutdown(context.Background()) })
	if !errors.Is(nested, contract.ErrStopFromOwnWork) {
		t.Errorf("Shutdown from a child's Shutdown = %v, want ErrStopFromOwnWork", nested)
	}
}
