package schemes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/crypto"
)

// BenchmarkSessionScheme_Login signs a user in outside the session
// middleware, with a server session store: the path that stores the user's
// identifier in the session and keys the server record by its text.
func BenchmarkSessionScheme_Login(b *testing.B) {
	enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		b.Fatalf("NewEncryptor: %v", err)
	}
	scheme, err := NewSessionScheme(&revokeTestStore{users: map[string]*revokeTestUser{"u1": {id: "u1"}}}, auth.SessionConfig{
		Name: "vel_session", IdleLifetime: 60, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		b.Fatalf("NewSessionScheme: %v", err)
	}
	scheme.SetServerSessionStore(session.NewMemoryStore())
	user := &revokeTestUser{id: "u1"}
	b.ReportAllocs()
	for b.Loop() {
		w := httptest.NewRecorder()
		r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/login", nil))
		if err := scheme.Login(w, r, user); err != nil {
			b.Fatal(err)
		}
	}
}
