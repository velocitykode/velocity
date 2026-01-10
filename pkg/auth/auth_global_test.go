package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// resetGlobalManager resets the global manager for testing
func resetGlobalManager() {
	globalMux.Lock()
	defer globalMux.Unlock()
	globalManager = nil
}

// mockGuard implements Guard interface for testing
type mockGuard struct {
	checkFn     func(*http.Request) bool
	userFn      func(*http.Request) Authenticatable
	idFn        func(*http.Request) interface{}
	loginFn     func(http.ResponseWriter, *http.Request, Authenticatable, ...bool) error
	loginByIDFn func(http.ResponseWriter, *http.Request, interface{}, ...bool) error
	attemptFn   func(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error)
	logoutFn    func(http.ResponseWriter, *http.Request) error
	provider    UserProvider
}

func (m *mockGuard) Check(r *http.Request) bool {
	if m.checkFn != nil {
		return m.checkFn(r)
	}
	return false
}

func (m *mockGuard) User(r *http.Request) Authenticatable {
	if m.userFn != nil {
		return m.userFn(r)
	}
	return nil
}

func (m *mockGuard) ID(r *http.Request) interface{} {
	if m.idFn != nil {
		return m.idFn(r)
	}
	return nil
}

func (m *mockGuard) Login(w http.ResponseWriter, r *http.Request, user Authenticatable, remember ...bool) error {
	if m.loginFn != nil {
		return m.loginFn(w, r, user, remember...)
	}
	return nil
}

func (m *mockGuard) LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
	if m.loginByIDFn != nil {
		return m.loginByIDFn(w, r, id, remember...)
	}
	return nil
}

func (m *mockGuard) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	if m.attemptFn != nil {
		return m.attemptFn(w, r, credentials, remember...)
	}
	return false, nil
}

func (m *mockGuard) Logout(w http.ResponseWriter, r *http.Request) error {
	if m.logoutFn != nil {
		return m.logoutFn(w, r)
	}
	return nil
}

func (m *mockGuard) SetProvider(provider UserProvider) {
	m.provider = provider
}

// mockUserProvider implements UserProvider interface for testing
type mockUserProvider struct {
	findByIDFn          func(id interface{}) (Authenticatable, error)
	findByCredentialsFn func(credentials map[string]interface{}) (Authenticatable, error)
	validateFn          func(user Authenticatable, credentials map[string]interface{}) bool
	updateTokenFn       func(user Authenticatable, token string) error
}

func (m *mockUserProvider) FindByID(id interface{}) (Authenticatable, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(id)
	}
	return nil, ErrUserNotFound
}

func (m *mockUserProvider) FindByCredentials(credentials map[string]interface{}) (Authenticatable, error) {
	if m.findByCredentialsFn != nil {
		return m.findByCredentialsFn(credentials)
	}
	return nil, ErrUserNotFound
}

func (m *mockUserProvider) ValidateCredentials(user Authenticatable, credentials map[string]interface{}) bool {
	if m.validateFn != nil {
		return m.validateFn(user, credentials)
	}
	return false
}

func (m *mockUserProvider) UpdateRememberToken(user Authenticatable, token string) error {
	if m.updateTokenFn != nil {
		return m.updateTokenFn(user, token)
	}
	return nil
}

func TestInit(t *testing.T) {
	tests := []struct {
		name         string
		config       Config
		wantErr      bool
		checkDefault string
	}{
		{
			name: "initializes with empty config",
			config: Config{
				DefaultGuard: "",
			},
			wantErr:      false,
			checkDefault: "web", // default value
		},
		{
			name: "initializes with custom default guard",
			config: Config{
				DefaultGuard: "api",
			},
			wantErr:      false,
			checkDefault: "api",
		},
		{
			name: "initializes with guards config",
			config: Config{
				DefaultGuard: "session",
				Guards: map[string]GuardConfig{
					"session": {
						Driver:   "session",
						Provider: "users",
					},
				},
			},
			wantErr:      false,
			checkDefault: "session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetGlobalManager()

			err := Init(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("Init() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				manager, err := GetManager()
				if err != nil {
					t.Errorf("GetManager() after Init should not error: %v", err)
					return
				}

				if manager.defaultGuard != tt.checkDefault {
					t.Errorf("defaultGuard = %v, want %v", manager.defaultGuard, tt.checkDefault)
				}
			}
		})
	}
}

