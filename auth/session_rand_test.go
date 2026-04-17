package auth

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// errReader is an io.Reader that always returns err. Used to drive the
// crypto/rand failure paths in session ID generation.
type errReader struct{ err error }

func (e errReader) Read(p []byte) (int, error) { return 0, e.err }

func withSessionRand(t *testing.T, r io.Reader) func() {
	t.Helper()
	orig := sessionRandReader
	sessionRandReader = r
	return func() { sessionRandReader = orig }
}

func TestGenerateSessionID_RandFailure(t *testing.T) {
	restore := withSessionRand(t, errReader{err: errors.New("boom")})
	defer restore()

	id, err := generateSessionID()
	if err == nil {
		t.Fatal("expected error from generateSessionID")
	}
	if id != "" {
		t.Errorf("expected empty id on failure, got %q", id)
	}
	if !strings.HasPrefix(err.Error(), "velocity/auth:") {
		t.Errorf("error missing velocity/auth prefix: %v", err)
	}
}

func TestNewSessionWithError_RandFailure(t *testing.T) {
	restore := withSessionRand(t, errReader{err: errors.New("boom")})
	defer restore()

	sess, err := NewSessionWithError("")
	if err == nil {
		t.Fatal("expected error from NewSessionWithError")
	}
	if sess == nil {
		t.Fatal("expected non-nil session even on error")
	}
	if sess.ID() != "" {
		t.Errorf("expected empty id on failure, got %q", sess.ID())
	}
}

func TestBaseSessionRegenerate_RandFailure(t *testing.T) {
	sess := NewSession("original-id")
	restore := withSessionRand(t, errReader{err: errors.New("boom")})
	defer restore()

	before := sess.ID()
	if err := sess.Regenerate(); err == nil {
		t.Fatal("expected error from Regenerate")
	}
	if sess.ID() != before {
		t.Errorf("id should be unchanged on rand failure, before=%q after=%q", before, sess.ID())
	}
}
