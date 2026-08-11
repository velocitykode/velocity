package guards

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

// mockSessionSchemeUserStore implements auth.UserStore for session scheme tests
type mockSessionSchemeUserStore struct {
	findByIDFunc            func(id interface{}) (auth.Authenticatable, error)
	findByCredentialsFunc   func(credentials map[string]interface{}) (auth.Authenticatable, error)
	validateCredentialsFunc func(user auth.Authenticatable, credentials map[string]interface{}) bool
	updateRememberTokenFunc func(user auth.Authenticatable, token string) error
}

func (p *mockSessionSchemeUserStore) FindByID(id interface{}) (auth.Authenticatable, error) {
	if p.findByIDFunc != nil {
		return p.findByIDFunc(id)
	}
	return &mockSessionSchemeUser{id: id, password: "hashedpassword"}, nil
}

func (p *mockSessionSchemeUserStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}

func (p *mockSessionSchemeUserStore) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	if p.findByCredentialsFunc != nil {
		return p.findByCredentialsFunc(credentials)
	}
	return &mockSessionSchemeUser{id: "user123", email: "test@example.com", password: "hashedpassword"}, nil
}

func (p *mockSessionSchemeUserStore) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}

func (p *mockSessionSchemeUserStore) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	if p.validateCredentialsFunc != nil {
		return p.validateCredentialsFunc(user, credentials)
	}
	return true
}

func (p *mockSessionSchemeUserStore) UpdateRememberToken(user auth.Authenticatable, token string) error {
	if p.updateRememberTokenFunc != nil {
		return p.updateRememberTokenFunc(user, token)
	}
	return nil
}

func (p *mockSessionSchemeUserStore) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}

// mockSessionSchemeUser implements auth.Authenticatable for session scheme tests
type mockSessionSchemeUser struct {
	id            interface{}
	email         string
	password      string
	rememberToken string
}

func (u *mockSessionSchemeUser) GetAuthIdentifier() interface{} {
	return u.id
}

func (u *mockSessionSchemeUser) GetAuthPassword() string {
	return u.password
}

func (u *mockSessionSchemeUser) GetRememberToken() string {
	return u.rememberToken
}

func (u *mockSessionSchemeUser) SetRememberToken(token string) {
	u.rememberToken = token
}

// mockSessionSchemeSession implements auth.Session for testing
type mockSessionSchemeSession struct {
	id              string
	data            map[string]interface{}
	flash           map[string]interface{}
	saveError       error
	regenerateError error
	invalidateError error
	regenerated     bool
	invalidated     bool
}

func newMockSessionSchemeSession(id string) *mockSessionSchemeSession {
	if id == "" {
		id = "test-session-id"
	}
	return &mockSessionSchemeSession{
		id:    id,
		data:  make(map[string]interface{}),
		flash: make(map[string]interface{}),
	}
}

func (s *mockSessionSchemeSession) ID() string                        { return s.id }
func (s *mockSessionSchemeSession) Get(key string) interface{}        { return s.data[key] }
func (s *mockSessionSchemeSession) Put(key string, value interface{}) { s.data[key] = value }
func (s *mockSessionSchemeSession) Has(key string) bool               { _, ok := s.data[key]; return ok }
func (s *mockSessionSchemeSession) Remove(key string)                 { delete(s.data, key) }
func (s *mockSessionSchemeSession) Clear()                            { s.data = make(map[string]interface{}) }
func (s *mockSessionSchemeSession) Regenerate() error {
	s.regenerated = true
	return s.regenerateError
}
func (s *mockSessionSchemeSession) Invalidate() error {
	s.invalidated = true
	s.data = make(map[string]interface{})
	return s.invalidateError
}
func (s *mockSessionSchemeSession) Flash(key string, value interface{}) { s.flash[key] = value }
func (s *mockSessionSchemeSession) GetFlash(key string) interface{} {
	v := s.flash[key]
	delete(s.flash, key)
	return v
}
func (s *mockSessionSchemeSession) FlushFlash() map[string]interface{} {
	if len(s.flash) == 0 {
		return nil
	}
	out := s.flash
	s.flash = make(map[string]interface{})
	return out
}
func (s *mockSessionSchemeSession) Save(w http.ResponseWriter) error { return s.saveError }