func TestGetManager(t *testing.T) {
	tests := []struct {
		name      string
		setup     func()
		wantNil   bool
		wantErr   bool
		errTarget error
	}{
		{
			name: "returns error when not initialized",
			setup: func() {
				resetGlobalManager()
			},
			wantNil:   true,
			wantErr:   true,
			errTarget: ErrNotInitialized,
		},
		{
			name: "returns manager when initialized",
			setup: func() {
				resetGlobalManager()
				Init(Config{})
			},
			wantNil: false,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			got, err := GetManager()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetManager() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errTarget != nil {
				if !errors.Is(err, tt.errTarget) {
					t.Errorf("GetManager() error = %v, want %v", err, tt.errTarget)
				}
			}

			if (got == nil) != tt.wantNil {
				t.Errorf("GetManager() got nil = %v, wantNil %v", got == nil, tt.wantNil)
			}
		})
	}
}

func TestGetGuard(t *testing.T) {
	tests := []struct {
		name      string
		guardName string
		setup     func()
		wantNil   bool
		wantErr   bool
		errTarget error
	}{
		{
			name:      "returns error when manager not initialized",
			guardName: "web",
			setup: func() {
				resetGlobalManager()
			},
			wantNil:   true,
			wantErr:   true,
			errTarget: ErrNotInitialized,
		},
		{
			name:      "returns error when guard not found",
			guardName: "nonexistent",
			setup: func() {
				resetGlobalManager()
				Init(Config{})
			},
			wantNil:   true,
			wantErr:   true,
			errTarget: ErrGuardNotFound,
		},
		{
			name:      "returns guard when found",
			guardName: "web",
			setup: func() {
				resetGlobalManager()
				Init(Config{})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{})
			},
			wantNil: false,
			wantErr: false,
		},
		{
			name:      "returns default guard with empty name",
			guardName: "",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{})
			},
			wantNil: false,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			got, err := GetGuard(tt.guardName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetGuard() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errTarget != nil {
				if !errors.Is(err, tt.errTarget) {
					t.Errorf("GetGuard() error = %v, want %v", err, tt.errTarget)
				}
			}

			if (got == nil) != tt.wantNil {
				t.Errorf("GetGuard() got nil = %v, wantNil %v", got == nil, tt.wantNil)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		setup    func()
		wantBool bool
	}{
		{
			name: "returns false when manager not initialized",
			setup: func() {
				resetGlobalManager()
			},
			wantBool: false,
		},
		{
			name: "returns false when default guard not found",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "nonexistent"})
			},
			wantBool: false,
		},
		{
			name: "returns false when guard returns false",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					checkFn: func(r *http.Request) bool { return false },
				})
			},
			wantBool: false,
		},
		{
			name: "returns true when guard returns true",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					checkFn: func(r *http.Request) bool { return true },
				})
			},
			wantBool: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			req := httptest.NewRequest("GET", "/", nil)
			got := Check(req)

			if got != tt.wantBool {
				t.Errorf("Check() = %v, want %v", got, tt.wantBool)
			}
		})
	}
}

func TestUser(t *testing.T) {
	testUser := &AuthUser{ID: 1, Email: "test@example.com"}

	tests := []struct {
		name     string
		setup    func()
		wantNil  bool
		wantUser Authenticatable
	}{
		{
			name: "returns nil when manager not initialized",
			setup: func() {
				resetGlobalManager()
			},
			wantNil: true,
		},
		{
			name: "returns nil when default guard not found",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "nonexistent"})
			},
			wantNil: true,
		},
		{
			name: "returns nil when guard returns nil user",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					userFn: func(r *http.Request) Authenticatable { return nil },
				})
			},
			wantNil: true,
		},
		{
			name: "returns user when guard returns user",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					userFn: func(r *http.Request) Authenticatable { return testUser },
				})
			},
			wantNil:  false,
			wantUser: testUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			req := httptest.NewRequest("GET", "/", nil)
			got := User(req)

			if (got == nil) != tt.wantNil {
				t.Errorf("User() got nil = %v, wantNil %v", got == nil, tt.wantNil)
			}

			if !tt.wantNil && got != tt.wantUser {
				t.Errorf("User() = %v, want %v", got, tt.wantUser)
			}
		})
	}
}

