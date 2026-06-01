package guards

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/velocitykode/velocity/auth"
)

func TestJWTGuard_User_RejectsBlacklistedCachedToken(t *testing.T) {
	user := &mockJWTUser{id: "user123"}
	guard := mustNewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())

	token, err := guard.jwtManager.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	if got := guard.User(r); got == nil {
		t.Fatal("User() before revocation = nil, want authenticated user")
	}
	if _, ok := guard.getCachedUser(token); !ok {
		t.Fatal("expected first User() call to populate user cache")
	}

	claims, err := guard.jwtManager.ValidateAccessToken(token)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	guard.jwtManager.RevokeToken(claims.ID, claims.ExpiresAt.Time)

	if _, ok := guard.getCachedUser(token); !ok {
		t.Fatal("test setup error: revocation should not remove the cached user")
	}
	if got := guard.User(r); got != nil {
		t.Fatalf("User() after revocation = %v, want nil", got)
	}
}

func TestJWTGuard_User_RejectsExpiredCachedToken(t *testing.T) {
	config := newTestJWTConfig()
	guard := mustNewJWTGuard(&mockJWTUserProvider{}, config)
	now := time.Now()

	claims := auth.Claims{
		RegisteredClaims: jwtlib.RegisteredClaims{
			ID:        "expired-access-token",
			Subject:   "user123",
			IssuedAt:  jwtlib.NewNumericDate(now.Add(-2 * time.Hour)),
			ExpiresAt: jwtlib.NewNumericDate(now.Add(-1 * time.Hour)),
			NotBefore: jwtlib.NewNumericDate(now.Add(-2 * time.Hour)),
		},
		UserID:    "user123",
		TokenType: "access",
	}
	token, err := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims).SignedString([]byte(config.Secret))
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	guard.userCache[token] = cachedUser{user: &mockJWTUser{id: "cached-user"}, cachedAt: time.Now()}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	if _, ok := guard.getCachedUser(token); !ok {
		t.Fatal("test setup error: expected expired token to have a cached user")
	}
	if got := guard.User(r); got != nil {
		t.Fatalf("User() for expired cached token = %v, want nil", got)
	}
}
