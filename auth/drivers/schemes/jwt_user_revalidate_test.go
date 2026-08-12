package schemes

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/velocitykode/velocity/auth"
)

func TestJWTScheme_User_RejectsBlacklistedCachedToken(t *testing.T) {
	user := &mockJWTUser{id: "user123"}
	scheme := mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())

	token, err := scheme.jwtManager.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	if got := scheme.User(r); got == nil {
		t.Fatal("User() before revocation = nil, want authenticated user")
	}
	if _, ok := scheme.getCachedUser(token); !ok {
		t.Fatal("expected first User() call to populate user cache")
	}

	claims, err := scheme.jwtManager.ValidateAccessToken(token)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	scheme.jwtManager.RevokeToken(claims.ID, claims.ExpiresAt.Time)

	if _, ok := scheme.getCachedUser(token); !ok {
		t.Fatal("test setup error: revocation should not remove the cached user")
	}
	if got := scheme.User(r); got != nil {
		t.Fatalf("User() after revocation = %v, want nil", got)
	}
}

func TestJWTScheme_User_RejectsExpiredCachedToken(t *testing.T) {
	config := newTestJWTConfig()
	scheme := mustNewJWTScheme(&mockJWTUserStore{}, config)
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

	scheme.userCache[token] = cachedUser{user: &mockJWTUser{id: "cached-user"}, cachedAt: time.Now()}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	if _, ok := scheme.getCachedUser(token); !ok {
		t.Fatal("test setup error: expected expired token to have a cached user")
	}
	if got := scheme.User(r); got != nil {
		t.Fatalf("User() for expired cached token = %v, want nil", got)
	}
}
