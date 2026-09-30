package csrf

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/csrf/stores"
)

// BenchmarkTokenForRequest measures a request's first token read (the
// load through the store) and a later read (the request cache).
func BenchmarkTokenForRequest(b *testing.B) {
	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	cfg.Store = stores.NewMemoryStore()
	c, err := NewE(cfg)
	if err != nil {
		b.Fatal(err)
	}
	base := requestWithSession("GET", "/", "bench-session")
	if _, err := c.GetToken(context.Background(), "bench-session"); err != nil {
		b.Fatal(err)
	}
	b.Run("first-read", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			r := base.WithContext(withTokenState(base.Context(), c))
			if tok, err := TokenForRequest(r); err != nil || tok == "" {
				b.Fatal(tok, err)
			}
		}
	})
	b.Run("cache-hit", func(b *testing.B) {
		r := base.WithContext(withTokenState(base.Context(), c))
		if _, err := TokenForRequest(r); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			if tok, err := TokenForRequest(r); err != nil || tok == "" {
				b.Fatal(tok, err)
			}
		}
	})
}
