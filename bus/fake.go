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
// it; a nil callback matches on type alone. callback runs without the fake's
// lock, so it may dispatch on the fake.
func (f *FakeBus) AssertDispatched(cmd Command, callback func(Command) bool) error {
	if matchingCommand(f.GetDispatched(), cmd, callback) {
		return nil
	}
	return fmt.Errorf("expected command %T to be dispatched, but it was not", cmd)
}

// AssertDispatchedTimes asserts that a command type was dispatched exactly n times.
func (f *FakeBus) AssertDispatchedTimes(cmd Command, n int) error {
	if count := countCommands(f.GetDispatched(), cmd); count != n {
		return fmt.Errorf("expected command %T to be dispatched %d times, got %d", cmd, n, count)
	}
	return nil
}

// AssertNotDispatched asserts that a command type was never dispatched.
func (f *FakeBus) AssertNotDispatched(cmd Command) error {
	if countCommands(f.GetDispatched(), cmd) > 0 {
		return fmt.Errorf("expected command %T not to be dispatched, but it was", cmd)
	}
	return nil
}

// AssertNothingDispatched asserts that no commands were dispatched.
func (f *FakeBus) AssertNothingDispatched() error {
	if n := len(f.GetDispatched()); n > 0 {
		return fmt.Errorf("expected no commands dispatched, got %d", n)
	}
	return nil
}

// AssertAsyncDispatched asserts that a command type was dispatched async at
// least once. When callback is non-nil, a matching command must also satisfy
// it; a nil callback matches on type alone. callback runs without the fake's
// lock, so it may dispatch on the fake.
func (f *FakeBus) AssertAsyncDispatched(cmd Command, callback func(Command) bool) error {
	if matchingCommand(f.GetAsyncDispatched(), cmd, callback) {
		return nil
	}
	return fmt.Errorf("expected command %T to be async dispatched, but it was not", cmd)
}

// AssertAsyncDispatchedTimes asserts that a command type was dispatched async
// exactly n times.
func (f *FakeBus) AssertAsyncDispatchedTimes(cmd Command, n int) error {
	if count := countCommands(f.GetAsyncDispatched(), cmd); count != n {
		return fmt.Errorf("expected command %T to be async dispatched %d times, got %d", cmd, n, count)
	}
	return nil
}

// AssertAsyncNotDispatched asserts that a command type was never dispatched async.
func (f *FakeBus) AssertAsyncNotDispatched(cmd Command) error {
	if countCommands(f.GetAsyncDispatched(), cmd) > 0 {
		return fmt.Errorf("expected command %T not to be async dispatched, but it was", cmd)
	}
	return nil
}

// AssertNothingAsyncDispatched asserts that no commands were dispatched async.
func (f *FakeBus) AssertNothingAsyncDispatched() error {
	if n := len(f.GetAsyncDispatched()); n > 0 {
		return fmt.Errorf("expected no commands async dispatched, got %d", n)
	}
	return nil
}

// matchingCommand reports whether one of recorded has cmd's type and, when
// callback is non-nil, satisfies it.
func matchingCommand(recorded []Command, cmd Command, callback func(Command) bool) bool {
	cmdType := reflect.TypeOf(cmd)
	for _, d := range recorded {
		if reflect.TypeOf(d) == cmdType && (callback == nil || callback(d)) {
			return true
		}
	}
	return false
}

// countCommands returns how many of recorded have cmd's type.
func countCommands(recorded []Command, cmd Command) int {
	cmdType := reflect.TypeOf(cmd)
	count := 0
	for _, d := range recorded {
		if reflect.TypeOf(d) == cmdType {
			count++
		}
	}
	return count
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
