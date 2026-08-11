package auth

import (
	"errors"
	"sync"
)

// Authorization errors
var (
	ErrUnauthorized    = errors.New("unauthorized action")
	ErrPolicyNotFound  = errors.New("policy not found")
	ErrAccessNotFound  = errors.New("access not found")
	ErrNoUserInContext = errors.New("no authenticated user in context")
	ErrInvalidResource = errors.New("invalid resource type")
)

// AccessCallback is a function that determines if a user can perform an action
type AccessCallback func(user Authenticatable, args ...interface{}) bool

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

// BeforeCallback is called before any access/policy check
// Return true to allow, false to deny, nil to continue to the actual check
type BeforeCallback func(user Authenticatable, ability string, args ...interface{}) *bool

// AfterCallback is called after any access/policy check
type AfterCallback func(user Authenticatable, ability string, result bool, args ...interface{}) bool

// RoleChecker is a function that checks if a user has a role
type RoleChecker func(user Authenticatable, role string) bool

// Access manages authorization abilities and policies
type Access struct {
	abilities   map[string]AccessCallback
	policies    map[string]Policy
	before      []BeforeCallback
	after       []AfterCallback
	roleChecker RoleChecker
	mu          sync.RWMutex
}

// NewAccess creates a new Access instance
func NewAccess() *Access {
	return &Access{
		abilities: make(map[string]AccessCallback),
		policies:  make(map[string]Policy),
		before:    make([]BeforeCallback, 0),
		after:     make([]AfterCallback, 0),
	}
}

// Define registers an access callback for an ability
func (g *Access) Define(ability string, callback AccessCallback) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.abilities[ability] = callback
}

// Policy registers a policy for a resource type
func (g *Access) RegisterPolicy(resourceType string, policy Policy) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.policies[resourceType] = policy
}

// Before registers a callback to run before authorization checks
func (g *Access) Before(callback BeforeCallback) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.before = append(g.before, callback)
}

// After registers a callback to run after authorization checks
func (g *Access) After(callback AfterCallback) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.after = append(g.after, callback)
}

// SetRoleChecker sets the function used to check user roles
func (g *Access) SetRoleChecker(checker RoleChecker) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.roleChecker = checker
}

// Allows checks if a user is allowed to perform an ability
func (g *Access) Allows(user Authenticatable, ability string, args ...interface{}) bool {
	g.mu.RLock()
	beforeCallbacks := make([]BeforeCallback, len(g.before))
	copy(beforeCallbacks, g.before)
	afterCallbacks := make([]AfterCallback, len(g.after))
	copy(afterCallbacks, g.after)
	callback, ok := g.abilities[ability]
	g.mu.RUnlock()

	// Run before callbacks
	for _, before := range beforeCallbacks {
		if result := before(user, ability, args...); result != nil {
			return runAfter(afterCallbacks, user, ability, *result, args...)
		}
	}

	// Check access
	if ok {
		result := callback(user, args...)
		return runAfter(afterCallbacks, user, ability, result, args...)
	}

	return runAfter(afterCallbacks, user, ability, false, args...)
}

// Denies checks if a user is denied from performing an ability
func (g *Access) Denies(user Authenticatable, ability string, args ...interface{}) bool {
	return !g.Allows(user, ability, args...)
}

// Check checks multiple abilities (all must pass)
func (g *Access) Check(user Authenticatable, abilities []string, args ...interface{}) bool {
	for _, ability := range abilities {
		if !g.Allows(user, ability, args...) {
			return false
		}
	}
	return true
}

// Any checks if any of the abilities pass
func (g *Access) Any(user Authenticatable, abilities []string, args ...interface{}) bool {
	for _, ability := range abilities {
		if g.Allows(user, ability, args...) {
			return true
		}
	}
	return false
}

