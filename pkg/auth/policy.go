package auth

import (
	"errors"
	"net/http"
	"sync"
)

// Authorization errors
var (
	ErrUnauthorized     = errors.New("unauthorized action")
	ErrPolicyNotFound   = errors.New("policy not found")
	ErrGateNotFound     = errors.New("gate not found")
	ErrNoUserInContext  = errors.New("no authenticated user in context")
	ErrInvalidResource  = errors.New("invalid resource type")
)

// GateCallback is a function that determines if a user can perform an action
type GateCallback func(user Authenticatable, args ...interface{}) bool

// Policy defines authorization logic for a specific resource type
type Policy interface {
	// Authorize checks if user can perform action on the resource
	Authorize(user Authenticatable, action string, resource interface{}) bool
}

// PolicyFunc is a function adapter for simple policies
type PolicyFunc func(user Authenticatable, action string, resource interface{}) bool

// Authorize implements Policy interface
func (f PolicyFunc) Authorize(user Authenticatable, action string, resource interface{}) bool {
	return f(user, action, resource)
}

// BeforeCallback is called before any gate/policy check
// Return true to allow, false to deny, nil to continue to the actual check
type BeforeCallback func(user Authenticatable, ability string, args ...interface{}) *bool

// AfterCallback is called after any gate/policy check
type AfterCallback func(user Authenticatable, ability string, result bool, args ...interface{}) bool

// RoleChecker is a function that checks if a user has a role
type RoleChecker func(user Authenticatable, role string) bool

// Gate manages authorization gates and policies
type Gate struct {
	gates       map[string]GateCallback
	policies    map[string]Policy
	before      []BeforeCallback
	after       []AfterCallback
	roleChecker RoleChecker
	mu          sync.RWMutex
}

// NewGate creates a new Gate instance
func NewGate() *Gate {
	return &Gate{
		gates:    make(map[string]GateCallback),
		policies: make(map[string]Policy),
		before:   make([]BeforeCallback, 0),
		after:    make([]AfterCallback, 0),
	}
}

// Define registers a gate callback for an ability
func (g *Gate) Define(ability string, callback GateCallback) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gates[ability] = callback
}

// Policy registers a policy for a resource type
func (g *Gate) RegisterPolicy(resourceType string, policy Policy) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.policies[resourceType] = policy
}

// Before registers a callback to run before authorization checks
func (g *Gate) Before(callback BeforeCallback) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.before = append(g.before, callback)
}

// After registers a callback to run after authorization checks
func (g *Gate) After(callback AfterCallback) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.after = append(g.after, callback)
}

// SetRoleChecker sets the function used to check user roles
func (g *Gate) SetRoleChecker(checker RoleChecker) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.roleChecker = checker
}

// Allows checks if a user is allowed to perform an ability
func (g *Gate) Allows(user Authenticatable, ability string, args ...interface{}) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Run before callbacks
	for _, before := range g.before {
		if result := before(user, ability, args...); result != nil {
			return g.runAfterCallbacks(user, ability, *result, args...)
		}
	}

	// Check gate
	if callback, ok := g.gates[ability]; ok {
		result := callback(user, args...)
		return g.runAfterCallbacks(user, ability, result, args...)
	}

	return g.runAfterCallbacks(user, ability, false, args...)
}

// Denies checks if a user is denied from performing an ability
func (g *Gate) Denies(user Authenticatable, ability string, args ...interface{}) bool {
	return !g.Allows(user, ability, args...)
}

// Check checks multiple abilities (all must pass)
func (g *Gate) Check(user Authenticatable, abilities []string, args ...interface{}) bool {
	for _, ability := range abilities {
		if !g.Allows(user, ability, args...) {
			return false
		}
	}
	return true
}

// Any checks if any of the abilities pass
func (g *Gate) Any(user Authenticatable, abilities []string, args ...interface{}) bool {
	for _, ability := range abilities {
		if g.Allows(user, ability, args...) {
			return true
		}
	}
	return false
}

// AuthorizePolicy checks authorization using a registered policy
func (g *Gate) AuthorizePolicy(user Authenticatable, resourceType, action string, resource interface{}) bool {
	g.mu.RLock()
	policy, ok := g.policies[resourceType]
	g.mu.RUnlock()

	if !ok {
		return false
	}

	// Run before callbacks
	g.mu.RLock()
	for _, before := range g.before {
		if result := before(user, action, resource); result != nil {
			g.mu.RUnlock()
			return g.runAfterCallbacks(user, action, *result, resource)
		}
	}
	g.mu.RUnlock()

	result := policy.Authorize(user, action, resource)
	return g.runAfterCallbacks(user, action, result, resource)
}

// HasRole checks if a user has a specific role
func (g *Gate) HasRole(user Authenticatable, role string) bool {
	g.mu.RLock()
	checker := g.roleChecker
	g.mu.RUnlock()

	if checker == nil {
		return false
	}
	return checker(user, role)
}

// HasAnyRole checks if a user has any of the given roles
func (g *Gate) HasAnyRole(user Authenticatable, roles ...string) bool {
	for _, role := range roles {
		if g.HasRole(user, role) {
			return true
		}
	}
	return false
}

// HasAllRoles checks if a user has all the given roles
func (g *Gate) HasAllRoles(user Authenticatable, roles ...string) bool {
	for _, role := range roles {
		if !g.HasRole(user, role) {
			return false
		}
	}
	return true
}

// runAfterCallbacks runs after callbacks and returns the final result
func (g *Gate) runAfterCallbacks(user Authenticatable, ability string, result bool, args ...interface{}) bool {
	g.mu.RLock()
	afterCallbacks := g.after
	g.mu.RUnlock()

	for _, after := range afterCallbacks {
		result = after(user, ability, result, args...)
	}
	return result
}

