package auth

import (
	"strings"
	"testing"
)

func benchJWTManager(b *testing.B, blacklist bool) *JWTManager {
	b.Helper()
	mgr, err := NewJWTManager(JWTConfig{
		Secret:           strings.Repeat("s", 64),
		Algorithm:        "HS256",
		TTL:              60,
		RefreshTTL:       20160,
		BlacklistEnabled: blacklist,
		BlacklistStore:   NewInMemoryBlacklistStore(),
	})
	if err != nil {
		b.Fatalf("NewJWTManager: %v", err)
	}
	return mgr
}

// BenchmarkJWTManager_ValidateToken measures one access-token validation
// with and without the blacklist read.
func BenchmarkJWTManager_ValidateToken(b *testing.B) {
	for _, mode := range []struct {
		name      string
		blacklist bool
	}{{"blacklist_on", true}, {"blacklist_off", false}} {
		b.Run(mode.name, func(b *testing.B) {
			mgr := benchJWTManager(b, mode.blacklist)
			token, err := mgr.GenerateToken(&jwtRefreshTestUser{id: "user-1"})
			if err != nil {
				b.Fatalf("GenerateToken: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := mgr.ValidateToken(token); err != nil {
					b.Fatalf("ValidateToken: %v", err)
				}
			}
		})
	}
}

// BenchmarkJWTManager_RefreshToken measures one refresh with the blacklist
// off, where a refresh token is reusable so one token serves every
// iteration: validation, generation check, user lookup and issuance.
func BenchmarkJWTManager_RefreshToken(b *testing.B) {
	mgr := benchJWTManager(b, false)
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &consumeUserStore{user: user}
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		b.Fatalf("GenerateRefreshToken: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := mgr.RefreshToken(refresh, users); err != nil {
			b.Fatalf("RefreshToken: %v", err)
		}
	}
}
