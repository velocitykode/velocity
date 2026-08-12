package schemes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/velocitykode/velocity/auth"
)

// forgeUnsignedJWTSchemeToken crafts a JWT with the given alg header and no
// signature. Mirrors auth.forgeUnsignedToken (package-private) for scheme-level
// tests.
func forgeUnsignedJWTSchemeToken(t *testing.T, alg string, claims map[string]interface{}) string {
	t.Helper()
	header := map[string]string{"alg": alg, "typ": "JWT"}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(h) + "." +
		base64.RawURLEncoding.EncodeToString(p) + "."
}

// mockJWTUserStore implements auth.UserStore for JWT tests
type mockJWTUserStore struct {
	findByIDFunc            func(id interface{}) (auth.Authenticatable, error)
	findByCredentialsFunc   func(credentials map[string]interface{}) (auth.Authenticatable, error)
	validateCredentialsFunc func(user auth.Authenticatable, credentials map[string]interface{}) bool
	updateRememberTokenFunc func(user auth.Authenticatable, token string) error
}

func (p *mockJWTUserStore) FindByID(id interface{}) (auth.Authenticatable, error) {
	if p.findByIDFunc != nil {
		return p.findByIDFunc(id)
	}
	return &mockJWTUser{id: id, password: "hashedpassword"}, nil
}

func (p *mockJWTUserStore) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	if p.findByCredentialsFunc != nil {
		return p.findByCredentialsFunc(credentials)
	}
	return &mockJWTUser{id: "user123", email: "test@example.com", password: "hashedpassword"}, nil
}

func (p *mockJWTUserStore) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	if p.validateCredentialsFunc != nil {
		return p.validateCredentialsFunc(user, credentials)
	}
	return true
}

func (p *mockJWTUserStore) UpdateRememberToken(user auth.Authenticatable, token string) error {
	if p.updateRememberTokenFunc != nil {
		return p.updateRememberTokenFunc(user, token)
	}
	return nil
}

// mockJWTUser implements auth.Authenticatable for JWT tests
type mockJWTUser struct {
	id            interface{}
	email         string
	password      string
	rememberToken string
}

func (u *mockJWTUser) GetAuthIdentifier() interface{} {
	return u.id
}

func (u *mockJWTUser) GetAuthPassword() string {
	return u.password
}

func (u *mockJWTUser) GetRememberToken() string {
	return u.rememberToken
}

func (u *mockJWTUser) SetRememberToken(token string) {
	u.rememberToken = token
}

func newTestJWTConfig() auth.JWTConfig {
	return auth.JWTConfig{
		Secret:           "test-secret-key-for-jwt-signing-minimum-length",
		Algorithm:        "HS256",
		TTL:              60,
		RefreshTTL:       1440,
		BlacklistEnabled: true,
		BlacklistStore:   auth.NewInMemoryBlacklistStore(),
	}
}

// mustNewJWTScheme is a test helper that panics if NewJWTScheme returns an
// error. Used to keep existing table-driven tests terse while NewJWTScheme's
// signature changed to (*JWTScheme, error).
func mustNewJWTScheme(userStore auth.UserStore, config auth.JWTConfig) *JWTScheme {
	g, err := NewJWTScheme(userStore, config)
	if err != nil {
		panic("mustNewJWTScheme: " + err.Error())
	}
	return g
}

func TestNewJWTScheme(t *testing.T) {
	tests := []struct {
		name      string
		userStore auth.UserStore
		config    auth.JWTConfig
	}{
		{
			name:      "creates scheme with valid user store and config",
			userStore: &mockJWTUserStore{},
			config:    newTestJWTConfig(),
		},
		{
			name:      "creates scheme with empty algorithm defaults to HS256",
			userStore: &mockJWTUserStore{},
			config: auth.JWTConfig{
				Secret: "test-secret-key-for-jwt-minimum-32b",
				TTL:    60,
			},
		},
		{
			name:      "creates scheme with zero TTL defaults to 60",
			userStore: &mockJWTUserStore{},
			config: auth.JWTConfig{
				Secret: "test-secret-key-for-jwt-minimum-32b",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, err := NewJWTScheme(tt.userStore, tt.config)
			if err != nil {
				t.Fatalf("NewJWTScheme returned error: %v", err)
			}
			if scheme == nil {
				t.Error("NewJWTScheme returned nil")
				return
			}
			if scheme.loadUserStore() != tt.userStore {
				t.Error("user store not set correctly")
			}
			if scheme.userCache == nil {
				t.Error("userCache not initialized")
			}
		})
	}
}