// ForUser creates a user-scoped authorization checker
func (g *Gate) ForUser(user Authenticatable) *UserGate {
	return &UserGate{
		gate: g,
		user: user,
	}
}

// UserGate provides authorization methods for a specific user
type UserGate struct {
	gate *Gate
	user Authenticatable
}

// Allows checks if the user is allowed to perform an ability
func (ug *UserGate) Allows(ability string, args ...interface{}) bool {
	return ug.gate.Allows(ug.user, ability, args...)
}

// Denies checks if the user is denied from performing an ability
func (ug *UserGate) Denies(ability string, args ...interface{}) bool {
	return ug.gate.Denies(ug.user, ability, args...)
}

// Can is an alias for Allows
func (ug *UserGate) Can(ability string, args ...interface{}) bool {
	return ug.Allows(ability, args...)
}

// Cannot is an alias for Denies
func (ug *UserGate) Cannot(ability string, args ...interface{}) bool {
	return ug.Denies(ability, args...)
}

// Authorize checks authorization and returns an error if denied
func (ug *UserGate) Authorize(ability string, args ...interface{}) error {
	if !ug.Allows(ability, args...) {
		return ErrUnauthorized
	}
	return nil
}

// Global gate instance
var (
	globalGate     *Gate
	globalGateOnce sync.Once
	globalGateMux  sync.RWMutex
)

// GetGate returns the global gate instance
func GetGate() *Gate {
	globalGateMux.RLock()
	g := globalGate
	globalGateMux.RUnlock()

	if g != nil {
		return g
	}

	globalGateOnce.Do(func() {
		globalGateMux.Lock()
		globalGate = NewGate()
		globalGateMux.Unlock()
	})

	globalGateMux.RLock()
	defer globalGateMux.RUnlock()
	return globalGate
}

// SetGate sets the global gate instance (useful for testing)
func SetGate(g *Gate) {
	globalGateMux.Lock()
	defer globalGateMux.Unlock()
	globalGate = g
}

// Define registers a gate callback on the global gate
func Define(ability string, callback GateCallback) {
	GetGate().Define(ability, callback)
}

// RegisterPolicy registers a policy on the global gate
func RegisterPolicy(resourceType string, policy Policy) {
	GetGate().RegisterPolicy(resourceType, policy)
}

// BeforeCheck registers a before callback on the global gate
func BeforeCheck(callback BeforeCallback) {
	GetGate().Before(callback)
}

// AfterCheck registers an after callback on the global gate
func AfterCheck(callback AfterCallback) {
	GetGate().After(callback)
}

// SetGlobalRoleChecker sets the role checker on the global gate
func SetGlobalRoleChecker(checker RoleChecker) {
	GetGate().SetRoleChecker(checker)
}

// Can checks if the authenticated user can perform an ability
func Can(r *http.Request, ability string, args ...interface{}) bool {
	user := User(r)
	if user == nil {
		return false
	}
	return GetGate().Allows(user, ability, args...)
}

// Cannot checks if the authenticated user cannot perform an ability
func Cannot(r *http.Request, ability string, args ...interface{}) bool {
	return !Can(r, ability, args...)
}

// Authorize checks authorization and returns an error if denied
func Authorize(r *http.Request, ability string, args ...interface{}) error {
	if !Can(r, ability, args...) {
		return ErrUnauthorized
	}
	return nil
}

// CanPolicy checks if the authenticated user can perform an action on a resource via policy
func CanPolicy(r *http.Request, resourceType, action string, resource interface{}) bool {
	user := User(r)
	if user == nil {
		return false
	}
	return GetGate().AuthorizePolicy(user, resourceType, action, resource)
}

// HasRole checks if the authenticated user has a role
func HasRole(r *http.Request, role string) bool {
	user := User(r)
	if user == nil {
		return false
	}
	return GetGate().HasRole(user, role)
}

// HasAnyRole checks if the authenticated user has any of the given roles
func HasAnyRole(r *http.Request, roles ...string) bool {
	user := User(r)
	if user == nil {
		return false
	}
	return GetGate().HasAnyRole(user, roles...)
}

// HasAllRoles checks if the authenticated user has all the given roles
func HasAllRoles(r *http.Request, roles ...string) bool {
	user := User(r)
	if user == nil {
		return false
	}
	return GetGate().HasAllRoles(user, roles...)
}

// ForRequest creates a user-scoped gate for the authenticated user
func ForRequest(r *http.Request) *UserGate {
	user := User(r)
	if user == nil {
		return nil
	}
	return GetGate().ForUser(user)
}

// ResourceLoader loads a resource from request parameters
type ResourceLoader func(r *http.Request) (interface{}, error)

// AuthorizeMiddleware creates middleware that checks authorization for an ability
func AuthorizeMiddleware(ability string, loader ...ResourceLoader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := User(r)
			if user == nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			var args []interface{}
			if len(loader) > 0 && loader[0] != nil {
				resource, err := loader[0](r)
				if err != nil {
					http.Error(w, "Resource not found", http.StatusNotFound)
					return
				}
				args = append(args, resource)
			}

			if !GetGate().Allows(user, ability, args...) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireRole creates middleware that requires a specific role
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !HasRole(r, role) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAnyRole creates middleware that requires any of the given roles
func RequireAnyRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !HasAnyRole(r, roles...) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAllRoles creates middleware that requires all of the given roles
func RequireAllRoles(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !HasAllRoles(r, roles...) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
