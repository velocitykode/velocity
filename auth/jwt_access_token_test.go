package auth

import "testing"

func TestJWTManager_ValidateAccessToken(t *testing.T) {
	mgr := newJWTManagerForRefresh(t)
	user := &jwtRefreshTestUser{id: "user-access"}

	accessToken, err := mgr.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	refreshToken, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	tests := []struct {
		name       string
		token      string
		wantClaims bool
		wantErr    bool
	}{
		{
			name:       "accepts access token",
			token:      accessToken,
			wantClaims: true,
		},
		{
			name:    "rejects refresh token",
			token:   refreshToken,
			wantErr: true,
		},
		{
			name:    "rejects garbage token",
			token:   "not-a-jwt",
			wantErr: true,
		},
		{
			name:    "rejects empty token",
			token:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims, err := mgr.ValidateAccessToken(tt.token)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ValidateAccessToken returned nil error; expected rejection")
				}
				if claims != nil {
					t.Fatalf("ValidateAccessToken claims = %#v, want nil", claims)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateAccessToken returned error: %v", err)
			}
			if claims == nil {
				t.Fatal("ValidateAccessToken returned nil claims")
			}
			if claims.TokenType != "access" {
				t.Fatalf("TokenType = %q, want access", claims.TokenType)
			}
		})
	}

	t.Run("ValidateToken still accepts refresh token", func(t *testing.T) {
		claims, err := mgr.ValidateToken(refreshToken)
		if err != nil {
			t.Fatalf("ValidateToken(refreshToken) returned error: %v", err)
		}
		if claims == nil {
			t.Fatal("ValidateToken(refreshToken) returned nil claims")
		}
		if claims.TokenType != "refresh" {
			t.Fatalf("TokenType = %q, want refresh", claims.TokenType)
		}
	})
}