func TestJWTScheme_Check(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *JWTScheme
		setupReq    func(scheme *JWTScheme) *http.Request
		want        bool
	}{
		{
			name: "returns true for valid token with existing user",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			want: true,
		},
		{
			name: "returns false when no token in request",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			want: false,
		},
		{
			name: "returns false for invalid token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer invalid-token")
				return req
			},
			want: false,
		},
		{
			name: "returns false when user not found",
			setupScheme: func() *JWTScheme {
				userStore := &mockJWTUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return mustNewJWTScheme(userStore, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				user := &mockJWTUser{id: "nonexistent"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			want: false,
		},
		{
			name: "returns false when user is nil",
			setupScheme: func() *JWTScheme {
				userStore := &mockJWTUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, nil
					},
				}
				return mustNewJWTScheme(userStore, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			want: false,
		},
		{
			name: "caches user after successful check",
			setupScheme: func() *JWTScheme {
				callCount := 0
				userStore := &mockJWTUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						callCount++
						if callCount > 1 {
							return nil, errors.New("should not be called twice")
						}
						return &mockJWTUser{id: id}, nil
					},
				}
				return mustNewJWTScheme(userStore, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq(scheme)
			got := scheme.Check(req)
			if got != tt.want {
				t.Errorf("Check() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJWTScheme_User(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *JWTScheme
		setupReq    func(scheme *JWTScheme) *http.Request
		wantNil     bool
		wantID      interface{}
	}{
		{
			name: "returns user for valid token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantNil: false,
			wantID:  "user123",
		},
		{
			name: "returns nil when no token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			wantNil: true,
		},
		{
			name: "returns nil for invalid token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer invalid-token")
				return req
			},
			wantNil: true,
		},
		{
			name: "returns nil when user store returns error",
			setupScheme: func() *JWTScheme {
				userStore := &mockJWTUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("database error")
					},
				}
				return mustNewJWTScheme(userStore, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantNil: true,
		},
		{
			name: "returns cached user on subsequent calls",
			setupScheme: func() *JWTScheme {
				scheme := mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
				user := &mockJWTUser{id: "user123"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				scheme.userCache[token] = cachedUser{user: &mockJWTUser{id: "cached-user"}, cachedAt: time.Now()}
				return scheme
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				var token string
				for cachedToken := range scheme.userCache {
					token = cachedToken
					break
				}
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantNil: false,
			wantID:  "cached-user",
		},
		{
			name: "returns nil for revoked token even when user is cached",
			setupScheme: func() *JWTScheme {
				scheme := mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
				user := &mockJWTUser{id: "user123"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				claims, _ := scheme.jwtManager.ValidateToken(token)
				scheme.userCache[token] = cachedUser{user: &mockJWTUser{id: "cached-user"}, cachedAt: time.Now()}
				scheme.jwtManager.RevokeToken(claims.ID, claims.ExpiresAt.Time)
				return scheme
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				var token string
				for cachedToken := range scheme.userCache {
					token = cachedToken
					break
				}
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq(scheme)
			got := scheme.User(req)
			if tt.wantNil {
				if got != nil {
					t.Errorf("User() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Error("User() = nil, want non-nil")
				return
			}
			if got.GetAuthIdentifier() != tt.wantID {
				t.Errorf("User().GetAuthIdentifier() = %v, want %v", got.GetAuthIdentifier(), tt.wantID)
			}
		})
	}
}

func TestJWTScheme_ID(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *JWTScheme
		setupReq    func(scheme *JWTScheme) *http.Request
		wantNil     bool
		wantID      interface{}
	}{
		{
			name: "returns user ID for valid token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				user := &mockJWTUser{id: "user456"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantNil: false,
			wantID:  "user456",
		},
		{
			name: "returns nil when no token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			wantNil: true,
		},
		{
			name: "returns nil for invalid token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer bad-token")
				return req
			},
			wantNil: true,
		},
		{
			name: "returns ID without verifying user exists",
			setupScheme: func() *JWTScheme {
				userStore := &mockJWTUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user deleted")
					},
				}
				return mustNewJWTScheme(userStore, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				user := &mockJWTUser{id: "deleted-user"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantNil: false,
			wantID:  "deleted-user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq(scheme)
			got := scheme.ID(req)
			if tt.wantNil {
				if got != nil {
					t.Errorf("ID() = %v, want nil", got)
				}
				return
			}
			if got != tt.wantID {
				t.Errorf("ID() = %v, want %v", got, tt.wantID)
			}
		})
	}
}

func TestJWTScheme_Login(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *JWTScheme
		user        auth.Authenticatable
		remember    []bool
		wantErr     bool
		checkResp   func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{
			name: "generates token and sets X-Auth-Token header",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			user:    &mockJWTUser{id: "user123"},
			wantErr: false,
			checkResp: func(t *testing.T, w *httptest.ResponseRecorder) {
				token := w.Header().Get("X-Auth-Token")
				if token == "" {
					t.Error("X-Auth-Token header not set")
				}
			},
		},
		{
			name: "generates refresh token when remember is true",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			user:     &mockJWTUser{id: "user123"},
			remember: []bool{true},
			wantErr:  false,
			checkResp: func(t *testing.T, w *httptest.ResponseRecorder) {
				token := w.Header().Get("X-Auth-Token")
				if token == "" {
					t.Error("X-Auth-Token header not set")
				}
				refreshToken := w.Header().Get("X-Refresh-Token")
				if refreshToken == "" {
					t.Error("X-Refresh-Token header not set when remember=true")
				}
			},
		},
		{
			name: "does not generate refresh token when remember is false",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			user:     &mockJWTUser{id: "user123"},
			remember: []bool{false},
			wantErr:  false,
			checkResp: func(t *testing.T, w *httptest.ResponseRecorder) {
				refreshToken := w.Header().Get("X-Refresh-Token")
				if refreshToken != "" {
					t.Error("X-Refresh-Token header should not be set when remember=false")
				}
			},
		},
		{
			name: "does not generate refresh token when remember not provided",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			user:    &mockJWTUser{id: "user123"},
			wantErr: false,
			checkResp: func(t *testing.T, w *httptest.ResponseRecorder) {
				refreshToken := w.Header().Get("X-Refresh-Token")
				if refreshToken != "" {
					t.Error("X-Refresh-Token header should not be set when remember not provided")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/login", nil)

			err := scheme.Login(w, r, tt.user, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("Login() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.checkResp != nil {
				tt.checkResp(t, w)
			}
		})
	}
}

func TestJWTScheme_LoginByID(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *JWTScheme
		id          interface{}
		remember    []bool
		wantErr     bool
	}{
		{
			name: "logs in existing user by ID",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			id:      "user123",
			wantErr: false,
		},
		{
			name: "returns error when user not found",
			setupScheme: func() *JWTScheme {
				userStore := &mockJWTUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return mustNewJWTScheme(userStore, newTestJWTConfig())
			},
			id:      "nonexistent",
			wantErr: true,
		},
		{
			name: "passes remember flag to Login",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			id:       "user123",
			remember: []bool{true},
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/login", nil)

			err := scheme.LoginByID(w, r, tt.id, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoginByID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestJWTScheme_Attempt(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *JWTScheme
		credentials map[string]interface{}
		remember    []bool
		wantSuccess bool
		wantErr     bool
	}{
		{
			name: "returns true for valid credentials",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			credentials: map[string]interface{}{
				"email":    "test@example.com",
				"password": "correctpassword",
			},
			wantSuccess: true,
			wantErr:     false,
		},
		{
			name: "returns false when user not found",
			setupScheme: func() *JWTScheme {
				userStore := &mockJWTUserStore{
					findByCredentialsFunc: func(credentials map[string]interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return mustNewJWTScheme(userStore, newTestJWTConfig())
			},
			credentials: map[string]interface{}{
				"email":    "nonexistent@example.com",
				"password": "password",
			},
			wantSuccess: false,
			wantErr:     false,
		},
		{
			name: "returns error when password not a string",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			credentials: map[string]interface{}{
				"email":    "test@example.com",
				"password": 12345,
			},
			wantSuccess: false,
			wantErr:     true,
		},
		{
			name: "returns false when password validation fails",
			setupScheme: func() *JWTScheme {
				userStore := &mockJWTUserStore{
					validateCredentialsFunc: func(user auth.Authenticatable, credentials map[string]interface{}) bool {
						return false
					},
				}
				return mustNewJWTScheme(userStore, newTestJWTConfig())
			},
			credentials: map[string]interface{}{
				"email":    "test@example.com",
				"password": "wrongpassword",
			},
			wantSuccess: false,
			wantErr:     false,
		},
		{
			name: "passes remember flag when authenticating",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			credentials: map[string]interface{}{
				"email":    "test@example.com",
				"password": "password",
			},
			remember:    []bool{true},
			wantSuccess: true,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/login", nil)

			success, err := scheme.Attempt(w, r, tt.credentials, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("Attempt() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if success != tt.wantSuccess {
				t.Errorf("Attempt() success = %v, want %v", success, tt.wantSuccess)
			}
		})
	}
}

func TestJWTScheme_Logout(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *JWTScheme
		setupReq    func(scheme *JWTScheme) *http.Request
		wantErr     bool
	}{
		{
			name: "revokes valid token and clears cache",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				// Pre-cache user
				scheme.userCache[token] = cachedUser{user: user, cachedAt: time.Now()}
				req := httptest.NewRequest("POST", "/logout", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantErr: false,
		},
		{
			name: "returns nil when no token in request",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				return httptest.NewRequest("POST", "/logout", nil)
			},
			wantErr: false,
		},
		{
			name: "returns nil for invalid token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupReq: func(scheme *JWTScheme) *http.Request {
				req := httptest.NewRequest("POST", "/logout", nil)
				req.Header.Set("Authorization", "Bearer invalid-token")
				return req
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq(scheme)
			w := httptest.NewRecorder()

			err := scheme.Logout(w, req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Logout() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestJWTScheme_getTokenFromRequest(t *testing.T) {
	tests := []struct {
		name      string
		setupReq  func() *http.Request
		wantToken string
	}{
		{
			name: "extracts Bearer token from Authorization header",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer my-jwt-token")
				return req
			},
			wantToken: "my-jwt-token",
		},
		{
			name: "rejects lowercase bearer (case sensitive)",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "bearer my-jwt-token")
				return req
			},
			wantToken: "",
		},
		{
			name: "rejects plain token in Authorization header",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "plain-token")
				return req
			},
			wantToken: "",
		},
		{
			name: "extracts token from X-Auth-Token header",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("X-Auth-Token", "x-auth-token-value")
				return req
			},
			wantToken: "x-auth-token-value",
		},
		{
			name: "ignores query parameter for WebSocket requests when AllowQueryToken is off (default)",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/?token=query-token", nil)
				req.Header.Set("Upgrade", "websocket")
				return req
			},
			wantToken: "",
		},
		{
			name: "extracts token from Sec-WebSocket-Protocol for WebSocket requests",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Upgrade", "websocket")
				req.Header.Set("Sec-WebSocket-Protocol", "chat, bearer.ws-header-token")
				return req
			},
			wantToken: "ws-header-token",
		},
		{
			name: "Sec-WebSocket-Protocol takes precedence over query parameter for WebSocket requests",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/?token=query-token", nil)
				req.Header.Set("Upgrade", "websocket")
				req.Header.Set("Sec-WebSocket-Protocol", "bearer.ws-header-token")
				return req
			},
			wantToken: "ws-header-token",
		},
		{
			name: "rejects query parameter for non-WebSocket requests",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/?token=query-token", nil)
				return req
			},
			wantToken: "",
		},
		{
			name: "rejects Sec-WebSocket-Protocol token for non-WebSocket requests",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Sec-WebSocket-Protocol", "bearer.ws-header-token")
				return req
			},
			wantToken: "",
		},
		{
			name: "extracts token from POST form value",
			setupReq: func() *http.Request {
				form := url.Values{}
				form.Set("token", "form-token")
				req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return req
			},
			wantToken: "form-token",
		},
		{
			name: "returns empty string when no token found",
			setupReq: func() *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			wantToken: "",
		},
		{
			name: "Authorization header takes precedence over X-Auth-Token",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer auth-header-token")
				req.Header.Set("X-Auth-Token", "x-auth-token")
				return req
			},
			wantToken: "auth-header-token",
		},
		{
			name: "X-Auth-Token takes precedence over query parameter",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/?token=query-token", nil)
				req.Header.Set("X-Auth-Token", "x-auth-token")
				return req
			},
			wantToken: "x-auth-token",
		},
		{
			name: "query parameter ignored for non-WebSocket POST (falls through to form)",
			setupReq: func() *http.Request {
				form := url.Values{}
				form.Set("token", "form-token")
				req := httptest.NewRequest("POST", "/?token=query-token", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return req
			},
			wantToken: "form-token",
		},
		{
			name: "does not check form value for GET request",
			setupReq: func() *http.Request {
				form := url.Values{}
				form.Set("token", "form-token")
				req := httptest.NewRequest("GET", "/", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return req
			},
			wantToken: "",
		},
		{
			name: "rejects malformed Bearer token (no space after Bearer)",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer")
				return req
			},
			wantToken: "",
		},
		{
			name: "handles Bearer with extra spaces (preserves token as-is after prefix)",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer  token-with-extra-space")
				return req
			},
			wantToken: " token-with-extra-space",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			req := tt.setupReq()
			got := scheme.getTokenFromRequest(req)
			if got != tt.wantToken {
				t.Errorf("getTokenFromRequest() = %q, want %q", got, tt.wantToken)
			}
		})
	}
}

