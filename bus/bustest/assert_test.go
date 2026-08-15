package bustest

import (
	"testing"

	"github.com/velocitykode/velocity/bus"
)

// recordingTB captures failures instead of failing the enclosing test.
type recordingTB struct {
	testing.TB
	failed bool
}

func (r *recordingTB) Helper()               {}
func (r *recordingTB) Error(args ...any)     { r.failed = true }
func (r *recordingTB) Errorf(string, ...any) { r.failed = true }

type createUser struct{ Name string }
type sendEmail struct{}

func TestSyncAssertions(t *testing.T) {
	f := bus.NewFakeBus()
	if err := f.Dispatch(createUser{Name: "alice"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	AssertDispatched(t, f, createUser{}, nil)
	AssertDispatched(t, f, createUser{}, func(c bus.Command) bool {
		return c.(createUser).Name == "alice"
	})
	AssertDispatchedTimes(t, f, createUser{}, 1)
	AssertNotDispatched(t, f, sendEmail{})

	rec := &recordingTB{}
	AssertDispatched(rec, f, sendEmail{}, nil)
	AssertDispatchedTimes(rec, f, createUser{}, 2)
	AssertNotDispatched(rec, f, createUser{})
	AssertNothingDispatched(rec, f)
	if !rec.failed {
		t.Error("sync assertions should fail on mismatches")
	}

	AssertNothingDispatched(t, bus.NewFakeBus())
}

func TestAsyncAssertions(t *testing.T) {
	f := bus.NewFakeBus()
	if err := f.DispatchAsync(sendEmail{}); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}

	AssertAsyncDispatched(t, f, sendEmail{}, nil)
	AssertAsyncDispatchedTimes(t, f, sendEmail{}, 1)
	AssertAsyncNotDispatched(t, f, createUser{})

	rec := &recordingTB{}
	AssertAsyncDispatched(rec, f, createUser{}, nil)
	AssertAsyncDispatchedTimes(rec, f, sendEmail{}, 2)
	AssertAsyncNotDispatched(rec, f, sendEmail{})
	AssertNothingAsyncDispatched(rec, f)
	if !rec.failed {
		t.Error("async assertions should fail on mismatches")
	}

	AssertNothingAsyncDispatched(t, bus.NewFakeBus())
}