// mockSessionSchemeStore implements auth.SessionStore for testing
type mockSessionSchemeStore struct {
	createFunc  func(id string) (auth.Session, error)
	getFunc     func(r *http.Request, id string) (auth.Session, error)
	saveFunc    func(w http.ResponseWriter, session auth.Session) error
	destroyFunc func(id string) error
	gcFunc      func(maxLifetime time.Duration) error
}

func (s *mockSessionSchemeStore) Create(id string) (auth.Session, error) {
	if s.createFunc != nil {
		return s.createFunc(id)
	}
	return newMockSessionSchemeSession(id), nil
}

func (s *mockSessionSchemeStore) Get(r *http.Request, id string) (auth.Session, error) {
	if s.getFunc != nil {
		return s.getFunc(r, id)
	}
	return newMockSessionSchemeSession(id), nil
}

func (s *mockSessionSchemeStore) Save(w http.ResponseWriter, session auth.Session) error {
	if s.saveFunc != nil {
		return s.saveFunc(w, session)
	}
	return nil
}

func (s *mockSessionSchemeStore) Destroy(id string) error {
	if s.destroyFunc != nil {
		return s.destroyFunc(id)
	}
	return nil
}

func (s *mockSessionSchemeStore) GarbageCollect(maxLifetime time.Duration) error {
	if s.gcFunc != nil {
		return s.gcFunc(maxLifetime)
	}
	return nil
}

