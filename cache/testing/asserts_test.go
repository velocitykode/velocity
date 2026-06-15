package testing

import (
	"testing"
	"time"
)

// stubTB is a minimal testing.TB used to capture whether an assertion helper
// reported a failure, without failing the real test. The embedded testing.TB
// is nil; the helpers only ever call Helper and Errorf, both overridden here,
// so no nil method is reached and nothing panics.
type stubTB struct {
	testing.TB
	failed bool
}

func (s *stubTB) Helper()                                   {}
func (s *stubTB) Errorf(format string, args ...interface{}) { s.failed = true }

func TestAsserts(t *testing.T) {
	store, cleanup := FakeMemory(t, "asserts")
	defer cleanup()

	if err := store.Put("present", "value", time.Hour); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	t.Run("AssertHas happy", func(t *testing.T) {
		s := &stubTB{}
		AssertHas(s, store, "present")
		if s.failed {
			t.Error("AssertHas reported failure for a present key")
		}
	})

	t.Run("AssertHas sad", func(t *testing.T) {
		s := &stubTB{}
		AssertHas(s, store, "absent")
		if !s.failed {
			t.Error("AssertHas did not report failure for a missing key")
		}
	})

	t.Run("AssertMissing happy", func(t *testing.T) {
		s := &stubTB{}
		AssertMissing(s, store, "absent")
		if s.failed {
			t.Error("AssertMissing reported failure for a missing key")
		}
	})

	t.Run("AssertMissing sad", func(t *testing.T) {
		s := &stubTB{}
		AssertMissing(s, store, "present")
		if !s.failed {
			t.Error("AssertMissing did not report failure for a present key")
		}
	})

	t.Run("AssertForgotten happy", func(t *testing.T) {
		s := &stubTB{}
		AssertForgotten(s, store, "absent")
		if s.failed {
			t.Error("AssertForgotten reported failure for a missing key")
		}
	})

	t.Run("AssertForgotten sad", func(t *testing.T) {
		s := &stubTB{}
		AssertForgotten(s, store, "present")
		if !s.failed {
			t.Error("AssertForgotten did not report failure for a present key")
		}
	})

	t.Run("AssertHasValue happy", func(t *testing.T) {
		s := &stubTB{}
		AssertHasValue(s, store, "present", "value")
		if s.failed {
			t.Error("AssertHasValue reported failure for a matching value")
		}
	})

	t.Run("AssertHasValue sad wrong value", func(t *testing.T) {
		s := &stubTB{}
		AssertHasValue(s, store, "present", "other")
		if !s.failed {
			t.Error("AssertHasValue did not report failure for a mismatched value")
		}
	})

	t.Run("AssertHasValue sad missing", func(t *testing.T) {
		s := &stubTB{}
		AssertHasValue(s, store, "absent", "value")
		if !s.failed {
			t.Error("AssertHasValue did not report failure for a missing key")
		}
	})
}

func TestAssertsAgainstManager(t *testing.T) {
	manager, cleanup := FakeManagerMemory(t, "asserts")
	defer cleanup()

	if err := manager.Put("present", "value", time.Hour); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	s := &stubTB{}
	AssertHas(s, manager, "present")
	AssertMissing(s, manager, "absent")
	AssertForgotten(s, manager, "absent")
	AssertHasValue(s, manager, "present", "value")
	if s.failed {
		t.Error("assertions reported failure against a manager for valid expectations")
	}
}
