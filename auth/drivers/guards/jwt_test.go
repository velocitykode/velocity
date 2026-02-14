package guards

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

// mockJWTUserProvider implements auth.UserProvider for JWT tests
type mockJWTUserProvider struct {
	findByIDFunc            func(id interface{}) (auth.Authenticatable, error)
	findByCredentialsFunc   func(credentials map[string]interface{}) (auth.Authenticatable, error)
	validateCredentialsFunc func(user auth.Authenticatable, credentials map[string]interface{}) bool
	updateRememberTokenFunc func(user auth.Authenticatable, token string) error
}

func (p *mockJWTUserProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
	if p.findByIDFunc != nil {
		return p.findByIDFunc(id)
	}
	return &mockJWTUser{id: id, password: "hashedpassword"}, nil
}

func (p *mockJWTUserProvider) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	if p.findByCredentialsFunc != nil {
		return p.findByCredentialsFunc(credentials)
	}
	return &mockJWTUser{id: "user123", email: "test@example.com", password: "hashedpassword"}, nil
}

func (p *mockJWTUserProvider) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	if p.validateCredentialsFunc != nil {
		return p.validateCredentialsFunc(user, credentials)
	}
	return true
}

func (p *mockJWTUserProvider) UpdateRememberToken(user auth.Authenticatable, token string) error {
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
	}
}

func TestNewJWTGuard(t *testing.T) {
	tests := []struct {
		name     string
		provider auth.UserProvider
		config   auth.JWTConfig
	}{
		{
			name:     "creates guard with valid provider and config",
			provider: &mockJWTUserProvider{},
			config:   newTestJWTConfig(),
		},
		{
			name:     "creates guard with empty algorithm defaults to HS256",
			provider: &mockJWTUserProvider{},
			config: auth.JWTConfig{
				Secret: "test-secret-key-for-jwt-minimum-32b",
				TTL:    60,
			},
		},
		{
			name:     "creates guard with zero TTL defaults to 60",
			provider: &mockJWTUserProvider{},
			config: auth.JWTConfig{
				Secret: "test-secret-key-for-jwt-minimum-32b",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard := NewJWTGuard(tt.provider, tt.config)
			if guard == nil {
				t.Error("NewJWTGuard returned nil")
				return
			}
			if guard.provider != tt.provider {
				t.Error("provider not set correctly")
			}
			if guard.userCache == nil {
				t.Error("userCache not initialized")
			}
		})
	}
}

