package bus

import (
	"context"
	"fmt"
	"reflect"
	"sync"
)

// FakeBus records dispatched commands for test assertions.
// It mirrors the API pattern of events.FakeDispatcher.
type FakeBus struct {
	mu              sync.Mutex
	dispatched      []Command
	asyncDispatched []Command
}

// NewFakeBus creates a new FakeBus for testing.
func NewFakeBus() *FakeBus {
	return &FakeBus{}
}

// Dispatch records a synchronous dispatch.
func (f *FakeBus) Dispatch(cmd Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, cmd)
	return nil
}

// DispatchAsync records an async dispatch.
func (f *FakeBus) DispatchAsync(cmd Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asyncDispatched = append(f.asyncDispatched, cmd)
	return nil
}

// DispatchAsyncCtx records an async dispatch, ignoring ctx. It exists so
// *FakeBus satisfies the Dispatcher interface alongside the real *Bus.
func (f *FakeBus) DispatchAsyncCtx(_ context.Context, cmd Command) error {
	return f.DispatchAsync(cmd)
}

// AssertDispatched asserts that a command of the given type was dispatched at
// least once. When callback is non-nil, a matching command must also satisfy
// it; a nil callback matches on type alone.
func (f *FakeBus) AssertDispatched(cmd Command, callback func(Command) bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmdType := reflect.TypeOf(cmd)
	for _, d := range f.dispatched {
		if reflect.TypeOf(d) == cmdType {
			if callback == nil || callback(d) {
				return nil
			}
		}
	}
	return fmt.Errorf("expected command %T to be dispatched, but it was not", cmd)
}

// AssertDispatchedTimes asserts that a command type was dispatched exactly n times.
func (f *FakeBus) AssertDispatchedTimes(cmd Command, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmdType := reflect.TypeOf(cmd)
	count := 0
	for _, d := range f.dispatched {
		if reflect.TypeOf(d) == cmdType {
			count++
		}
	}
	if count != n {
		return fmt.Errorf("expected command %T to be dispatched %d times, got %d", cmd, n, count)
	}
	return nil
}

// AssertNotDispatched asserts that a command type was never dispatched.
func (f *FakeBus) AssertNotDispatched(cmd Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmdType := reflect.TypeOf(cmd)
	for _, d := range f.dispatched {
		if reflect.TypeOf(d) == cmdType {
			return fmt.Errorf("expected command %T not to be dispatched, but it was", cmd)
		}
	}
	return nil
}

// AssertNothingDispatched asserts that no commands were dispatched.
func (f *FakeBus) AssertNothingDispatched() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.dispatched) > 0 {
		return fmt.Errorf("expected no commands dispatched, got %d", len(f.dispatched))
	}
	return nil
}

// AssertAsyncDispatched asserts that a command type was dispatched async at
// least once. When callback is non-nil, a matching command must also satisfy
// it; a nil callback matches on type alone.
func (f *FakeBus) AssertAsyncDispatched(cmd Command, callback func(Command) bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmdType := reflect.TypeOf(cmd)
	for _, d := range f.asyncDispatched {
		if reflect.TypeOf(d) == cmdType {
			if callback == nil || callback(d) {
				return nil
			}
		}
	}
	return fmt.Errorf("expected command %T to be async dispatched, but it was not", cmd)
}

// AssertAsyncDispatchedTimes asserts that a command type was dispatched async
// exactly n times.
func (f *FakeBus) AssertAsyncDispatchedTimes(cmd Command, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmdType := reflect.TypeOf(cmd)
	count := 0
	for _, d := range f.asyncDispatched {
		if reflect.TypeOf(d) == cmdType {
			count++
		}
	}
	if count != n {
		return fmt.Errorf("expected command %T to be async dispatched %d times, got %d", cmd, n, count)
	}
	return nil
}

// AssertAsyncNotDispatched asserts that a command type was never dispatched async.
func (f *FakeBus) AssertAsyncNotDispatched(cmd Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmdType := reflect.TypeOf(cmd)
	for _, d := range f.asyncDispatched {
		if reflect.TypeOf(d) == cmdType {
			return fmt.Errorf("expected command %T not to be async dispatched, but it was", cmd)
		}
	}
	return nil
}

// AssertNothingAsyncDispatched asserts that no commands were dispatched async.
func (f *FakeBus) AssertNothingAsyncDispatched() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.asyncDispatched) > 0 {
		return fmt.Errorf("expected no commands async dispatched, got %d", len(f.asyncDispatched))
	}
	return nil
}

// GetDispatched returns all synchronously dispatched commands.
func (f *FakeBus) GetDispatched() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]Command, len(f.dispatched))
	copy(cp, f.dispatched)
	return cp
}

// GetAsyncDispatched returns all asynchronously dispatched commands.
func (f *FakeBus) GetAsyncDispatched() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]Command, len(f.asyncDispatched))
	copy(cp, f.asyncDispatched)
	return cp
}

// ClearDispatched clears all recorded dispatches.
func (f *FakeBus) ClearDispatched() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = nil
	f.asyncDispatched = nil
}