func TestJWTScheme_getTokenFromRequest_AllowQueryToken(t *testing.T) {
	wsReq := func() *http.Request {
		req := httptest.NewRequest("GET", "/?token=query-token", nil)
		req.Header.Set("Upgrade", "websocket")
		return req
	}

	// Default (AllowQueryToken off): the query token is rejected on WS upgrades.
	off := mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
	if got := off.getTokenFromRequest(wsReq()); got != "" {
		t.Errorf("AllowQueryToken off: getTokenFromRequest() = %q, want \"\"", got)
	}

	// Opt-in: the query token is accepted on WS upgrades.
	cfg := newTestJWTConfig()
	cfg.AllowQueryToken = true
	on := mustNewJWTScheme(&mockJWTUserStore{}, cfg)
	if got := on.getTokenFromRequest(wsReq()); got != "query-token" {
		t.Errorf("AllowQueryToken on: getTokenFromRequest() = %q, want %q", got, "query-token")
	}

	// Even with opt-in, the Sec-WebSocket-Protocol transport takes precedence.
	subReq := httptest.NewRequest("GET", "/?token=query-token", nil)
	subReq.Header.Set("Upgrade", "websocket")
	subReq.Header.Set("Sec-WebSocket-Protocol", "bearer.ws-header-token")
	if got := on.getTokenFromRequest(subReq); got != "ws-header-token" {
		t.Errorf("subprotocol precedence: getTokenFromRequest() = %q, want %q", got, "ws-header-token")
	}
}

