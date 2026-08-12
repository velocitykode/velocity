package auth

import (
	"errors"
	"testing"
)

// TestBaseSessionInvalidate_RotatesID verifies that Invalidate() rotates
// the session ID so a caller holding the pre-invalidate id cannot reuse
// it to rehydrate state.
//
// Audit M-05: an attacker who learned the old ID (logged elsewhere,
// remote leak, downstream rehydration) must not get back into a session
// at the same id after Invalidate succeeds.
func TestBaseSessionInvalidate_RotatesID(t *testing.T) {
	sess := NewSession("planted-id")
	before := sess.ID()

	if err := sess.Invalidate(); err != nil {
		t.Fatalf("Invalidate() err = %v", err)
	}

	after := sess.ID()
	if after == before {
		t.Errorf("Invalidate() must rotate id (before=%q, after=%q)", before, after)
	}
	if after == "" {
		t.Error("Invalidate() should produce a fresh non-empty id when entropy is available")
	}
	if !sess.IsDestroyed() {
		t.Error("Invalidate() must mark session destroyed")
	}
}

// TestBaseSessionInvalidate_ClearsState verifies that Invalidate() empties
// the data and flash bags. The pre-existing behaviour is preserved
// alongside the new id rotation.
func TestBaseSessionInvalidate_ClearsState(t *testing.T) {
	sess := NewSession("test-id")
	sess.Put("user_id", 42)
	sess.Flash("notice", "hi")

	if err := sess.Invalidate(); err != nil {
		t.Fatalf("Invalidate() err = %v", err)
	}

	if got := sess.Get("user_id"); got != nil {
		t.Errorf("data not cleared: got %v", got)
	}
	if got := sess.GetFlash("notice"); got != nil {
		t.Errorf("flash not cleared: got %v", got)
	}
}

// TestBaseSessionInvalidate_RandFailureZeroesID verifies that when
// crypto/rand fails during ID rotation, the session is still marked
// destroyed and its ID is forced to empty so the pre-invalidate id
// is not retained. Defence in depth for the M-05 invariant.
func TestBaseSessionInvalidate_RandFailureZeroesID(t *testing.T) {
	sess := NewSession("planted-id")
	restore := withSessionRand(t, errReader{err: errors.New("boom")})
	defer restore()

	err := sess.Invalidate()
	if err == nil {
		t.Fatal("expected error from Invalidate when rand fails")
	}
	if sess.ID() == "planted-id" {
		t.Errorf("Invalidate must not leave pre-invalidate id intact on rand failure: %q", sess.ID())
	}
	if sess.ID() != "" {
		t.Errorf("expected empty id on rand failure, got %q", sess.ID())
	}
	if !sess.IsDestroyed() {
		t.Error("destroyed flag must still be set on rand failure")
	}
}
