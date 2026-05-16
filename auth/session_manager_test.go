package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockSessionGuard is a Guard that also satisfies SessionAware so the
// Manager.Session delegation path can be exercised.
type mockSessionGuard struct {
	mockGuard
	session Session
}

func (g *mockSessionGuard) Session(*http.Request) Session { return g.session }

// Compile-time check that the test mocks satisfy the interfaces under test.
var (
	_ Guard        = (*mockGuard)(nil)
	_ Guard        = (*mockSessionGuard)(nil)
	_ SessionAware = (*mockSessionGuard)(nil)
)

func TestManagerSession_NoDefaultGuard(t *testing.T) {
	m := NewManager()

	// Sanity: no guard registered, DefaultGuard must error.
	if _, err := m.DefaultGuard(); err == nil {
		t.Fatal("precondition: DefaultGuard() should error when no guard registered")
	}

	r := httptest.NewRequest("GET", "/", nil)

	// Must not panic; must return nil.
	if got := m.Session(r); got != nil {
		t.Errorf("Session() with no default guard = %v, want nil", got)
	}
}

func TestManagerSession_GuardNotSessionAware(t *testing.T) {
	m := NewManager()
	m.RegisterGuard("web", &mockGuard{name: "web"})

	r := httptest.NewRequest("GET", "/", nil)

	if got := m.Session(r); got != nil {
		t.Errorf("Session() with non-SessionAware guard = %v, want nil", got)
	}
}

func TestManagerSession_GuardSessionAware(t *testing.T) {
	wantSession := NewSession("manager-session-id")
	wantSession.Flash("greeting", "hello")

	m := NewManager()
	m.RegisterGuard("web", &mockSessionGuard{
		mockGuard: mockGuard{name: "web"},
		session:   wantSession,
	})

	r := httptest.NewRequest("GET", "/", nil)

	got := m.Session(r)
	if got == nil {
		t.Fatal("Session() returned nil for SessionAware guard")
	}
	if got != Session(wantSession) {
		t.Error("Session() did not return the guard's session instance")
	}

	// Confirm we can drive the flash bag through the Manager surface.
	flushed := got.FlushFlash()
	if flushed["greeting"] != "hello" {
		t.Errorf("FlushFlash via Manager.Session = %v, want greeting=hello", flushed)
	}
}