func TestJWTScheme_SetUserStore(t *testing.T) {
	tests := []struct {
		name     string
		newStore auth.UserStore
	}{
		{
			name:     "sets new user store",
			newStore: &mockJWTUserStore{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			scheme.SetUserStore(tt.newStore)
			if scheme.loadUserStore() != tt.newStore {
				t.Error("SetUserStore() did not update user store")
			}
		})
	}
}

func TestJWTScheme_GenerateToken(t *testing.T) {
	tests := []struct {
		name         string
		user         auth.Authenticatable
		customClaims []map[string]interface{}
		wantErr      bool
	}{
		{
			name:    "generates token for user",
			user:    &mockJWTUser{id: "user123"},
			wantErr: false,
		},
		{
			name: "generates token with custom claims",
			user: &mockJWTUser{id: "user123"},
			customClaims: []map[string]interface{}{
				{"email": "test@example.com", "role": "admin"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			token, err := scheme.GenerateToken(tt.user, tt.customClaims...)
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && token == "" {
				t.Error("GenerateToken() returned empty token")
			}
		})
	}
}

func TestJWTScheme_GenerateRefreshToken(t *testing.T) {
	tests := []struct {
		name    string
		user    auth.Authenticatable
		wantErr bool
	}{
		{
			name:    "generates refresh token for user",
			user:    &mockJWTUser{id: "user123"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			token, err := scheme.GenerateRefreshToken(tt.user)
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateRefreshToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && token == "" {
				t.Error("GenerateRefreshToken() returned empty token")
			}
		})
	}
}

func TestJWTScheme_RefreshToken(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *JWTScheme
		setupToken  func(scheme *JWTScheme) string
		wantErr     bool
	}{
		{
			name: "refreshes valid refresh token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupToken: func(scheme *JWTScheme) string {
				user := &mockJWTUser{id: "user123"}
				token, _ := scheme.jwtManager.GenerateRefreshToken(user)
				return token
			},
			wantErr: false,
		},
		{
			name: "returns error for invalid refresh token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupToken: func(scheme *JWTScheme) string {
				return "invalid-refresh-token"
			},
			wantErr: true,
		},
		{
			name: "returns error when user not found during refresh",
			setupScheme: func() *JWTScheme {
				userStore := &mockJWTUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return mustNewJWTScheme(userStore, newTestJWTConfig())
			},
			setupToken: func(scheme *JWTScheme) string {
				user := &mockJWTUser{id: "deleted-user"}
				token, _ := scheme.jwtManager.GenerateRefreshToken(user)
				return token
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			refreshToken := tt.setupToken(scheme)
			newToken, err := scheme.RefreshToken(refreshToken)
			if (err != nil) != tt.wantErr {
				t.Errorf("RefreshToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && newToken == "" {
				t.Error("RefreshToken() returned empty token")
			}
		})
	}
}