// AuthorizePolicy checks authorization using a registered policy.
//
// Snapshot pattern: copy the policy reference and the before-callback slice
// under a brief RLock, then release the lock and invoke user callbacks
// OUTSIDE any lock. A panic inside a before callback would otherwise leak
// the RLock permanently (sync.RWMutex is not goroutine-attached and does
// not unwind on panic), wedging every future Access.Define/Before/After
// writer and every reader queued behind it, a permanent authorization DoS.
// Mirrors the snapshot used by runAfterCallbacks below.
func (g *Access) AuthorizePolicy(user Authenticatable, resourceType, action string, resource interface{}) bool {
	g.mu.RLock()
	policy, ok := g.policies[resourceType]
	// Snapshot before-callback slice. append() inside Before() either grows
	// into a new backing array (leaving ours untouched) or appends within
	// existing capacity. To stay safe against the in-cap-append case we
	// copy the slice header contents into a fresh slice.
	beforeCallbacks := make([]BeforeCallback, len(g.before))
	copy(beforeCallbacks, g.before)
	g.mu.RUnlock()

	if !ok {
		return false
	}

	// Run before callbacks OUTSIDE the lock. A panic here can be recovered
	// upstream without leaving g.mu in a wedged state.
	for _, before := range beforeCallbacks {
		if result := before(user, action, resource); result != nil {
			return g.runAfterCallbacks(user, action, *result, resource)
		}
	}

	result := policy.Authorize(user, action, resource)
	return g.runAfterCallbacks(user, action, result, resource)
}

// HasRole checks if a user has a specific role
func (g *Access) HasRole(user Authenticatable, role string) bool {
	g.mu.RLock()
	checker := g.roleChecker
	g.mu.RUnlock()

	if checker == nil {
		return false
	}
	return checker(user, role)
}

// HasAnyRole checks if a user has any of the given roles
func (g *Access) HasAnyRole(user Authenticatable, roles ...string) bool {
	for _, role := range roles {
		if g.HasRole(user, role) {
			return true
		}
	}
	return false
}

// HasAllRoles checks if a user has all the given roles
func (g *Access) HasAllRoles(user Authenticatable, roles ...string) bool {
	for _, role := range roles {
		if !g.HasRole(user, role) {
			return false
		}
	}
	return true
}

// runAfterCallbacks runs after callbacks and returns the final result
func (g *Access) runAfterCallbacks(user Authenticatable, ability string, result bool, args ...interface{}) bool {
	g.mu.RLock()
	afterCallbacks := make([]AfterCallback, len(g.after))
	copy(afterCallbacks, g.after)
	g.mu.RUnlock()

	return runAfter(afterCallbacks, user, ability, result, args...)
}

func runAfter(afterCallbacks []AfterCallback, user Authenticatable, ability string, result bool, args ...interface{}) bool {
	for _, after := range afterCallbacks {
		result = after(user, ability, result, args...)
	}
	return result
}

// ForUser creates a user-scoped authorization checker
func (g *Access) ForUser(user Authenticatable) *UserAccess {
	return &UserAccess{
		access: g,
		user:   user,
	}
}

// UserAccess provides authorization methods for a specific user
type UserAccess struct {
	access *Access
	user   Authenticatable
}

// Allows checks if the user is allowed to perform an ability
func (ug *UserAccess) Allows(ability string, args ...interface{}) bool {
	return ug.access.Allows(ug.user, ability, args...)
}

// Denies checks if the user is denied from performing an ability
func (ug *UserAccess) Denies(ability string, args ...interface{}) bool {
	return ug.access.Denies(ug.user, ability, args...)
}

// Can is an alias for Allows
func (ug *UserAccess) Can(ability string, args ...interface{}) bool {
	return ug.Allows(ability, args...)
}

// Cannot is an alias for Denies
func (ug *UserAccess) Cannot(ability string, args ...interface{}) bool {
	return ug.Denies(ability, args...)
}

// Authorize checks authorization and returns an error if denied
func (ug *UserAccess) Authorize(ability string, args ...interface{}) error {
	if !ug.Allows(ability, args...) {
		return ErrUnauthorized
	}
	return nil
}
