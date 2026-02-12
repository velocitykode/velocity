package guards

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/pkg/auth"
	"github.com/velocitykode/velocity/pkg/crypto"
)

// mockSessionGuardUserProvider implements auth.UserProvider for session guard tests
type mockSessionGuardUserProvider struct {
	findByIDFunc            func(id interface{}) (auth.Authenticatable, error)
	findByCredentialsFunc   func(credentials map[string]interface{}) (auth.Authenticatable, error)
	validateCredentialsFunc func(user auth.Authenticatable, credentials map[string]interface{}) bool
	updateRememberTokenFunc func(user auth.Authenticatable, token string) error
}

func (p *mockSessionGuardUserProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
	if p.findByIDFunc != nil {
		return p.findByIDFunc(id)
	}
	return &mockSessionGuardUser{id: id, password: "hashedpassword"}, nil
}

func (p *mockSessionGuardUserProvider) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	if p.findByCredentialsFunc != nil {
		return p.findByCredentialsFunc(credentials)
	}
	return &mockSessionGuardUser{id: "user123", email: "test@example.com", password: "hashedpassword"}, nil
}

func (p *mockSessionGuardUserProvider) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	if p.validateCredentialsFunc != nil {
		return p.validateCredentialsFunc(user, credentials)
	}
	return true
}

func (p *mockSessionGuardUserProvider) UpdateRememberToken(user auth.Authenticatable, token string) error {
	if p.updateRememberTokenFunc != nil {
		return p.updateRememberTokenFunc(user, token)
	}
	return nil
}

// mockSessionGuardUser implements auth.Authenticatable for session guard tests
type mockSessionGuardUser struct {
	id            interface{}
	email         string
	password      string
	rememberToken string
}

func (u *mockSessionGuardUser) GetAuthIdentifier() interface{} {
	return u.id
}

func (u *mockSessionGuardUser) GetAuthPassword() string {
	return u.password
}

func (u *mockSessionGuardUser) GetRememberToken() string {
	return u.rememberToken
}

func (u *mockSessionGuardUser) SetRememberToken(token string) {
	u.rememberToken = token
}

// mockSessionGuardSession implements auth.Session for testing
type mockSessionGuardSession struct {
	id              string
	data            map[string]interface{}
	flash           map[string]interface{}
	saveError       error
	regenerateError error
	invalidateError error
	regenerated     bool
	invalidated     bool
}

func newMockSessionGuardSession(id string) *mockSessionGuardSession {
	if id == "" {
		id = "test-session-id"
	}
	return &mockSessionGuardSession{
		id:    id,
		data:  make(map[string]interface{}),
		flash: make(map[string]interface{}),
	}
}

func (s *mockSessionGuardSession) ID() string                        { return s.id }
func (s *mockSessionGuardSession) Get(key string) interface{}        { return s.data[key] }
func (s *mockSessionGuardSession) Put(key string, value interface{}) { s.data[key] = value }
func (s *mockSessionGuardSession) Has(key string) bool               { _, ok := s.data[key]; return ok }
func (s *mockSessionGuardSession) Remove(key string)                 { delete(s.data, key) }
func (s *mockSessionGuardSession) Clear()                            { s.data = make(map[string]interface{}) }
func (s *mockSessionGuardSession) Regenerate() error {
	s.regenerated = true
	return s.regenerateError
}
func (s *mockSessionGuardSession) Invalidate() error {
	s.invalidated = true
	s.data = make(map[string]interface{})
	return s.invalidateError
}
func (s *mockSessionGuardSession) Flash(key string, value interface{}) { s.flash[key] = value }
func (s *mockSessionGuardSession) GetFlash(key string) interface{} {
	v := s.flash[key]
	delete(s.flash, key)
	return v
}
func (s *mockSessionGuardSession) Save(w http.ResponseWriter) error { return s.saveError }

// mockSessionGuardStore implements auth.SessionStore for testing
type mockSessionGuardStore struct {
	createFunc  func(id string) (auth.Session, error)
	getFunc     func(r *http.Request, id string) (auth.Session, error)
	saveFunc    func(w http.ResponseWriter, session auth.Session) error
	destroyFunc func(id string) error
	gcFunc      func(maxLifetime time.Duration) error
}

func (s *mockSessionGuardStore) Create(id string) (auth.Session, error) {
	if s.createFunc != nil {
		return s.createFunc(id)
	}
	return newMockSessionGuardSession(id), nil
}

