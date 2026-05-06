package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/bcrypt"
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

// Logger is the minimal logging interface the auth package uses for
// operational events (authentication failures, authorization denials,
// bcrypt cost clamping). The framework's log.Logger satisfies this
// interface; keeping the contract local avoids importing log/ and
// preserves auth's leaf status for log-adjacent packages.
type Logger interface {
	Info(msg string, kvs ...any)
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
}

// Manager manages multiple authentication guards
type Manager struct {
	guards       map[string]Guard
	providers    map[string]UserProvider
	defaultGuard string
	hasher       Hasher
	gate         *Gate

	// logger is stored atomically so middleware request paths can read
	// the current logger without contending with the RWMutex protecting
	// the guard/provider maps.
	logger atomic.Value // holds authLoggerHolder{Logger}

	// serverSessions holds an optional server-side session store used by
	// administrative operations (RevokeSession, RevokeAllSessions,
	// ListActiveSessions). Nil disables those operations.
	serverSessions ServerSessionStore

	mu sync.RWMutex
}

// authLoggerHolder wraps a Logger so atomic.Value stores a single type.
type authLoggerHolder struct{ Logger }

// NewManager creates a new auth manager
func NewManager() *Manager {
	return &Manager{
		guards:       make(map[string]Guard),
		providers:    make(map[string]UserProvider),
		defaultGuard: "web",
		gate:         NewGate(),
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

// Gate returns the authorization gate.
func (m *Manager) Gate() *Gate {
	return m.gate
}

// GateAllows checks if the authenticated user (from the default guard) is
// allowed to perform the given ability. Returns false when there is no
// authenticated user.
func (m *Manager) GateAllows(r *http.Request, ability string, args ...interface{}) bool {
	user := m.User(r)
	if user == nil {
		return false
	}
	return m.gate.Allows(user, ability, args...)
}

// GateAuthorize checks if the authenticated user (from the default guard) is
// allowed to perform the given ability. Returns ErrUnauthorized on denial or
// when there is no authenticated user.
func (m *Manager) GateAuthorize(r *http.Request, ability string, args ...interface{}) error {
	if !m.GateAllows(r, ability, args...) {
		return ErrUnauthorized
	}
	return nil
}

// Hash hashes a password using the manager's hasher.
func (m *Manager) Hash(password string) (string, error) {
	return m.GetHasher().Hash(password)
}

// Verify verifies a password against a hash using the manager's hasher.
func (m *Manager) Verify(password string, hash string) bool {
	return m.GetHasher().Verify(password, hash)
}

// SetHasher sets the hasher on the manager. When a logger has already been
// installed via SetLogger and the hasher is a *BcryptHasher, the logger is
// propagated so hasher warnings surface through the framework logger.
func (m *Manager) SetHasher(h Hasher) {
	m.mu.Lock()
	m.hasher = h
	m.mu.Unlock()

	if logger := m.log(); logger != nil {
		if bh, ok := h.(*BcryptHasher); ok {
			bh.SetLogger(logger)
		}
	}
}

// SetLogger installs a logger for auth operational events (authentication
// required denials, authorization rejections, hasher configuration warnings).
// Nil disables logging. Safe to call concurrently.
func (m *Manager) SetLogger(l Logger) {
	m.logger.Store(authLoggerHolder{Logger: l})

	m.mu.RLock()
	hasher := m.hasher
	m.mu.RUnlock()

	if bh, ok := hasher.(*BcryptHasher); ok {
		bh.SetLogger(l)
	}
}

// log returns the installed logger, or nil when SetLogger has not been called.
func (m *Manager) log() Logger {
	v := m.logger.Load()
	if v == nil {
		return nil
	}
	return v.(authLoggerHolder).Logger
}

// logWarn emits a warn event when a logger is configured.
func (m *Manager) logWarn(msg string, kvs ...any) {
	if l := m.log(); l != nil {
		l.Warn(msg, kvs...)
	}
}

// GetHasher returns the manager's hasher, falling back to a default bcrypt hasher.
func (m *Manager) GetHasher() Hasher {
	m.mu.RLock()
	h := m.hasher
	m.mu.RUnlock()
	if h != nil {
		return h
	}
	return NewBcryptHasher(bcrypt.DefaultCost)
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
	Driver string
	Model  string
}

// SetServerSessionStore installs a server-side session store. Pass nil to
// remove a previously installed store. Safe for concurrent use.
func (m *Manager) SetServerSessionStore(store ServerSessionStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serverSessions = store
}

// ServerSessionStore returns the installed server-side session store, or
// nil when none has been configured.
func (m *Manager) ServerSessionStore() ServerSessionStore {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.serverSessions
}

// RevokeSession deletes a single server-side session by id. Returns
// ErrNoServerSessionStore when no store has been configured.
func (m *Manager) RevokeSession(ctx context.Context, sessionID string) error {
	store := m.ServerSessionStore()
	if store == nil {
		return ErrNoServerSessionStore
	}
	return store.Delete(ctx, sessionID)
}

// RevokeAllSessions deletes every server-side session belonging to
// userID. Returns ErrNoServerSessionStore when no store has been
// configured.
func (m *Manager) RevokeAllSessions(ctx context.Context, userID string) error {
	store := m.ServerSessionStore()
	if store == nil {
		return ErrNoServerSessionStore
	}
	return store.DeleteAllForUser(ctx, userID)
}

// ListActiveSessions returns metadata for every non-expired server-side
// session belonging to userID. Returns ErrNoServerSessionStore when no
// store has been configured.
func (m *Manager) ListActiveSessions(ctx context.Context, userID string) ([]*SessionMeta, error) {
	store := m.ServerSessionStore()
	if store == nil {
		return nil, ErrNoServerSessionStore
	}
	return store.ListForUser(ctx, userID)
}