func TestID(t *testing.T) {
	tests := []struct {
		name    string
		setup   func()
		wantNil bool
		wantID  interface{}
	}{
		{
			name: "returns nil when manager not initialized",
			setup: func() {
				resetGlobalManager()
			},
			wantNil: true,
		},
		{
			name: "returns nil when default guard not found",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "nonexistent"})
			},
			wantNil: true,
		},
		{
			name: "returns nil when guard returns nil ID",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					idFn: func(r *http.Request) interface{} { return nil },
				})
			},
			wantNil: true,
		},
		{
			name: "returns ID when guard returns ID",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					idFn: func(r *http.Request) interface{} { return 123 },
				})
			},
			wantNil: false,
			wantID:  123,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			req := httptest.NewRequest("GET", "/", nil)
			got := ID(req)

			if (got == nil) != tt.wantNil {
				t.Errorf("ID() got nil = %v, wantNil %v", got == nil, tt.wantNil)
			}

			if !tt.wantNil && got != tt.wantID {
				t.Errorf("ID() = %v, want %v", got, tt.wantID)
			}
		})
	}
}

func TestLogin(t *testing.T) {
	testUser := &AuthUser{ID: 1, Email: "test@example.com"}
	loginErr := errors.New("login failed")

	tests := []struct {
		name     string
		setup    func()
		user     Authenticatable
		remember []bool
		wantErr  bool
	}{
		{
			name: "returns error when manager not initialized",
			setup: func() {
				resetGlobalManager()
			},
			user:    testUser,
			wantErr: true,
		},
		{
			name: "returns error when default guard not found",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "nonexistent"})
			},
			user:    testUser,
			wantErr: true,
		},
		{
			name: "returns error when guard login fails",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					loginFn: func(w http.ResponseWriter, r *http.Request, user Authenticatable, remember ...bool) error {
						return loginErr
					},
				})
			},
			user:    testUser,
			wantErr: true,
		},
		{
			name: "succeeds when guard login succeeds",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					loginFn: func(w http.ResponseWriter, r *http.Request, user Authenticatable, remember ...bool) error {
						return nil
					},
				})
			},
			user:    testUser,
			wantErr: false,
		},
		{
			name: "passes remember parameter to guard",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					loginFn: func(w http.ResponseWriter, r *http.Request, user Authenticatable, remember ...bool) error {
						if len(remember) == 0 || !remember[0] {
							return errors.New("remember not passed")
						}
						return nil
					},
				})
			},
			user:     testUser,
			remember: []bool{true},
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			req := httptest.NewRequest("POST", "/login", nil)
			rec := httptest.NewRecorder()

			err := Login(rec, req, tt.user, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("Login() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoginByID(t *testing.T) {
	loginErr := errors.New("login failed")

	tests := []struct {
		name     string
		setup    func()
		id       interface{}
		remember []bool
		wantErr  bool
	}{
		{
			name: "returns error when manager not initialized",
			setup: func() {
				resetGlobalManager()
			},
			id:      1,
			wantErr: true,
		},
		{
			name: "returns error when default guard not found",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "nonexistent"})
			},
			id:      1,
			wantErr: true,
		},
		{
			name: "returns error when guard loginByID fails",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					loginByIDFn: func(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
						return loginErr
					},
				})
			},
			id:      1,
			wantErr: true,
		},
		{
			name: "succeeds when guard loginByID succeeds",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					loginByIDFn: func(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
						return nil
					},
				})
			},
			id:      1,
			wantErr: false,
		},
		{
			name: "passes ID to guard",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					loginByIDFn: func(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
						if id != 42 {
							return errors.New("wrong ID")
						}
						return nil
					},
				})
			},
			id:      42,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			req := httptest.NewRequest("POST", "/login", nil)
			rec := httptest.NewRecorder()

			err := LoginByID(rec, req, tt.id, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoginByID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAttempt(t *testing.T) {
	attemptErr := errors.New("attempt failed")

	tests := []struct {
		name        string
		setup       func()
		credentials map[string]interface{}
		remember    []bool
		wantSuccess bool
		wantErr     bool
	}{
		{
			name: "returns error when manager not initialized",
			setup: func() {
				resetGlobalManager()
			},
			credentials: map[string]interface{}{"email": "test@example.com"},
			wantSuccess: false,
			wantErr:     true,
		},
		{
			name: "returns error when default guard not found",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "nonexistent"})
			},
			credentials: map[string]interface{}{"email": "test@example.com"},
			wantSuccess: false,
			wantErr:     true,
		},
		{
			name: "returns error when guard attempt fails",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					attemptFn: func(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
						return false, attemptErr
					},
				})
			},
			credentials: map[string]interface{}{"email": "test@example.com"},
			wantSuccess: false,
			wantErr:     true,
		},
		{
			name: "returns false success when credentials invalid",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					attemptFn: func(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
						return false, nil
					},
				})
			},
			credentials: map[string]interface{}{"email": "wrong@example.com"},
			wantSuccess: false,
			wantErr:     false,
		},
		{
			name: "returns true success when credentials valid",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					attemptFn: func(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
						return true, nil
					},
				})
			},
			credentials: map[string]interface{}{"email": "test@example.com", "password": "secret"},
			wantSuccess: true,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			req := httptest.NewRequest("POST", "/login", nil)
			rec := httptest.NewRecorder()

			gotSuccess, err := Attempt(rec, req, tt.credentials, tt.remember...)
			if (err != nil) != tt.wantErr {
				t.Errorf("Attempt() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if gotSuccess != tt.wantSuccess {
				t.Errorf("Attempt() success = %v, want %v", gotSuccess, tt.wantSuccess)
			}
		})
	}
}

