package scheduler

import "testing"

type nilLocker struct{ Locker }

// SetLocker treats a typed-nil Locker as nil: the in-process Locker is
// restored, never a nil receiver the next guarded job would call.
func TestSetLocker_TypedNilRestoresTheInProcessLocker(t *testing.T) {
	var typed *nilLocker
	s := New().SetLocker(typed)
	if _, ok := s.Locker().(*InMemoryLocker); !ok {
		t.Fatalf("Locker after SetLocker(typed nil) = %T, want *InMemoryLocker", s.Locker())
	}
}
