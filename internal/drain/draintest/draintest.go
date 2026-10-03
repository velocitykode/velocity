// Package draintest is the contract every drain owner keeps, as one test
// table each owner runs against itself: a component that admits work and
// stops through internal/drain (router requests and event pools, the ORM
// manager, the local storage driver). It imports only the standard
// library and contract.
package draintest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// Owner is one fresh instance of a component under the contract.
type Owner struct {
	// Hold starts one unit of work that runs until release is called and
	// returns once the unit is admitted and running (evidence, not time).
	Hold func(t *testing.T) (release func())
	// Stop is the component's stop.
	Stop func(ctx context.Context) error
	// Refused tries one new unit of work after a stop began and reports
	// whether the component refused it without running it.
	Refused func(t *testing.T) bool
	// StopFromOwnWork calls Stop from inside a unit of the component's own
	// work and returns that Stop's error, once the unit returned.
	StopFromOwnWork func(t *testing.T) error
}

// wait bounds a call the contract expects to return: a stop past its own
// ctx, or one with nothing left to wait for.
const wait = 5 * time.Second

// Run runs the contract against owners built by build, one per case.
func Run(t *testing.T, build func(t *testing.T) Owner) {
	t.Helper()
	t.Run("IdleStopRetainsNil", func(t *testing.T) {
		o := build(t)
		for i := 0; i < 2; i++ {
			if err := within(t, func() error { return o.Stop(context.Background()) }); err != nil {
				t.Fatalf("stop %d of an idle owner = %v, want nil", i+1, err)
			}
		}
	})
	t.Run("DeadlineIsNotCompletion", func(t *testing.T) {
		o := build(t)
		release := o.Hold(t)
		defer release()
		for i := 0; i < 2; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			err := within(t, func() error { return o.Stop(ctx) })
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stop %d with work held = %v, want context.DeadlineExceeded", i+1, err)
			}
		}
		release()
		if err := within(t, func() error { return o.Stop(context.Background()) }); err != nil {
			t.Fatalf("stop after the work released = %v, want the retained nil", err)
		}
	})
	t.Run("RefusesWorkOnceStopping", func(t *testing.T) {
		o := build(t)
		release := o.Hold(t)
		defer release()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = o.Stop(ctx)
		if !o.Refused(t) {
			t.Fatal("work arriving once the stop began was admitted")
		}
		release()
		if err := within(t, func() error { return o.Stop(context.Background()) }); err != nil {
			t.Fatalf("stop after the work released = %v, want nil", err)
		}
	})
	t.Run("StopFromOwnWorkIsRefused", func(t *testing.T) {
		o := build(t)
		err := o.StopFromOwnWork(t)
		if !errors.Is(err, contract.ErrStopFromOwnWork) {
			t.Fatalf("stop from the owner's own work = %v, want contract.ErrStopFromOwnWork", err)
		}
		if err := within(t, func() error { return o.Stop(context.Background()) }); err != nil {
			t.Fatalf("stop from outside after the refused one = %v, want nil", err)
		}
	})
}

// within runs fn and fails the test when it does not return within wait.
func within(t *testing.T, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }() //safe-goroutine: the bounded call; its result is read below
	select {
	case err := <-done:
		return err
	case <-time.After(wait):
		t.Fatalf("stop did not return within %v", wait)
		return nil
	}
}