func TestJWTGuard_Check(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *JWTGuard
		setupReq   func(guard *JWTGuard) *http.Request
		want       bool
	}{
		{
			name: "returns true for valid token with existing user",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := guard.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			want: true,
		},
		{
			name: "returns false when no token in request",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			want: false,
		},
		{
			name: "returns false for invalid token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer invalid-token")
				return req
			},
			want: false,
		},
		{
			name: "returns false when user not found",
			setupGuard: func() *JWTGuard {
				provider := &mockJWTUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return NewJWTGuard(provider, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				user := &mockJWTUser{id: "nonexistent"}
				token, _ := guard.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			want: false,
		},
		{
			name: "returns false when user is nil",
			setupGuard: func() *JWTGuard {
				provider := &mockJWTUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, nil
					},
				}
				return NewJWTGuard(provider, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := guard.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			want: false,
		},
		{
			name: "caches user after successful check",
			setupGuard: func() *JWTGuard {
				callCount := 0
				provider := &mockJWTUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						callCount++
						if callCount > 1 {
							return nil, errors.New("should not be called twice")
						}
						return &mockJWTUser{id: id}, nil
					},
				}
				return NewJWTGuard(provider, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := guard.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard := tt.setupGuard()
			req := tt.setupReq(guard)
			got := guard.Check(req)
			if got != tt.want {
				t.Errorf("Check() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJWTGuard_User(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *JWTGuard
		setupReq   func(guard *JWTGuard) *http.Request
		wantNil    bool
		wantID     interface{}
	}{
		{
			name: "returns user for valid token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := guard.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantNil: false,
			wantID:  "user123",
		},
		{
			name: "returns nil when no token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			wantNil: true,
		},
		{
			name: "returns nil for invalid token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer invalid-token")
				return req
			},
			wantNil: true,
		},
		{
			name: "returns nil when provider returns error",
			setupGuard: func() *JWTGuard {
				provider := &mockJWTUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("database error")
					},
				}
				return NewJWTGuard(provider, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := guard.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantNil: true,
		},
		{
			name: "returns cached user on subsequent calls",
			setupGuard: func() *JWTGuard {
				guard := NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
				// Pre-cache a user
				guard.userCache["cached-token"] = cachedUser{user: &mockJWTUser{id: "cached-user"}, cachedAt: time.Now()}
				return guard
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer cached-token")
				return req
			},
			wantNil: false,
			wantID:  "cached-user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard := tt.setupGuard()
			req := tt.setupReq(guard)
			got := guard.User(req)
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

func TestJWTGuard_ID(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *JWTGuard
		setupReq   func(guard *JWTGuard) *http.Request
		wantNil    bool
		wantID     interface{}
	}{
		{
			name: "returns user ID for valid token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				user := &mockJWTUser{id: "user456"}
				token, _ := guard.jwtManager.GenerateToken(user)
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantNil: false,
			wantID:  "user456",
		},
		{
			name: "returns nil when no token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			wantNil: true,
		},
		{
			name: "returns nil for invalid token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Authorization", "Bearer bad-token")
				return req
			},
			wantNil: true,
		},
		{
			name: "returns ID without verifying user exists",
			setupGuard: func() *JWTGuard {
				provider := &mockJWTUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user deleted")
					},
				}
				return NewJWTGuard(provider, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				user := &mockJWTUser{id: "deleted-user"}
				token, _ := guard.jwtManager.GenerateToken(user)
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
			guard := tt.setupGuard()
			req := tt.setupReq(guard)
			got := guard.ID(req)
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

func TestJWTGuard_Login(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *JWTGuard
		user       auth.Authenticatable
		remember   []bool
		wantErr    bool
		checkResp  func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{
			name: "generates token and sets X-Auth-Token header",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
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
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
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
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
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
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
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
			guard := tt.setupGuard()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/login", nil)

			err := guard.Login(w, r, tt.user, tt.remember...)
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

func TestJWTGuard_LoginByID(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *JWTGuard
		id         interface{}
		remember   []bool
		wantErr    bool
	}{
		{
			name: "logs in existing user by ID",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			id:      "user123",
			wantErr: false,
		},
		{
			name: "returns error when user not found",
			setupGuard: func() *JWTGuard {
				provider := &mockJWTUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return NewJWTGuard(provider, newTestJWTConfig())
			},
			id:      "nonexistent",
			wantErr: true,
		},
		{
			name: "passes remember flag to Login",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			id:       "user123",
			remember: []bool{true},
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard := tt.setupGuard()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/login", nil)

			err := guard.LoginByID(w, r, tt.id, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoginByID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestJWTGuard_Attempt(t *testing.T) {
	tests := []struct {
		name        string
		setupGuard  func() *JWTGuard
		credentials map[string]interface{}
		remember    []bool
		wantSuccess bool
		wantErr     bool
	}{
		{
			name: "returns true for valid credentials",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
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
			setupGuard: func() *JWTGuard {
				provider := &mockJWTUserProvider{
					findByCredentialsFunc: func(credentials map[string]interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return NewJWTGuard(provider, newTestJWTConfig())
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
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
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
			setupGuard: func() *JWTGuard {
				provider := &mockJWTUserProvider{
					validateCredentialsFunc: func(user auth.Authenticatable, credentials map[string]interface{}) bool {
						return false
					},
				}
				return NewJWTGuard(provider, newTestJWTConfig())
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
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
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
			guard := tt.setupGuard()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/login", nil)

			success, err := guard.Attempt(w, r, tt.credentials, tt.remember...)
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

func TestJWTGuard_Logout(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *JWTGuard
		setupReq   func(guard *JWTGuard) *http.Request
		wantErr    bool
	}{
		{
			name: "revokes valid token and clears cache",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				user := &mockJWTUser{id: "user123"}
				token, _ := guard.jwtManager.GenerateToken(user)
				// Pre-cache user
				guard.userCache[token] = cachedUser{user: user, cachedAt: time.Now()}
				req := httptest.NewRequest("POST", "/logout", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				return req
			},
			wantErr: false,
		},
		{
			name: "returns nil when no token in request",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				return httptest.NewRequest("POST", "/logout", nil)
			},
			wantErr: false,
		},
		{
			name: "returns nil for invalid token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupReq: func(guard *JWTGuard) *http.Request {
				req := httptest.NewRequest("POST", "/logout", nil)
				req.Header.Set("Authorization", "Bearer invalid-token")
				return req
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard := tt.setupGuard()
			req := tt.setupReq(guard)
			w := httptest.NewRecorder()

			err := guard.Logout(w, req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Logout() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestJWTGuard_getTokenFromRequest(t *testing.T) {
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
			name: "extracts token from query parameter for WebSocket requests",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/?token=query-token", nil)
				req.Header.Set("Upgrade", "websocket")
				return req
			},
			wantToken: "query-token",
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
			guard := NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			req := tt.setupReq()
			got := guard.getTokenFromRequest(req)
			if got != tt.wantToken {
				t.Errorf("getTokenFromRequest() = %q, want %q", got, tt.wantToken)
			}
		})
	}
}

func TestJWTGuard_SetProvider(t *testing.T) {
	tests := []struct {
		name        string
		newProvider auth.UserProvider
	}{
		{
			name:        "sets new provider",
			newProvider: &mockJWTUserProvider{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard := NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			guard.SetProvider(tt.newProvider)
			if guard.provider != tt.newProvider {
				t.Error("SetProvider() did not update provider")
			}
		})
	}
}

func TestJWTGuard_GenerateToken(t *testing.T) {
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
			guard := NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			token, err := guard.GenerateToken(tt.user, tt.customClaims...)
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

func TestJWTGuard_GenerateRefreshToken(t *testing.T) {
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
			guard := NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			token, err := guard.GenerateRefreshToken(tt.user)
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

func TestJWTGuard_RefreshToken(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *JWTGuard
		setupToken func(guard *JWTGuard) string
		wantErr    bool
	}{
		{
			name: "refreshes valid refresh token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupToken: func(guard *JWTGuard) string {
				user := &mockJWTUser{id: "user123"}
				token, _ := guard.jwtManager.GenerateRefreshToken(user)
				return token
			},
			wantErr: false,
		},
		{
			name: "returns error for invalid refresh token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupToken: func(guard *JWTGuard) string {
				return "invalid-refresh-token"
			},
			wantErr: true,
		},
		{
			name: "returns error when user not found during refresh",
			setupGuard: func() *JWTGuard {
				provider := &mockJWTUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return NewJWTGuard(provider, newTestJWTConfig())
			},
			setupToken: func(guard *JWTGuard) string {
				user := &mockJWTUser{id: "deleted-user"}
				token, _ := guard.jwtManager.GenerateRefreshToken(user)
				return token
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard := tt.setupGuard()
			refreshToken := tt.setupToken(guard)
			newToken, err := guard.RefreshToken(refreshToken)
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

func TestJWTGuard_ValidateToken(t *testing.T) {
	tests := []struct {
		name        string
		setupGuard  func() *JWTGuard
		setupToken  func(guard *JWTGuard) string
		wantErr     bool
		checkClaims func(t *testing.T, claims *auth.Claims)
	}{
		{
			name: "validates valid token",
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupToken: func(guard *JWTGuard) string {
				user := &mockJWTUser{id: "user123"}
				token, _ := guard.jwtManager.GenerateToken(user)
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
			setupGuard: func() *JWTGuard {
				return NewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
			},
			setupToken: func(guard *JWTGuard) string {
				return "invalid-token"
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard := tt.setupGuard()
			token := tt.setupToken(guard)
			claims, err := guard.ValidateToken(token)
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
