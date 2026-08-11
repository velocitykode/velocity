package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockSessionScheme is a Scheme that also satisfies SessionAware so the
// Manager.Session delegation path can be exercised.
type mockSessionScheme struct {
	mockScheme
	session Session
}

func (g *mockSessionScheme) Session(*http.Request) Session { return g.session }

// Compile-time check that the test mocks satisfy the interfaces under test.
var (
	_ Scheme       = (*mockScheme)(nil)
	_ Scheme       = (*mockSessionScheme)(nil)
	_ SessionAware = (*mockSessionScheme)(nil)
)

func TestManagerSession_NoDefaultScheme(t *testing.T) {
	m := NewManager()

	// Sanity: no scheme registered, DefaultScheme must error.
	if _, err := m.DefaultScheme(); err == nil {
		t.Fatal("precondition: DefaultScheme() should error when no scheme registered")
	}

	r := httptest.NewRequest("GET", "/", nil)

	// Must not panic; must return nil.
	if got := m.Session(r); got != nil {
		t.Errorf("Session() with no default scheme = %v, want nil", got)
	}
}

func TestManagerSession_SchemeNotSessionAware(t *testing.T) {
	m := NewManager()
	m.RegisterScheme("web", &mockScheme{name: "web"})

	r := httptest.NewRequest("GET", "/", nil)

	if got := m.Session(r); got != nil {
		t.Errorf("Session() with non-SessionAware scheme = %v, want nil", got)
	}
}

func TestManagerSession_SchemeSessionAware(t *testing.T) {
	wantSession := NewSession("manager-session-id")
	wantSession.Flash("greeting", "hello")

	m := NewManager()
	m.RegisterScheme("web", &mockSessionScheme{
		mockScheme: mockScheme{name: "web"},
		session:    wantSession,
	})

	r := httptest.NewRequest("GET", "/", nil)

	got := m.Session(r)
	if got == nil {
		t.Fatal("Session() returned nil for SessionAware scheme")
	}
	if got != Session(wantSession) {
		t.Error("Session() did not return the scheme's session instance")
	}

	// Confirm we can drive the flash bag through the Manager surface.
	flushed := got.FlushFlash()
	if flushed["greeting"] != "hello" {
		t.Errorf("FlushFlash via Manager.Session = %v, want greeting=hello", flushed)
	}
}
