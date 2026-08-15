// Package bustest provides test-failing assertion helpers for bus.FakeBus,
// mirroring the queuetest and mailtest idiom: pass the test handle and the
// assertion fails the test directly, so a forgotten error check cannot
// silently pass.
package bustest

import (
	"testing"

	"github.com/velocitykode/velocity/bus"
)

// AssertDispatched fails the test if no command of cmd's type was dispatched.
// A nil match matches on type alone.
func AssertDispatched(tb testing.TB, f *bus.FakeBus, cmd bus.Command, match func(bus.Command) bool) {
	tb.Helper()
	if err := f.AssertDispatched(cmd, match); err != nil {
		tb.Error(err)
	}
}

// AssertDispatchedTimes fails the test unless commands of cmd's type were
// dispatched exactly n times.
func AssertDispatchedTimes(tb testing.TB, f *bus.FakeBus, cmd bus.Command, n int) {
	tb.Helper()
	if err := f.AssertDispatchedTimes(cmd, n); err != nil {
		tb.Error(err)
	}
}

// AssertNotDispatched fails the test if a command of cmd's type was dispatched.
func AssertNotDispatched(tb testing.TB, f *bus.FakeBus, cmd bus.Command) {
	tb.Helper()
	if err := f.AssertNotDispatched(cmd); err != nil {
		tb.Error(err)
	}
}

// AssertNothingDispatched fails the test if any command was dispatched.
func AssertNothingDispatched(tb testing.TB, f *bus.FakeBus) {
	tb.Helper()
	if err := f.AssertNothingDispatched(); err != nil {
		tb.Error(err)
	}
}

// AssertAsyncDispatched fails the test if no command of cmd's type was
// dispatched async. A nil match matches on type alone.
func AssertAsyncDispatched(tb testing.TB, f *bus.FakeBus, cmd bus.Command, match func(bus.Command) bool) {
	tb.Helper()
	if err := f.AssertAsyncDispatched(cmd, match); err != nil {
		tb.Error(err)
	}
}

// AssertAsyncDispatchedTimes fails the test unless commands of cmd's type
// were dispatched async exactly n times.
func AssertAsyncDispatchedTimes(tb testing.TB, f *bus.FakeBus, cmd bus.Command, n int) {
	tb.Helper()
	if err := f.AssertAsyncDispatchedTimes(cmd, n); err != nil {
		tb.Error(err)
	}
}

// AssertAsyncNotDispatched fails the test if a command of cmd's type was
// dispatched async.
func AssertAsyncNotDispatched(tb testing.TB, f *bus.FakeBus, cmd bus.Command) {
	tb.Helper()
	if err := f.AssertAsyncNotDispatched(cmd); err != nil {
		tb.Error(err)
	}
}

// AssertNothingAsyncDispatched fails the test if any command was dispatched
// async.
func AssertNothingAsyncDispatched(tb testing.TB, f *bus.FakeBus) {
	tb.Helper()
	if err := f.AssertNothingAsyncDispatched(); err != nil {
		tb.Error(err)
	}
}
