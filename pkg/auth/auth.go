package auth

import (
	"errors"
	"net/http"
	"sync"
)

var (
	// Global auth manager instance
	globalManager *Manager
	globalMux     sync.RWMutex
	initOnce      sync.Once
)

// Errors
var (
	ErrNotAuthenticated   = errors.New("not authenticated")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUserNotFound       = errors.New("user not found")
	ErrGuardNotFound      = errors.New("guard not found")
	ErrNotInitialized     = errors.New("auth manager not initialized")
	ErrInvalidSession     = errors.New("invalid session")
)

// Authenticatable represents a user that can be authenticated
type Authenticatable interface {
	GetAuthIdentifier() interface{}
	GetAuthPassword() string
	GetRememberToken() string
	SetRememberToken(token string)
}

// UserProvider handles user retrieval and validation
type UserProvider interface {
	// Retrieve user by ID
	FindByID(id interface{}) (Authenticatable, error)

	// Retrieve user by credentials
	FindByCredentials(credentials map[string]interface{}) (Authenticatable, error)

	// Validate user credentials
	ValidateCredentials(user Authenticatable, credentials map[string]interface{}) bool

	// Update remember token
	UpdateRememberToken(user Authenticatable, token string) error
}

// Guard defines authentication guard interface
type Guard interface {
	// Check if user is authenticated
	Check(r *http.Request) bool

	// Get authenticated user
	User(r *http.Request) Authenticatable

	// Get user ID
	ID(r *http.Request) interface{}

	// Login user
	Login(w http.ResponseWriter, r *http.Request, user Authenticatable, remember ...bool) error

	// Login by user ID
	LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error

	// Attempt login with credentials
	Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error)

	// Logout user
	Logout(w http.ResponseWriter, r *http.Request) error

	// Set user provider
	SetProvider(provider UserProvider)
}

// Manager manages multiple authentication guards
type Manager struct {
	guards       map[string]Guard
	providers    map[string]UserProvider
	defaultGuard string
	mu           sync.RWMutex
}

// NewManager creates a new auth manager
func NewManager() *Manager {
	return &Manager{
		guards:       make(map[string]Guard),
		providers:    make(map[string]UserProvider),
		defaultGuard: "web",
	}
}

// RegisterGuard registers an authentication guard
func (m *Manager) RegisterGuard(name string, guard Guard) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.guards[name] = guard
}

// RegisterProvider registers a user provider
func (m *Manager) RegisterProvider(name string, provider UserProvider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers[name] = provider
}

// SetDefaultGuard sets the default guard
func (m *Manager) SetDefaultGuard(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultGuard = name
}

// Guard returns a guard by name
func (m *Manager) Guard(name string) (Guard, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if name == "" {
		name = m.defaultGuard
	}

	guard, ok := m.guards[name]
	if !ok {
		return nil, ErrGuardNotFound
	}

	return guard, nil
}

// DefaultGuard returns the default guard
func (m *Manager) DefaultGuard() (Guard, error) {
	return m.Guard("")
}

// Provider returns a provider by name
func (m *Manager) Provider(name string) (UserProvider, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	provider, ok := m.providers[name]
	if !ok {
		return nil, errors.New("provider not found")
	}

	return provider, nil
}

// Init initializes the global auth manager
func Init(config Config) error {
	globalMux.Lock()
	defer globalMux.Unlock()

	manager := NewManager()

	// Set default guard
	if config.DefaultGuard != "" {
		manager.SetDefaultGuard(config.DefaultGuard)
	}

	// Initialize providers and guards based on config
	// This will be expanded with actual implementations

	globalManager = manager
	return nil
}

// GetManager returns the global auth manager
func GetManager() (*Manager, error) {
	globalMux.RLock()
	defer globalMux.RUnlock()

	if globalManager == nil {
		return nil, ErrNotInitialized
	}

	return globalManager, nil
}

// GetGuard returns a guard by name from global manager
func GetGuard(name string) (Guard, error) {
	manager, err := GetManager()
	if err != nil {
		return nil, err
	}

	return manager.Guard(name)
}

// Check if user is authenticated using default guard
func Check(r *http.Request) bool {
	manager, err := GetManager()
	if err != nil {
		return false
	}

	guard, err := manager.DefaultGuard()
	if err != nil {
		return false
	}

	return guard.Check(r)
}

// User returns authenticated user using default guard
func User(r *http.Request) Authenticatable {
	manager, err := GetManager()
	if err != nil {
		return nil
	}

	guard, err := manager.DefaultGuard()
	if err != nil {
		return nil
	}

	return guard.User(r)
}

// ID returns authenticated user ID using default guard
func ID(r *http.Request) interface{} {
	manager, err := GetManager()
	if err != nil {
		return nil
	}

	guard, err := manager.DefaultGuard()
	if err != nil {
		return nil
	}

	return guard.ID(r)
}

// Login logs in a user using default guard
func Login(w http.ResponseWriter, r *http.Request, user Authenticatable, remember ...bool) error {
	manager, err := GetManager()
	if err != nil {
		return err
	}

	guard, err := manager.DefaultGuard()
	if err != nil {
		return err
	}

	return guard.Login(w, r, user, remember...)
}

// LoginByID logs in a user by ID using default guard
func LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
	manager, err := GetManager()
	if err != nil {
		return err
	}

	guard, err := manager.DefaultGuard()
	if err != nil {
		return err
	}

	return guard.LoginByID(w, r, id, remember...)
}

// Attempt login with credentials using default guard
func Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	manager, err := GetManager()
	if err != nil {
		return false, err
	}

	guard, err := manager.DefaultGuard()
	if err != nil {
		return false, err
	}

	return guard.Attempt(w, r, credentials, remember...)
}

// Logout logs out user using default guard
func Logout(w http.ResponseWriter, r *http.Request) error {
	manager, err := GetManager()
	if err != nil {
		return err
	}

	guard, err := manager.DefaultGuard()
	if err != nil {
		return err
	}

	return guard.Logout(w, r)
}

// Config holds authentication configuration
type Config struct {
	DefaultGuard string
	Guards       map[string]GuardConfig
	Providers    map[string]ProviderConfig
}

// GuardConfig holds guard configuration
type GuardConfig struct {
	Driver   string
	Provider string
	Options  map[string]interface{}
}

// ProviderConfig holds provider configuration
type ProviderConfig struct {
	Driver  string
	Model   string
	Options map[string]interface{}
}
