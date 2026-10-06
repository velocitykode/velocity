package schemes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

func benchJWTScheme(b *testing.B, blacklist bool) (*JWTScheme, *http.Request) {
	b.Helper()
	cfg := newTestJWTConfig()
	cfg.BlacklistEnabled = blacklist
	cfg.BlacklistStore = auth.NewInMemoryBlacklistStore()
	g := mustNewJWTScheme(&mockJWTUserStore{}, cfg)
	token, err := g.GenerateToken(&mockJWTUser{id: "user123"})
	if err != nil {
		b.Fatalf("GenerateToken: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return g, r
}

// BenchmarkJWTScheme_Check measures one Check: validation, the blacklist
// read when enabled, and the user lookup.
func BenchmarkJWTScheme_Check(b *testing.B) {
	for _, mode := range []struct {
		name      string
		blacklist bool
	}{{"blacklist_on", true}, {"blacklist_off", false}} {
		b.Run(mode.name, func(b *testing.B) {
			g, r := benchJWTScheme(b, mode.blacklist)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if !g.Check(r) {
					b.Fatal("check refused a valid token")
				}
			}
		})
		b.Run(mode.name+"/parallel", func(b *testing.B) {
			g, r := benchJWTScheme(b, mode.blacklist)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if !g.Check(r) {
						b.Fatal("check refused a valid token")
					}
				}
			})
		})
	}
}

// BenchmarkJWTScheme_UserCacheHit measures User when the user is cached:
// validation and the blacklist read, no user lookup.
func BenchmarkJWTScheme_UserCacheHit(b *testing.B) {
	g, r := benchJWTScheme(b, true)
	if g.User(r) == nil {
		b.Fatal("user refused a valid token")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if g.User(r) == nil {
			b.Fatal("user refused a valid token")
		}
	}
}
