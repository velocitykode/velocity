package schemes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
)

// BenchmarkSessionMiddleware_UnmodifiedRequest is one request through the
// session middleware whose handler changes nothing: the session is loaded
// from its cookie and the commit asks it to save, which writes nothing.
// The whole request is measured (load, handler, commit), per session store.
func BenchmarkSessionMiddleware_UnmodifiedRequest(b *testing.B) {
	cfg := auth.SessionConfig{Name: "vel_session", IdleLifetime: 60, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}
	enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		b.Fatal(err)
	}
	stores := map[string]func(b *testing.B) []SessionSchemeOption{
		"cookie": func(*testing.B) []SessionSchemeOption { return nil },
		"server": func(b *testing.B) []SessionSchemeOption {
			records := session.NewMemoryStore()
			b.Cleanup(func() { _ = records.Close(context.Background()) })
			store, err := session.NewServerStore(cfg, records)
			if err != nil {
				b.Fatal(err)
			}
			return []SessionSchemeOption{WithSessionStore(store)}
		},
	}
	for name, opts := range stores {
		b.Run(name, func(b *testing.B) {
			scheme, err := NewSessionScheme(&mockSessionSchemeUserStore{}, cfg, enc, opts(b)...)
			if err != nil {
				b.Fatal(err)
			}
			r := router.New()
			r.Use(scheme.SessionMiddleware())
			r.Get("/", func(c *router.Context) error { return c.NoContent() })

			first := httptest.NewRecorder()
			r.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
			cookies := first.Result().Cookies()
			if len(cookies) != 1 {
				b.Fatalf("the first response set %d cookies, want the session's", len(cookies))
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(cookies[0])

			b.ReportAllocs()
			for b.Loop() {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if len(w.Header().Values("Set-Cookie")) != 0 {
					b.Fatal("an unmodified request wrote a cookie")
				}
			}
		})
	}
}
