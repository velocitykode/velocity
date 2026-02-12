package auth

import (
	"errors"
	"net/http"
	"sync"
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
	hasher       Hasher
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

// Check returns true if the request is authenticated using the default guard.
func (m *Manager) Check(r *http.Request) bool {
	guard, err := m.DefaultGuard()
	if err != nil {
		return false
	}
	return guard.Check(r)
}

// User returns the authenticated user using the default guard.
func (m *Manager) User(r *http.Request) Authenticatable {
	guard, err := m.DefaultGuard()
	if err != nil {
		return nil
	}
	return guard.User(r)
}

// ID returns the authenticated user ID using the default guard.
func (m *Manager) ID(r *http.Request) interface{} {
	guard, err := m.DefaultGuard()
	if err != nil {
		return nil
	}
	return guard.ID(r)
}

// Login logs in a user using the default guard.
func (m *Manager) Login(w http.ResponseWriter, r *http.Request, user Authenticatable, remember ...bool) error {
	guard, err := m.DefaultGuard()
	if err != nil {
		return err
	}
	return guard.Login(w, r, user, remember...)
}

// Attempt attempts login with credentials using the default guard.
func (m *Manager) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	guard, err := m.DefaultGuard()
	if err != nil {
		return false, err
	}
	return guard.Attempt(w, r, credentials, remember...)
}

// Logout logs out the user using the default guard.
func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) error {
	guard, err := m.DefaultGuard()
	if err != nil {
		return err
	}
	return guard.Logout(w, r)
}

// Hash hashes a password using the manager's hasher.
func (m *Manager) Hash(password string) (string, error) {
	return m.GetHasher().Hash(password)
}

// Verify verifies a password against a hash using the manager's hasher.
func (m *Manager) Verify(password string, hash string) bool {
	return m.GetHasher().Verify(password, hash)
}

// SetHasher sets the hasher on the manager.
func (m *Manager) SetHasher(h Hasher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hasher = h
}

// GetHasher returns the manager's hasher, falling back to a default bcrypt hasher.
func (m *Manager) GetHasher() Hasher {
	m.mu.RLock()
	h := m.hasher
	m.mu.RUnlock()
	if h != nil {
		return h
	}
	return GetHasher()
}

// NewManagerFromConfig creates a new Manager configured from the provided Config.
func NewManagerFromConfig(config Config) (*Manager, error) {
	manager := NewManager()

	if config.DefaultGuard != "" {
		manager.SetDefaultGuard(config.DefaultGuard)
	}

	if config.BcryptCost > 0 {
		manager.SetHasher(NewBcryptHasher(config.BcryptCost))
	}

	return manager, nil
}

// Config holds authentication configuration
type Config struct {
	DefaultGuard string
	Guards       map[string]GuardConfig
	Providers    map[string]ProviderConfig
	BcryptCost   int // Bcrypt cost for password hashing. 0 uses the default.
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
