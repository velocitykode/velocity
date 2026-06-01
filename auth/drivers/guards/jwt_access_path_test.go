package guards

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJWTGuard_RejectsRefreshTokenOnAccessPath(t *testing.T) {
	guard := mustNewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
	user := &mockJWTUser{id: "user123"}

	accessToken, err := guard.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	refreshToken, err := guard.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	requestWithBearer := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}

	t.Run("refresh token rejected", func(t *testing.T) {
		r := requestWithBearer(refreshToken)

		if guard.Check(r) {
			t.Fatal("Check returned true for refresh token")
		}
		if got := guard.User(r); got != nil {
			t.Fatalf("User returned %#v for refresh token, want nil", got)
		}
		if got := guard.ID(r); got != nil {
			t.Fatalf("ID returned %#v for refresh token, want nil", got)
		}
	})

	t.Run("access token accepted", func(t *testing.T) {
		r := requestWithBearer(accessToken)

		if !guard.Check(r) {
			t.Fatal("Check returned false for access token")
		}
		if got := guard.User(r); got == nil {
			t.Fatal("User returned nil for access token")
		}
		if got := guard.ID(r); got == nil {
			t.Fatal("ID returned nil for access token")
		}
	})
}
