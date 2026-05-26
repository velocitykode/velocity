package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
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

	// ErrRememberClearPartial is returned (wrapped, with errors.Join'd
	// causes) by Manager.RevokeAllSessions when the server-side session
	// deletion succeeded but one or more guards' RememberTokenClearer
	// implementations failed. The load-bearing security action (revoking
	// active sessions) has succeeded; callers can decide whether to retry
	// the clear, surface a degraded status to admins, or ignore.
	ErrRememberClearPartial = errors.New("velocity/auth: remember token clear partially failed")
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

// SessionAware is an optional capability interface implemented by guards
// that back authentication with a request-scoped Session. Guards that do
// not have a session (e.g. JWT/bearer-token) leave this unimplemented;
// Manager.Session returns nil for those.
type SessionAware interface {
	// Session returns the Session attached to this request, loading from
	// the cookie store on first call and caching in the request context
	// for subsequent calls. Returns nil when no session is available
	// (no cookie, decode error, or the guard does not maintain sessions).
	Session(r *http.Request) Session
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

	// trustedProxies is the parsed proxy-network list propagated to
	// every guard so login throttling, audit-trail IP capture, and
	// per-IP rate limits all agree on "who is the real client?".
	// Set via SetTrustedProxies (typically at boot from Config.TrustedProxies).
	// Nil means "no proxies trusted" (forwarded headers are ignored).
	trustedProxies []*net.IPNet

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

// RegisterGuard registers an authentication guard. If a server-side
// session store is already installed and the guard implements
// ServerSessionStoreReceiver, the store is propagated immediately so
// registration order does not matter. The same applies to the
// trusted-proxies list and TrustedProxiesReceiver.
func (m *Manager) RegisterGuard(name string, guard Guard) {
	m.mu.Lock()
	m.guards[name] = guard
	store := m.serverSessions
	proxies := m.trustedProxies
	m.mu.Unlock()

	if store != nil {
		if r, ok := guard.(ServerSessionStoreReceiver); ok {
			r.SetServerSessionStore(store)
		}
	}
	if len(proxies) > 0 {
		if r, ok := guard.(TrustedProxiesReceiver); ok {
			r.SetTrustedProxies(proxies)
		}
	}
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

// Session returns the Session attached to the request via the default
// guard, or nil when the guard does not implement SessionAware or no
// session is available. Handlers use this to set flash messages or
// read/write session data without reaching into a specific guard impl.
func (m *Manager) Session(r *http.Request) Session {
	guard, err := m.DefaultGuard()
	if err != nil {
		return nil
	}
	sa, ok := guard.(SessionAware)
	if !ok {
		return nil
	}
	return sa.Session(r)
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

	// TrustedProxies is the list of IP/CIDR strings whose forwarded
	// headers (Forwarded, X-Forwarded-For, X-Real-IP) may be honoured
	// when deriving the client IP for the login throttler and the
	// session audit trail. Empty means "no proxies trusted" (the
	// secure default; XFF spoofing is fully ignored). Configure this
	// at boot to match your load balancer / reverse proxy topology.
	//
	// Entries are parsed via internal/clientip.ParseCIDRs and
	// propagated to every guard via Manager.SetTrustedProxies.
	TrustedProxies []string
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
//
// Every registered guard that implements ServerSessionStoreReceiver is
// notified so it can consult the store on Login/Check/Logout. Guards that
// do not implement the interface (e.g. JWT) are silently skipped.
func (m *Manager) SetServerSessionStore(store ServerSessionStore) {
	m.mu.Lock()
	m.serverSessions = store
	receivers := make([]ServerSessionStoreReceiver, 0, len(m.guards))
	for _, g := range m.guards {
		if r, ok := g.(ServerSessionStoreReceiver); ok {
			receivers = append(receivers, r)
		}
	}
	m.mu.Unlock()

	for _, r := range receivers {
		r.SetServerSessionStore(store)
	}
}

// TrustedProxiesReceiver is an optional capability interface implemented
// by guards that derive a client IP for throttling or audit logging.
// Manager.SetTrustedProxies propagates the parsed proxy network list to
// every registered guard that satisfies this interface so the throttle
// key, the session audit trail, and per-IP limiters all agree on
// "who is the real client?".
//
// Guards that do not maintain a client-IP-sensitive surface (e.g. a
// pure bearer-token guard) leave this unimplemented; Manager silently
// skips them.
type TrustedProxiesReceiver interface {
	SetTrustedProxies(proxies []*net.IPNet)
}

// SetTrustedProxies installs the parsed proxy network list used for
// client-IP resolution across the auth package. Pass nil to clear a
// previously installed list (reverts to "trust nothing"). Safe for
// concurrent use.
//
// Every registered guard implementing TrustedProxiesReceiver is
// notified immediately; guards registered later inherit the list at
// registration time (see RegisterGuard).
//
// At boot the framework parses Config.TrustedProxies via
// internal/clientip.ParseCIDRs and calls this with the result, so app
// code does not normally need to touch it.
func (m *Manager) SetTrustedProxies(proxies []*net.IPNet) {
	m.mu.Lock()
	// Defensive copy so callers cannot mutate the manager's view by
	// editing the slice they handed in.
	if len(proxies) > 0 {
		m.trustedProxies = append([]*net.IPNet(nil), proxies...)
	} else {
		m.trustedProxies = nil
	}
	receivers := make([]TrustedProxiesReceiver, 0, len(m.guards))
	for _, g := range m.guards {
		if r, ok := g.(TrustedProxiesReceiver); ok {
			receivers = append(receivers, r)
		}
	}
	snapshot := m.trustedProxies
	m.mu.Unlock()

	for _, r := range receivers {
		r.SetTrustedProxies(snapshot)
	}
}

// TrustedProxies returns the parsed proxy network list installed via
// SetTrustedProxies. The returned slice is a snapshot; callers may
// read from it freely but must not mutate it.
func (m *Manager) TrustedProxies() []*net.IPNet {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.trustedProxies) == 0 {
		return nil
	}
	out := make([]*net.IPNet, len(m.trustedProxies))
	copy(out, m.trustedProxies)
	return out
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
//
// Caveat: this does NOT clear the user's remember-me token. The revoked
// browser's session cookie is dead, but if it also holds a remember
// cookie that cookie can resurrect a fresh session on the next request.
// This is intentional: remember tokens are per-user, so wiping one would
// also log the user out on every other device. To prevent resurrection
// across devices, call RevokeAllSessions instead. (Per-device remember
// tokens are out of scope for 0.x.)
func (m *Manager) RevokeSession(ctx context.Context, sessionID string) error {
	store := m.ServerSessionStore()
	if store == nil {
		return ErrNoServerSessionStore
	}
	return store.Delete(ctx, sessionID)
}

// RevokeAllSessions deletes every server-side session belonging to
// userID and clears the user's remember-me token on every registered
// guard that implements RememberTokenClearer. Returns
// ErrNoServerSessionStore when no store has been configured.
//
// Remember-token clearing is best-effort: failures are logged but do not
// undo the store-side session deletion, since the load-bearing security
// action (revoking active sessions) has already succeeded.
func (m *Manager) RevokeAllSessions(ctx context.Context, userID string) error {
	store := m.ServerSessionStore()
	if store == nil {
		return ErrNoServerSessionStore
	}
	if err := store.DeleteAllForUser(ctx, userID); err != nil {
		return err
	}

	m.mu.RLock()
	names := make([]string, 0, len(m.guards))
	clearers := make([]RememberTokenClearer, 0, len(m.guards))
	for name, g := range m.guards {
		if c, ok := g.(RememberTokenClearer); ok {
			names = append(names, name)
			clearers = append(clearers, c)
		}
	}
	m.mu.RUnlock()

	var clearerErrs []error
	for i, c := range clearers {
		if err := c.ClearRememberTokensForUser(ctx, userID); err != nil {
			m.logWarn("velocity/auth: clear remember token failed", "guard", names[i], "user_id", userID, "error", err)
			clearerErrs = append(clearerErrs, fmt.Errorf("guard %q: %w", names[i], err))
		}
	}
	if len(clearerErrs) > 0 {
		return fmt.Errorf("%w: %w", ErrRememberClearPartial, errors.Join(clearerErrs...))
	}
	return nil
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
