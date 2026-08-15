package eventstest

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/events"
)

// recordingTB captures failures instead of failing the enclosing test.
type recordingTB struct {
	testing.TB
	failed bool
}

func (r *recordingTB) Helper()               {}
func (r *recordingTB) Error(args ...any)     { r.failed = true }
func (r *recordingTB) Errorf(string, ...any) { r.failed = true }

type userRegistered struct{ Name string }
type orderShipped struct{}

func dispatchedFake(t *testing.T, evs ...any) *events.FakeDispatcher {
	t.Helper()
	f := events.NewFakeDispatcher()
	for _, ev := range evs {
		if err := f.Dispatch(context.Background(), ev); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
	}
	return f
}

func TestAssertDispatched(t *testing.T) {
	f := dispatchedFake(t, &userRegistered{Name: "alice"})

	AssertDispatched(t, f, &userRegistered{}, nil)
	AssertDispatched(t, f, &userRegistered{}, func(e any) bool {
		return e.(*userRegistered).Name == "alice"
	})

	rec := &recordingTB{}
	AssertDispatched(rec, f, &orderShipped{}, nil)
	if !rec.failed {
		t.Error("AssertDispatched should fail for an undispatched event")
	}
}

func TestAssertDispatchedTimes(t *testing.T) {
	f := dispatchedFake(t, &userRegistered{}, &userRegistered{})

	AssertDispatchedTimes(t, f, &userRegistered{}, 2)

	rec := &recordingTB{}
	AssertDispatchedTimes(rec, f, &userRegistered{}, 3)
	if !rec.failed {
		t.Error("AssertDispatchedTimes should fail on count mismatch")
	}
}

func TestAssertNotDispatched(t *testing.T) {
	f := dispatchedFake(t, &userRegistered{})

	AssertNotDispatched(t, f, &orderShipped{})

	rec := &recordingTB{}
	AssertNotDispatched(rec, f, &userRegistered{})
	if !rec.failed {
		t.Error("AssertNotDispatched should fail for a dispatched event")
	}
}

func TestAssertNothingDispatched(t *testing.T) {
	AssertNothingDispatched(t, dispatchedFake(t))

	rec := &recordingTB{}
	AssertNothingDispatched(rec, dispatchedFake(t, &userRegistered{}))
	if !rec.failed {
		t.Error("AssertNothingDispatched should fail when events were dispatched")
	}
}
