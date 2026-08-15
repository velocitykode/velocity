// Package eventstest provides test-failing assertion helpers for
// events.FakeDispatcher, mirroring the queuetest and mailtest idiom: pass the
// test handle and the assertion fails the test directly, so a forgotten error
// check cannot silently pass.
package eventstest

import (
	"testing"

	"github.com/velocitykode/velocity/events"
)

// AssertDispatched fails the test if no recorded event of eventType's type
// satisfies match. A nil match matches on type alone.
func AssertDispatched(tb testing.TB, f *events.FakeDispatcher, eventType any, match func(any) bool) {
	tb.Helper()
	if err := f.AssertDispatched(eventType, match); err != nil {
		tb.Error(err)
	}
}

// AssertDispatchedTimes fails the test unless events of eventType's type were
// recorded exactly n times.
func AssertDispatchedTimes(tb testing.TB, f *events.FakeDispatcher, eventType any, n int) {
	tb.Helper()
	if err := f.AssertDispatchedTimes(eventType, n); err != nil {
		tb.Error(err)
	}
}

// AssertNotDispatched fails the test if an event of eventType's type was
// recorded.
func AssertNotDispatched(tb testing.TB, f *events.FakeDispatcher, eventType any) {
	tb.Helper()
	if err := f.AssertNotDispatched(eventType); err != nil {
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
