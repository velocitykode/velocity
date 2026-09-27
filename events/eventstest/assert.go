// Package eventstest provides test-failing assertion helpers for
// events.FakeDispatcher, mirroring the queuetest and mailtest idiom: pass the
// test handle and the assertion fails the test directly, so a forgotten error
// check cannot silently pass.
package eventstest

import (
	"testing"

	"github.com/velocitykode/velocity/events"
)

// AssertDispatched fails the test if no recorded event key selects
// satisfies match. A nil match matches on key alone. A string key selects
// events by name (or pattern); any other value selects events of its type
// (see events.FakeDispatcher.AssertDispatched).
func AssertDispatched(tb testing.TB, f *events.FakeDispatcher, key any, match func(any) bool) {
	tb.Helper()
	if err := f.AssertDispatched(key, match); err != nil {
		tb.Error(err)
	}
}

// AssertDispatchedTimes fails the test unless events key selects were
// recorded exactly n times.
func AssertDispatchedTimes(tb testing.TB, f *events.FakeDispatcher, key any, n int) {
	tb.Helper()
	if err := f.AssertDispatchedTimes(key, n); err != nil {
		tb.Error(err)
	}
}

// AssertNotDispatched fails the test if an event key selects was recorded.
func AssertNotDispatched(tb testing.TB, f *events.FakeDispatcher, key any) {
	tb.Helper()
	if err := f.AssertNotDispatched(key); err != nil {
		tb.Error(err)
	}
}

// AssertNothingDispatched fails the test if any event was recorded.
func AssertNothingDispatched(tb testing.TB, f *events.FakeDispatcher) {
	tb.Helper()
	if err := f.AssertNothingDispatched(); err != nil {
		tb.Error(err)
	}
}
