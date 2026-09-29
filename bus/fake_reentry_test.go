package bus

import (
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

type reentryCmd struct{}

// An assertion callback runs without the fake's lock, so a callback that
// records another dispatch on the same fake does not deadlock.
func TestFakeBus_AssertCallbackMayDispatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record func(f *FakeBus)
		assert func(f *FakeBus, cb func(Command) bool) error
	}{
		{"AssertDispatched", func(f *FakeBus) { _ = f.Dispatch(reentryCmd{}) },
			func(f *FakeBus, cb func(Command) bool) error { return f.AssertDispatched(reentryCmd{}, cb) }},
		{"AssertAsyncDispatched", func(f *FakeBus) { _ = f.DispatchAsync(reentryCmd{}) },
			func(f *FakeBus, cb func(Command) bool) error { return f.AssertAsyncDispatched(reentryCmd{}, cb) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFakeBus()
			tc.record(f)
			var err error
			if p := hostile.Within(t, hostile.Deadline, func() {
				err = tc.assert(f, func(Command) bool {
					_ = f.Dispatch(reentryCmd{})
					_ = f.DispatchAsync(reentryCmd{})
					return true
				})
			}); p != nil {
				t.Fatalf("%s panicked: %v", tc.name, p)
			}
			if err != nil {
				t.Fatalf("%s = %v", tc.name, err)
			}
		})
	}
}