func newTestSessionConfig() auth.SessionConfig {
	return auth.SessionConfig{
		Name:     "test_session",
		Lifetime: 120,
		Path:     "/",
		Secure:   false,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func TestNewSessionScheme(t *testing.T) {
	// Create encryptor instance for cookie store creation
	encryptor, err := crypto.NewEncryptor(crypto.Config{
		Key:    "test-key-32-bytes-long-for-test!",
		Cipher: "AES-256-CBC",
	})
	if err != nil {
		t.Fatalf("Failed to create encryptor: %v", err)
	}

	tests := []struct {
		name      string
		userStore auth.UserStore
		config    auth.SessionConfig
		wantErr   bool
	}{
		{
			name:      "creates scheme with cookie store",
			userStore: &mockSessionSchemeUserStore{},
			config: auth.SessionConfig{
				Name: "test_session",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, err := NewSessionScheme(tt.userStore, tt.config, encryptor)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewSessionScheme() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if scheme == nil {
					t.Error("NewSessionScheme() returned nil scheme")
					return
				}
				if scheme.loadUserStore() != tt.userStore {
					t.Error("provider not set correctly")
				}
				if scheme.store == nil {
					t.Error("store not initialized")
				}
			}
		})
	}
}

func TestSessionScheme_Check(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *SessionScheme
		setupReq    func() *http.Request
		want        bool
	}{
		{
			name: "returns true when session has user_id and user exists",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				session.Put("user_id", int64(123))
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			want: true,
		},
		{
			name: "returns false when session is nil",
			setupScheme: func() *SessionScheme {
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("session not found")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create session")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			want: false,
		},
		{
			name: "returns false when session has no user_id",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				// No user_id set
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			want: false,
		},
		{
			name: "returns false when user not found",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				session.Put("user_id", int64(123))
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				userStore := &mockSessionSchemeUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: userStore})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			want: false,
		},
		{
			name: "returns false when user is nil",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				session.Put("user_id", int64(123))
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				userStore := &mockSessionSchemeUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: userStore})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq()
			got := scheme.Check(req)
			if got != tt.want {
				t.Errorf("Check() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionScheme_User(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *SessionScheme
		setupReq    func() *http.Request
		wantNil     bool
		wantID      interface{}
	}{
		{
			name: "returns user when session has user_id",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				session.Put("user_id", "user123")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantNil: false,
			wantID:  "user123",
		},
		{
			name: "returns nil when session is nil",
			setupScheme: func() *SessionScheme {
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("session not found")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantNil: true,
		},
		{
			name: "returns nil when session has no user_id",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantNil: true,
		},
		{
			name: "returns nil when user store returns error",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				session.Put("user_id", "user123")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				userStore := &mockSessionSchemeUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("database error")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: userStore})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq()
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

func TestSessionScheme_ID(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *SessionScheme
		setupReq    func() *http.Request
		wantNil     bool
		wantID      interface{}
	}{
		{
			name: "returns user_id from session",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				session.Put("user_id", int64(456))
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantNil: false,
			wantID:  int64(456),
		},
		{
			name: "returns nil when session is nil",
			setupScheme: func() *SessionScheme {
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("no session")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantNil: true,
		},
		{
			name: "returns nil when session has no user_id",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq()
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

func TestSessionScheme_Login(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *SessionScheme
		setupReq    func() *http.Request
		user        auth.Authenticatable
		remember    []bool
		wantErr     bool
		checkScheme func(t *testing.T, scheme *SessionScheme, req *http.Request)
	}{
		{
			name: "stores user_id in session and regenerates session",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/login", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			user:    &mockSessionSchemeUser{id: "newuser123"},
			wantErr: false,
			checkScheme: func(t *testing.T, scheme *SessionScheme, req *http.Request) {
				session := scheme.getSession(req)
				if session == nil {
					t.Error("session should not be nil after login")
					return
				}
				userID := session.Get("user_id")
				if userID != "newuser123" {
					t.Errorf("session user_id = %v, want newuser123", userID)
				}
			},
		},
		{
			name: "creates new session if none exists",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("no session")
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/login", nil)
			},
			user:    &mockSessionSchemeUser{id: "user123"},
			wantErr: false,
		},
		{
			name: "returns error when session creation fails",
			setupScheme: func() *SessionScheme {
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("no session")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create session")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/login", nil)
			},
			user:    &mockSessionSchemeUser{id: "user123"},
			wantErr: true,
		},
		{
			name: "returns error when session save fails",
			setupScheme: func() *SessionScheme {
				session := &mockSessionSchemeSession{
					id:        "test-id",
					data:      make(map[string]interface{}),
					flash:     make(map[string]interface{}),
					saveError: errors.New("save failed"),
				}
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/login", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			user:    &mockSessionSchemeUser{id: "user123"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq()
			w := httptest.NewRecorder()

			err := scheme.Login(w, req, tt.user, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("Login() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.checkScheme != nil && !tt.wantErr {
				tt.checkScheme(t, scheme, req)
			}
		})
	}
}

func TestSessionScheme_LoginByID(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *SessionScheme
		setupReq    func() *http.Request
		id          interface{}
		remember    []bool
		wantErr     bool
	}{
		{
			name: "logs in user by ID",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/login", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			id:      "user123",
			wantErr: false,
		},
		{
			name: "returns error when user not found",
			setupScheme: func() *SessionScheme {
				userStore := &mockSessionSchemeUserStore{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  &mockSessionSchemeStore{},
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: userStore})
					return g
				}()
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/login", nil)
			},
			id:      "nonexistent",
			wantErr: true,
		},
		{
			name: "passes remember flag to Login",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/login", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			id:       "user123",
			remember: []bool{true},
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()

			// Set encryptor on scheme for tests that use remember functionality
			if len(tt.remember) > 0 && tt.remember[0] {
				enc, err := crypto.NewEncryptor(crypto.Config{
					Key:    "test-key-32-bytes-long-for-test!",
					Cipher: "AES-256-CBC",
				})
				if err != nil {
					t.Fatalf("Failed to create encryptor: %v", err)
				}
				scheme.encryptor = enc
			}

			req := tt.setupReq()
			w := httptest.NewRecorder()

			err := scheme.LoginByID(w, req, tt.id, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoginByID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSessionScheme_Attempt(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *SessionScheme
		setupReq    func() *http.Request
		credentials map[string]interface{}
		remember    []bool
		wantSuccess bool
		wantErr     bool
	}{
		{
			name: "returns true for valid credentials",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/login", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
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
			setupScheme: func() *SessionScheme {
				userStore := &mockSessionSchemeUserStore{
					findByCredentialsFunc: func(credentials map[string]interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  &mockSessionSchemeStore{},
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: userStore})
					return g
				}()
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/login", nil)
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
			setupScheme: func() *SessionScheme {
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  &mockSessionSchemeStore{},
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/login", nil)
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
			setupScheme: func() *SessionScheme {
				userStore := &mockSessionSchemeUserStore{
					validateCredentialsFunc: func(user auth.Authenticatable, credentials map[string]interface{}) bool {
						return false
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  &mockSessionSchemeStore{},
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: userStore})
					return g
				}()
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/login", nil)
			},
			credentials: map[string]interface{}{
				"email":    "test@example.com",
				"password": "wrongpassword",
			},
			wantSuccess: false,
			wantErr:     false,
		},
		{
			name: "returns error when login fails after successful validation",
			setupScheme: func() *SessionScheme {
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("no session")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create session")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/login", nil)
			},
			credentials: map[string]interface{}{
				"email":    "test@example.com",
				"password": "password",
			},
			wantSuccess: false,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq()
			w := httptest.NewRecorder()

			success, err := scheme.Attempt(w, req, tt.credentials, tt.remember...)
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

func TestSessionScheme_Logout(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *SessionScheme
		setupReq    func() *http.Request
		wantErr     bool
		checkResp   func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{
			name: "invalidates session successfully",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				session.Put("user_id", "user123")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/logout", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantErr: false,
		},
		{
			name: "returns nil when session is nil",
			setupScheme: func() *SessionScheme {
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("no session")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/logout", nil)
			},
			wantErr: false,
		},
		{
			name: "returns error when session invalidation fails",
			setupScheme: func() *SessionScheme {
				session := &mockSessionSchemeSession{
					id:              "test-id",
					data:            make(map[string]interface{}),
					flash:           make(map[string]interface{}),
					invalidateError: errors.New("invalidate failed"),
				}
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/logout", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantErr: true,
		},
		{
			name: "clears remember cookie on logout",
			setupScheme: func() *SessionScheme {
				session := newMockSessionSchemeSession("test-id")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/logout", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				req.AddCookie(&http.Cookie{Name: "remember_test_session", Value: "remember-token"})
				return req
			},
			wantErr: false,
			checkResp: func(t *testing.T, w *httptest.ResponseRecorder) {
				cookies := w.Result().Cookies()
				for _, cookie := range cookies {
					if cookie.Name == "remember_test_session" {
						if cookie.MaxAge != -1 {
							t.Errorf("remember cookie MaxAge = %v, want -1", cookie.MaxAge)
						}
						return
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq()
			w := httptest.NewRecorder()

			err := scheme.Logout(w, req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Logout() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.checkResp != nil {
				tt.checkResp(t, w)
			}
		})
	}
}

func TestSessionScheme_SetUserStore(t *testing.T) {
	tests := []struct {
		name     string
		newStore auth.UserStore
	}{
		{
			name:     "sets new provider",
			newStore: &mockSessionSchemeUserStore{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := func() *SessionScheme {
				g := &SessionScheme{
					store:  &mockSessionSchemeStore{},
					config: newTestSessionConfig(),
					hasher: auth.NewBcryptHasher(10),
				}
				g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
				return g
			}()
			scheme.SetUserStore(tt.newStore)
			if scheme.loadUserStore() != tt.newStore {
				t.Error("SetUserStore() did not update provider")
			}
		})
	}
}

func TestSessionScheme_getSession(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() *SessionScheme
		setupReq    func() *http.Request
		wantNil     bool
	}{
		{
			name: "returns cached session",
			setupScheme: func() *SessionScheme {
				scheme := func() *SessionScheme {
					g := &SessionScheme{
						store:  &mockSessionSchemeStore{},
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
				return scheme
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "cached-session-id"})
				return req
			},
			wantNil: false,
		},
		{
			name: "creates new session when cookie exists but session not found",
			setupScheme: func() *SessionScheme {
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("session not found")
					},
					createFunc: func(id string) (auth.Session, error) {
						return newMockSessionSchemeSession("new-session"), nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "expired-session-id"})
				return req
			},
			wantNil: false,
		},
		{
			name: "creates new session when no cookie exists",
			setupScheme: func() *SessionScheme {
				store := &mockSessionSchemeStore{
					createFunc: func(id string) (auth.Session, error) {
						return newMockSessionSchemeSession("new-session"), nil
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			wantNil: false,
		},
		{
			name: "returns nil when session creation fails",
			setupScheme: func() *SessionScheme {
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("get failed")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("create failed")
					},
				}
				return func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := tt.setupScheme()
			req := tt.setupReq()
			got := scheme.getSession(req)
			if tt.wantNil {
				if got != nil {
					t.Errorf("getSession() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Error("getSession() = nil, want non-nil")
			}
		})
	}
}

func TestSessionScheme_SessionRegeneration(t *testing.T) {
	tests := []struct {
		name        string
		setupScheme func() (*SessionScheme, *mockSessionSchemeSession)
		user        auth.Authenticatable
		wantRegen   bool
	}{
		{
			name: "regenerates session ID on login for security",
			setupScheme: func() (*SessionScheme, *mockSessionSchemeSession) {
				session := newMockSessionSchemeSession("old-session-id")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				scheme := func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
				return scheme, session
			},
			user:      &mockSessionSchemeUser{id: "user123"},
			wantRegen: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, session := tt.setupScheme()
			req := httptest.NewRequest("POST", "/login", nil)
			req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
			w := httptest.NewRecorder()

			err := scheme.Login(w, req, tt.user)
			if err != nil {
				t.Fatalf("Login() error = %v", err)
			}

			if session.regenerated != tt.wantRegen {
				t.Errorf("session.regenerated = %v, want %v", session.regenerated, tt.wantRegen)
			}
		})
	}
}

func TestSessionScheme_SessionInvalidation(t *testing.T) {
	tests := []struct {
		name           string
		setupScheme    func() (*SessionScheme, *mockSessionSchemeSession)
		wantInvalidate bool
	}{
		{
			name: "invalidates session on logout",
			setupScheme: func() (*SessionScheme, *mockSessionSchemeSession) {
				session := newMockSessionSchemeSession("session-id")
				session.Put("user_id", "user123")
				store := &mockSessionSchemeStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				scheme := func() *SessionScheme {
					g := &SessionScheme{
						store:  store,
						config: newTestSessionConfig(),
						hasher: auth.NewBcryptHasher(10),
					}
					g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
					return g
				}()
				return scheme, session
			},
			wantInvalidate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, session := tt.setupScheme()
			req := httptest.NewRequest("POST", "/logout", nil)
			req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
			w := httptest.NewRecorder()

			err := scheme.Logout(w, req)
			if err != nil {
				t.Fatalf("Logout() error = %v", err)
			}

			if session.invalidated != tt.wantInvalidate {
				t.Errorf("session.invalidated = %v, want %v", session.invalidated, tt.wantInvalidate)
			}
		})
	}
}

// TestLogin_RegenerateErrorFailsLogin pins the regression for the bug
// where Login() ignored session.Regenerate()'s error and proceeded with
// the OLD session ID — opening a session-fixation window. The injected
// session returns a regenerate error; Login must surface it (wrapped)
// and must NOT write user_id into the session.
func TestLogin_RegenerateErrorFailsLogin(t *testing.T) {
	originalID := "fixation-attacker-chose-this-id"
	session := newMockSessionSchemeSession(originalID)
	session.regenerateError = errors.New("store I/O failure")

	store := &mockSessionSchemeStore{
		getFunc: func(r *http.Request, id string) (auth.Session, error) {
			return session, nil
		},
		createFunc: func(id string) (auth.Session, error) {
			return session, nil
		},
	}
	scheme := func() *SessionScheme {
		g := &SessionScheme{
			store:  store,
			config: newTestSessionConfig(),
			hasher: auth.NewBcryptHasher(10),
		}
		g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})
		return g
	}()

	req := httptest.NewRequest("POST", "/login", nil)
	req.AddCookie(&http.Cookie{Name: "test_session", Value: originalID})
	w := httptest.NewRecorder()
	user := &mockSessionSchemeUser{id: "victim123"}

	err := scheme.Login(w, req, user)
	if err == nil {
		t.Fatal("expected Login to fail when Regenerate fails")
	}
	if !errors.Is(err, session.regenerateError) {
		t.Errorf("expected wrapped regenerate error, got %v", err)
	}

	// CRITICAL: the old session ID must not now be bound to the user —
	// that would be the session-fixation bug.
	if got := session.Get("user_id"); got != nil {
		t.Errorf("session.user_id set despite Regenerate failure: %v (session-fixation leak)", got)
	}
	// Regenerate was attempted; the ID is unchanged because our mock's
	// Regenerate doesn't mutate on error, mirroring BaseSession behaviour.
	if session.id != originalID {
		t.Errorf("session id mutated despite regenerate error: before=%q after=%q", originalID, session.id)
	}
	// And we must not have called Save (would have persisted empty session data).
	if w.Result().Cookies() != nil && len(w.Result().Cookies()) > 0 {
		t.Errorf("no cookie should be written when Login fails pre-Save")
	}
}

// TestSessionScheme_LoginByID_UnknownID is the regression for the nil-user
// panic: UserStore.FindByID is contractually allowed to return (nil, nil)
// for an unknown id. LoginByID must surface that as auth.ErrUserNotFound
// instead of passing the nil user into Login and panicking on the user_id
// deref. Nothing may be written to the response.
func TestSessionScheme_LoginByID_UnknownID(t *testing.T) {
	userStore := &mockSessionSchemeUserStore{
		findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
			return nil, nil // not found, no error: contract-permitted
		},
	}
	g := &SessionScheme{
		store:  &mockSessionSchemeStore{},
		config: newTestSessionConfig(),
		hasher: auth.NewBcryptHasher(10),
	}
	g.userStore.Store(&userStoreHolder{p: userStore})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", nil)

	var err error
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("LoginByID panicked on unknown id: %v", rec)
			}
		}()
		err = g.LoginByID(w, r, "ghost")
	}()

	if !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("LoginByID error = %v, want auth.ErrUserNotFound", err)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("LoginByID wrote %d cookie(s) for an unknown id, want 0", len(cookies))
	}
}

// TestSessionScheme_Login_NilUser guards the deref site directly: Login is
// exported, so any caller (not just LoginByID) can reach it with a nil user.
// It must return auth.ErrUserNotFound before touching the session, never
// panic, and write nothing.
func TestSessionScheme_Login_NilUser(t *testing.T) {
	g := &SessionScheme{
		store:  &mockSessionSchemeStore{},
		config: newTestSessionConfig(),
		hasher: auth.NewBcryptHasher(10),
	}
	g.userStore.Store(&userStoreHolder{p: &mockSessionSchemeUserStore{}})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", nil)

	var err error
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("Login panicked on nil user: %v", rec)
			}
		}()
		err = g.Login(w, r, nil)
	}()

	if !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("Login(nil) error = %v, want auth.ErrUserNotFound", err)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("Login(nil) wrote %d cookie(s), want 0", len(cookies))
	}
}