func (s *mockSessionGuardStore) Get(r *http.Request, id string) (auth.Session, error) {
	if s.getFunc != nil {
		return s.getFunc(r, id)
	}
	return newMockSessionGuardSession(id), nil
}

func (s *mockSessionGuardStore) Save(w http.ResponseWriter, session auth.Session) error {
	if s.saveFunc != nil {
		return s.saveFunc(w, session)
	}
	return nil
}

func (s *mockSessionGuardStore) Destroy(id string) error {
	if s.destroyFunc != nil {
		return s.destroyFunc(id)
	}
	return nil
}

func (s *mockSessionGuardStore) GarbageCollect(maxLifetime time.Duration) error {
	if s.gcFunc != nil {
		return s.gcFunc(maxLifetime)
	}
	return nil
}

func newTestSessionConfig() auth.SessionConfig {
	return auth.SessionConfig{
		Driver:   "cookie",
		Name:     "test_session",
		Lifetime: 120,
		Path:     "/",
		Secure:   false,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func TestNewSessionGuard(t *testing.T) {
	// Create encryptor instance for cookie store creation
	encryptor, err := crypto.NewEncryptor(crypto.Config{
		Key:    "test-key-32-bytes-long-for-test!",
		Cipher: "AES-256-CBC",
	})
	if err != nil {
		t.Fatalf("Failed to create encryptor: %v", err)
	}

	tests := []struct {
		name     string
		provider auth.UserProvider
		config   auth.SessionConfig
		wantErr  bool
	}{
		{
			name:     "creates guard with cookie driver",
			provider: &mockSessionGuardUserProvider{},
			config: auth.SessionConfig{
				Driver: "cookie",
				Name:   "test_session",
			},
			wantErr: false,
		},
		{
			name:     "creates guard with empty driver defaults to cookie",
			provider: &mockSessionGuardUserProvider{},
			config: auth.SessionConfig{
				Driver: "",
				Name:   "test_session",
			},
			wantErr: false,
		},
		{
			name:     "creates guard with unknown driver defaults to cookie",
			provider: &mockSessionGuardUserProvider{},
			config: auth.SessionConfig{
				Driver: "unknown",
				Name:   "test_session",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard, err := NewSessionGuard(tt.provider, tt.config, encryptor)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewSessionGuard() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if guard == nil {
					t.Error("NewSessionGuard() returned nil guard")
					return
				}
				if guard.provider != tt.provider {
					t.Error("provider not set correctly")
				}
				if guard.store == nil {
					t.Error("store not initialized")
				}
			}
		})
	}
}

func TestSessionGuard_Check(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *SessionGuard
		setupReq   func() *http.Request
		want       bool
	}{
		{
			name: "returns true when session has user_id and user exists",
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				session.Put("user_id", int64(123))
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("session not found")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create session")
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				// No user_id set
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				session.Put("user_id", int64(123))
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				provider := &mockSessionGuardUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return &SessionGuard{
					provider: provider,
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				session.Put("user_id", int64(123))
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				provider := &mockSessionGuardUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, nil
					},
				}
				return &SessionGuard{
					provider: provider,
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			guard := tt.setupGuard()
			req := tt.setupReq()
			got := guard.Check(req)
			if got != tt.want {
				t.Errorf("Check() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionGuard_User(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *SessionGuard
		setupReq   func() *http.Request
		wantNil    bool
		wantID     interface{}
	}{
		{
			name: "returns user when session has user_id",
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				session.Put("user_id", "user123")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("session not found")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create")
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			wantNil: true,
		},
		{
			name: "returns nil when provider returns error",
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				session.Put("user_id", "user123")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				provider := &mockSessionGuardUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("database error")
					},
				}
				return &SessionGuard{
					provider: provider,
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			guard := tt.setupGuard()
			req := tt.setupReq()
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

func TestSessionGuard_ID(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *SessionGuard
		setupReq   func() *http.Request
		wantNil    bool
		wantID     interface{}
	}{
		{
			name: "returns user_id from session",
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				session.Put("user_id", int64(456))
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("no session")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create")
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			guard := tt.setupGuard()
			req := tt.setupReq()
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

func TestSessionGuard_Login(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *SessionGuard
		setupReq   func() *http.Request
		user       auth.Authenticatable
		remember   []bool
		wantErr    bool
		checkGuard func(t *testing.T, guard *SessionGuard, req *http.Request)
	}{
		{
			name: "stores user_id in session and regenerates session",
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/login", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			user:    &mockSessionGuardUser{id: "newuser123"},
			wantErr: false,
			checkGuard: func(t *testing.T, guard *SessionGuard, req *http.Request) {
				session := guard.getSession(req)
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
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("no session")
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/login", nil)
			},
			user:    &mockSessionGuardUser{id: "user123"},
			wantErr: false,
		},
		{
			name: "returns error when session creation fails",
			setupGuard: func() *SessionGuard {
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("no session")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create session")
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/login", nil)
			},
			user:    &mockSessionGuardUser{id: "user123"},
			wantErr: true,
		},
		{
			name: "returns error when session save fails",
			setupGuard: func() *SessionGuard {
				session := &mockSessionGuardSession{
					id:        "test-id",
					data:      make(map[string]interface{}),
					flash:     make(map[string]interface{}),
					saveError: errors.New("save failed"),
				}
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
			},
			setupReq: func() *http.Request {
				req := httptest.NewRequest("POST", "/login", nil)
				req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
				return req
			},
			user:    &mockSessionGuardUser{id: "user123"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard := tt.setupGuard()
			req := tt.setupReq()
			w := httptest.NewRecorder()

			err := guard.Login(w, req, tt.user, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("Login() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.checkGuard != nil && !tt.wantErr {
				tt.checkGuard(t, guard, req)
			}
		})
	}
}

func TestSessionGuard_LoginByID(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *SessionGuard
		setupReq   func() *http.Request
		id         interface{}
		remember   []bool
		wantErr    bool
	}{
		{
			name: "logs in user by ID",
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				provider := &mockSessionGuardUserProvider{
					findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return &SessionGuard{
					provider: provider,
					store:    &mockSessionGuardStore{},
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/login", nil)
			},
			id:      "nonexistent",
			wantErr: true,
		},
		{
			name: "passes remember flag to Login",
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			guard := tt.setupGuard()

			// Set encryptor on guard for tests that use remember functionality
			if len(tt.remember) > 0 && tt.remember[0] {
				enc, err := crypto.NewEncryptor(crypto.Config{
					Key:    "test-key-32-bytes-long-for-test!",
					Cipher: "AES-256-CBC",
				})
				if err != nil {
					t.Fatalf("Failed to create encryptor: %v", err)
				}
				guard.encryptor = enc
			}

			req := tt.setupReq()
			w := httptest.NewRecorder()

			err := guard.LoginByID(w, req, tt.id, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoginByID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSessionGuard_Attempt(t *testing.T) {
	tests := []struct {
		name        string
		setupGuard  func() *SessionGuard
		setupReq    func() *http.Request
		credentials map[string]interface{}
		remember    []bool
		wantSuccess bool
		wantErr     bool
	}{
		{
			name: "returns true for valid credentials",
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				provider := &mockSessionGuardUserProvider{
					findByCredentialsFunc: func(credentials map[string]interface{}) (auth.Authenticatable, error) {
						return nil, errors.New("user not found")
					},
				}
				return &SessionGuard{
					provider: provider,
					store:    &mockSessionGuardStore{},
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    &mockSessionGuardStore{},
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				provider := &mockSessionGuardUserProvider{
					validateCredentialsFunc: func(user auth.Authenticatable, credentials map[string]interface{}) bool {
						return false
					},
				}
				return &SessionGuard{
					provider: provider,
					store:    &mockSessionGuardStore{},
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("no session")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create session")
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			guard := tt.setupGuard()
			req := tt.setupReq()
			w := httptest.NewRecorder()

			success, err := guard.Attempt(w, req, tt.credentials, tt.remember...)
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

func TestSessionGuard_Logout(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *SessionGuard
		setupReq   func() *http.Request
		wantErr    bool
		checkResp  func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{
			name: "invalidates session successfully",
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				session.Put("user_id", "user123")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("no session")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("cannot create")
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("POST", "/logout", nil)
			},
			wantErr: false,
		},
		{
			name: "returns error when session invalidation fails",
			setupGuard: func() *SessionGuard {
				session := &mockSessionGuardSession{
					id:              "test-id",
					data:            make(map[string]interface{}),
					flash:           make(map[string]interface{}),
					invalidateError: errors.New("invalidate failed"),
				}
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				session := newMockSessionGuardSession("test-id")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			guard := tt.setupGuard()
			req := tt.setupReq()
			w := httptest.NewRecorder()

			err := guard.Logout(w, req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Logout() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.checkResp != nil {
				tt.checkResp(t, w)
			}
		})
	}
}

func TestSessionGuard_SetProvider(t *testing.T) {
	tests := []struct {
		name        string
		newProvider auth.UserProvider
	}{
		{
			name:        "sets new provider",
			newProvider: &mockSessionGuardUserProvider{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard := &SessionGuard{
				provider: &mockSessionGuardUserProvider{},
				store:    &mockSessionGuardStore{},
				config:   newTestSessionConfig(),
				hasher:   auth.NewBcryptHasher(10),
			}
			guard.SetProvider(tt.newProvider)
			if guard.provider != tt.newProvider {
				t.Error("SetProvider() did not update provider")
			}
		})
	}
}

func TestSessionGuard_getSession(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() *SessionGuard
		setupReq   func() *http.Request
		wantNil    bool
	}{
		{
			name: "returns cached session",
			setupGuard: func() *SessionGuard {
				guard := &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    &mockSessionGuardStore{},
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
				return guard
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
			setupGuard: func() *SessionGuard {
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("session not found")
					},
					createFunc: func(id string) (auth.Session, error) {
						return newMockSessionGuardSession("new-session"), nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			setupGuard: func() *SessionGuard {
				store := &mockSessionGuardStore{
					createFunc: func(id string) (auth.Session, error) {
						return newMockSessionGuardSession("new-session"), nil
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
			},
			setupReq: func() *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			wantNil: false,
		},
		{
			name: "returns nil when session creation fails",
			setupGuard: func() *SessionGuard {
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return nil, errors.New("get failed")
					},
					createFunc: func(id string) (auth.Session, error) {
						return nil, errors.New("create failed")
					},
				}
				return &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
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
			guard := tt.setupGuard()
			req := tt.setupReq()
			got := guard.getSession(req)
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

func TestSessionGuard_SessionRegeneration(t *testing.T) {
	tests := []struct {
		name       string
		setupGuard func() (*SessionGuard, *mockSessionGuardSession)
		user       auth.Authenticatable
		wantRegen  bool
	}{
		{
			name: "regenerates session ID on login for security",
			setupGuard: func() (*SessionGuard, *mockSessionGuardSession) {
				session := newMockSessionGuardSession("old-session-id")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				guard := &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
				return guard, session
			},
			user:      &mockSessionGuardUser{id: "user123"},
			wantRegen: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard, session := tt.setupGuard()
			req := httptest.NewRequest("POST", "/login", nil)
			req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
			w := httptest.NewRecorder()

			err := guard.Login(w, req, tt.user)
			if err != nil {
				t.Fatalf("Login() error = %v", err)
			}

			if session.regenerated != tt.wantRegen {
				t.Errorf("session.regenerated = %v, want %v", session.regenerated, tt.wantRegen)
			}
		})
	}
}

func TestSessionGuard_SessionInvalidation(t *testing.T) {
	tests := []struct {
		name           string
		setupGuard     func() (*SessionGuard, *mockSessionGuardSession)
		wantInvalidate bool
	}{
		{
			name: "invalidates session on logout",
			setupGuard: func() (*SessionGuard, *mockSessionGuardSession) {
				session := newMockSessionGuardSession("session-id")
				session.Put("user_id", "user123")
				store := &mockSessionGuardStore{
					getFunc: func(r *http.Request, id string) (auth.Session, error) {
						return session, nil
					},
					createFunc: func(id string) (auth.Session, error) {
						return session, nil
					},
				}
				guard := &SessionGuard{
					provider: &mockSessionGuardUserProvider{},
					store:    store,
					config:   newTestSessionConfig(),
					hasher:   auth.NewBcryptHasher(10),
				}
				return guard, session
			},
			wantInvalidate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard, session := tt.setupGuard()
			req := httptest.NewRequest("POST", "/logout", nil)
			req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
			w := httptest.NewRecorder()

			err := guard.Logout(w, req)
			if err != nil {
				t.Fatalf("Logout() error = %v", err)
			}

			if session.invalidated != tt.wantInvalidate {
				t.Errorf("session.invalidated = %v, want %v", session.invalidated, tt.wantInvalidate)
			}
		})
	}
}