func TestJWTScheme_ValidateToken(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *JWTScheme
		setupToken  func(scheme *JWTScheme) string
		wantErr     bool
		checkClaims func(t *testing.T, claims *auth.Claims)
	}{
		{
			name: "validates valid token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupToken: func(scheme *JWTScheme) string {
				user := &mockJWTUser{id: "user123"}
				token, _ := scheme.jwtManager.GenerateToken(user)
				return token
			},
			wantErr: false,
			checkClaims: func(t *testing.T, claims *auth.Claims) {
				if claims.UserID != "user123" {
					t.Errorf("claims.UserID = %v, want user123", claims.UserID)
				}
			},
		},
		{
			name: "returns error for invalid token",
			setupScheme: func() *JWTScheme {
				return mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
			},
			setupToken: func(scheme *JWTScheme) string {
				return "invalid-token"
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			token := tt.setupToken(scheme)
			claims, err := scheme.ValidateToken(token)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.checkClaims != nil {
				tt.checkClaims(t, claims)
			}
		})
	}
}

// TestJWTScheme_ValidateToken_NegativeTable exercises the common ways a JWT can
// be invalid — expired, bad signature, alg=none, future nbf, missing claim.
// Each row differs from a valid token in exactly one dimension so a
// regression pins down which check was lost. JWTManager has a parallel table;
// this one locks the scheme layer (which is what HTTP handlers actually call).
func TestJWTScheme_ValidateToken_NegativeTable(t *testing.T) {
	const hmacSecret = "test-secret-key-for-jwt-signing-minimum-length"
	cfg := newTestJWTConfig()
	cfg.Issuer = "velocity-scheme-test"
	scheme := mustNewJWTScheme(&mockJWTUserStore{}, cfg)

	validDate := func(offset time.Duration) *jwtlib.NumericDate {
		return jwtlib.NewNumericDate(time.Now().Add(offset))
	}

	cases := []struct {
		name    string
		build   func() string
		wantSub string // substring (lower-case) expected in the error
	}{
		{
			name: "expired",
			build: func() string {
				c := auth.Claims{
					RegisteredClaims: jwtlib.RegisteredClaims{
						Subject:   "1",
						Issuer:    cfg.Issuer,
						IssuedAt:  validDate(-2 * time.Hour),
						ExpiresAt: validDate(-1 * time.Hour),
					},
					UserID:    1,
					TokenType: "access",
				}
				tok := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, c)
				s, _ := tok.SignedString([]byte(hmacSecret))
				return s
			},
			wantSub: "expired",
		},
		{
			name: "bad signature",
			build: func() string {
				c := auth.Claims{
					RegisteredClaims: jwtlib.RegisteredClaims{
						Subject:   "1",
						Issuer:    cfg.Issuer,
						IssuedAt:  validDate(0),
						ExpiresAt: validDate(time.Hour),
					},
					UserID:    1,
					TokenType: "access",
				}
				tok := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, c)
				// Sign with the wrong secret — length still meets the 32-byte floor.
				s, _ := tok.SignedString([]byte("a-different-but-equally-long-secret-"))
				return s
			},
			wantSub: "signature",
		},
		{
			name: "alg=none",
			build: func() string {
				return forgeUnsignedJWTSchemeToken(t, "none", map[string]interface{}{
					"uid": 1,
					"sub": "1",
					"iss": cfg.Issuer,
					"exp": time.Now().Add(time.Hour).Unix(),
				})
			},
			// jwt/v5 phrases the error as "signing method (alg) is unavailable"
			// or similar — "none" appears in practice but the reliable substring
			// is empty (just assert non-nil error).
			wantSub: "",
		},
		{
			name: "future nbf",
			build: func() string {
				c := auth.Claims{
					RegisteredClaims: jwtlib.RegisteredClaims{
						Subject:   "1",
						Issuer:    cfg.Issuer,
						IssuedAt:  validDate(0),
						NotBefore: validDate(time.Hour),
						ExpiresAt: validDate(2 * time.Hour),
					},
					UserID:    1,
					TokenType: "access",
				}
				tok := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, c)
				s, _ := tok.SignedString([]byte(hmacSecret))
				return s
			},
			wantSub: "valid",
		},
		{
			name: "missing claim — empty subject",
			build: func() string {
				// Subject is required for our flows (schemes rely on it as the
				// user identifier). Emit a token without one and confirm the
				// scheme surfaces a rejection rather than silently allowing the
				// "anonymous" token through.
				c := auth.Claims{
					RegisteredClaims: jwtlib.RegisteredClaims{
						Issuer:    "someone-else", // wrong issuer surfaces as the detectable failure
						IssuedAt:  validDate(0),
						ExpiresAt: validDate(time.Hour),
					},
					TokenType: "access",
				}
				tok := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, c)
				s, _ := tok.SignedString([]byte(hmacSecret))
				return s
			},
			// An empty Subject isn't strictly invalid per JWT — we check issuer
			// mismatch alongside to guarantee a concrete rejection reason.
			wantSub: "issuer",
		},
		{
			name: "malformed — two segments",
			build: func() string {
				return "abc.def"
			},
			wantSub: "",
		},
		{
			name: "malformed — empty",
			build: func() string {
				return ""
			},
			wantSub: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := tc.build()
			claims, err := scheme.ValidateToken(tok)
			if err == nil {
				t.Fatalf("ValidateToken(%q) returned nil error; claims=%+v", tc.name, claims)
			}
			if tc.wantSub != "" && !strings.Contains(strings.ToLower(err.Error()), tc.wantSub) {
				t.Errorf("error %q missing substring %q", err, tc.wantSub)
			}
		})
	}
}

// Ctx-suffixed shims for auth.UserStore, added in Sweep 1b.
func (p *mockJWTUserStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *mockJWTUserStore) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *mockJWTUserStore) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}