func TestLogout(t *testing.T) {
	logoutErr := errors.New("logout failed")

	tests := []struct {
		name    string
		setup   func()
		wantErr bool
	}{
		{
			name: "returns error when manager not initialized",
			setup: func() {
				resetGlobalManager()
			},
			wantErr: true,
		},
		{
			name: "returns error when default guard not found",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "nonexistent"})
			},
			wantErr: true,
		},
		{
			name: "returns error when guard logout fails",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					logoutFn: func(w http.ResponseWriter, r *http.Request) error {
						return logoutErr
					},
				})
			},
			wantErr: true,
		},
		{
			name: "succeeds when guard logout succeeds",
			setup: func() {
				resetGlobalManager()
				Init(Config{DefaultGuard: "web"})
				manager, _ := GetManager()
				manager.RegisterGuard("web", &mockGuard{
					logoutFn: func(w http.ResponseWriter, r *http.Request) error {
						return nil
					},
				})
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			req := httptest.NewRequest("POST", "/logout", nil)
			rec := httptest.NewRecorder()

			err := Logout(rec, req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Logout() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestManagerRegisterGuard(t *testing.T) {
	tests := []struct {
		name      string
		guardName string
		guard     Guard
	}{
		{
			name:      "registers guard with name",
			guardName: "web",
			guard:     &mockGuard{},
		},
		{
			name:      "registers multiple guards",
			guardName: "api",
			guard:     &mockGuard{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager()
			manager.RegisterGuard(tt.guardName, tt.guard)

			got, err := manager.Guard(tt.guardName)
			if err != nil {
				t.Errorf("Guard() error = %v after RegisterGuard", err)
				return
			}

			if got != tt.guard {
				t.Errorf("Guard() = %v, want %v", got, tt.guard)
			}
		})
	}
}

func TestManagerRegisterProvider(t *testing.T) {
	tests := []struct {
		name         string
		providerName string
		provider     UserProvider
	}{
		{
			name:         "registers provider with name",
			providerName: "users",
			provider:     &mockUserProvider{},
		},
		{
			name:         "registers multiple providers",
			providerName: "admins",
			provider:     &mockUserProvider{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager()
			manager.RegisterProvider(tt.providerName, tt.provider)

			got, err := manager.Provider(tt.providerName)
			if err != nil {
				t.Errorf("Provider() error = %v after RegisterProvider", err)
				return
			}

			if got != tt.provider {
				t.Errorf("Provider() = %v, want %v", got, tt.provider)
			}
		})
	}
}

func TestManagerGuard(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(*Manager)
		guardName    string
		wantErr      bool
		wantDefault  bool
	}{
		{
			name: "returns error for nonexistent guard",
			setup: func(m *Manager) {
				// No guards registered
			},
			guardName: "nonexistent",
			wantErr:   true,
		},
		{
			name: "returns default guard with empty name",
			setup: func(m *Manager) {
				m.RegisterGuard("web", &mockGuard{})
			},
			guardName:   "",
			wantErr:     false,
			wantDefault: true,
		},
		{
			name: "returns named guard",
			setup: func(m *Manager) {
				m.RegisterGuard("api", &mockGuard{})
			},
			guardName: "api",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager()
			tt.setup(manager)

			got, err := manager.Guard(tt.guardName)
			if (err != nil) != tt.wantErr {
				t.Errorf("Guard() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && got == nil {
				t.Errorf("Guard() returned nil guard without error")
			}
		})
	}
}

func TestManagerDefaultGuard(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*Manager)
		wantErr bool
	}{
		{
			name: "returns error when default guard not registered",
			setup: func(m *Manager) {
				// No guards registered
			},
			wantErr: true,
		},
		{
			name: "returns default guard when registered",
			setup: func(m *Manager) {
				m.RegisterGuard("web", &mockGuard{})
			},
			wantErr: false,
		},
		{
			name: "returns custom default guard when set",
			setup: func(m *Manager) {
				m.SetDefaultGuard("api")
				m.RegisterGuard("api", &mockGuard{})
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager()
			tt.setup(manager)

			got, err := manager.DefaultGuard()
			if (err != nil) != tt.wantErr {
				t.Errorf("DefaultGuard() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && got == nil {
				t.Errorf("DefaultGuard() returned nil guard without error")
			}
		})
	}
}

func TestManagerConcurrency(t *testing.T) {
	manager := NewManager()

	var wg sync.WaitGroup
	numGoroutines := 100

	// Concurrent guard registration
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			guardName := "guard_" + string(rune('a'+idx%26))
			manager.RegisterGuard(guardName, &mockGuard{})
		}(i)
	}

	// Concurrent guard retrieval
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			guardName := "guard_" + string(rune('a'+idx%26))
			_, _ = manager.Guard(guardName)
		}(i)
	}

	// Concurrent provider registration
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			providerName := "provider_" + string(rune('a'+idx%26))
			manager.RegisterProvider(providerName, &mockUserProvider{})
		}(i)
	}

	wg.Wait()
}

func TestNewManager(t *testing.T) {
	manager := NewManager()

	if manager == nil {
		t.Fatal("NewManager() returned nil")
	}

	if manager.guards == nil {
		t.Error("guards map should be initialized")
	}

	if manager.providers == nil {
		t.Error("providers map should be initialized")
	}

	if manager.defaultGuard != "web" {
		t.Errorf("defaultGuard = %v, want 'web'", manager.defaultGuard)
	}
}

func TestManagerSetDefaultGuard(t *testing.T) {
	tests := []struct {
		name         string
		defaultGuard string
	}{
		{
			name:         "sets default guard to api",
			defaultGuard: "api",
		},
		{
			name:         "sets default guard to session",
			defaultGuard: "session",
		},
		{
			name:         "sets default guard to empty string",
			defaultGuard: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager()
			manager.SetDefaultGuard(tt.defaultGuard)

			if manager.defaultGuard != tt.defaultGuard {
				t.Errorf("defaultGuard = %v, want %v", manager.defaultGuard, tt.defaultGuard)
			}
		})
	}
}
