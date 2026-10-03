package auth

import (
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// BenchmarkJWTIssue issues access and refresh tokens for an integer and a
// string identifier: the paths that derive the token subject and the
// refresh-generation key from the user's identifier.
func BenchmarkJWTIssue(b *testing.B) {
	m, err := NewJWTManager(JWTConfig{
		Secret:     "benchmark-secret-must-be-at-least-32-b",
		Algorithm:  "HS256",
		TTL:        60,
		RefreshTTL: 1440,
	})
	if err != nil {
		b.Fatalf("NewJWTManager: %v", err)
	}
	for _, u := range []struct {
		name string
		user contract.Authenticatable
	}{{"uint", &AuthUser{ID: uint(1)}}, {"string", &AuthUser{ID: "0b0f6a8e-4f3c-4d5e-9a1b-2c3d4e5f6a7b"}}} {
		b.Run("access/"+u.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := m.GenerateToken(u.user); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("refresh/"+u.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := m.GenerateRefreshToken(u.user); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
