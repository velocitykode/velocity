package guards

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/auth"
)

const (
	jwtCacheTTL        = 5 * time.Minute
	jwtCacheMaxSize    = 10000
	jwtCleanupInterval = 1 * time.Minute
)

type cachedUser struct {
	user     auth.Authenticatable
	cachedAt time.Time
}

// JWTGuard implements JWT-based authentication for APIs
type JWTGuard struct {
	provider    auth.UserProvider
	jwtManager  *auth.JWTManager
	config      auth.JWTConfig
	mu          sync.RWMutex
	userCache   map[string]cachedUser
	stopCleanup chan struct{}
}

// NewJWTGuard creates a new JWT guard. An optional context.Context can be passed
// to control the lifecycle of the background cache cleanup goroutine. When the
// context is cancelled, the cleanup goroutine stops automatically. If no context
// is provided, call StopCleanup() to stop the goroutine manually.
func NewJWTGuard(provider auth.UserProvider, config auth.JWTConfig, ctx ...context.Context) *JWTGuard {
	g := &JWTGuard{
		provider:    provider,
		jwtManager:  auth.NewJWTManager(config),
		config:      config,
		userCache:   make(map[string]cachedUser),
		stopCleanup: make(chan struct{}),
	}
	if len(ctx) > 0 && ctx[0] != nil {
		go g.cleanupLoopWithContext(ctx[0])
	} else {
		go g.cleanupLoop()
	}
	return g
}

// StopCleanup stops the background cache cleanup goroutine.
func (g *JWTGuard) StopCleanup() {
	select {
	case <-g.stopCleanup:
		// already closed
	default:
		close(g.stopCleanup)
	}
}

func (g *JWTGuard) cleanupLoop() {
	ticker := time.NewTicker(jwtCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			g.evictExpired()
		case <-g.stopCleanup:
			return
		}
	}
}

func (g *JWTGuard) cleanupLoopWithContext(ctx context.Context) {
	ticker := time.NewTicker(jwtCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			g.evictExpired()
		case <-g.stopCleanup:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (g *JWTGuard) evictExpired() {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for token, entry := range g.userCache {
		if now.Sub(entry.cachedAt) > jwtCacheTTL {
			delete(g.userCache, token)
		}
	}
}

func (g *JWTGuard) getCachedUser(token string) (auth.Authenticatable, bool) {
	g.mu.RLock()
	entry, ok := g.userCache[token]
	g.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Since(entry.cachedAt) > jwtCacheTTL {
		g.mu.Lock()
		delete(g.userCache, token)
		g.mu.Unlock()
		return nil, false
	}
	return entry.user, true
}

func (g *JWTGuard) cacheUser(token string, user auth.Authenticatable) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// If cache is full, evict oldest entry
	if len(g.userCache) >= jwtCacheMaxSize {
		var oldestToken string
		var oldestTime time.Time
		for t, entry := range g.userCache {
			if oldestToken == "" || entry.cachedAt.Before(oldestTime) {
				oldestToken = t
				oldestTime = entry.cachedAt
			}
		}
		if oldestToken != "" {
			delete(g.userCache, oldestToken)
		}
	}

	g.userCache[token] = cachedUser{user: user, cachedAt: time.Now()}
}

// Check if user is authenticated via JWT
func (g *JWTGuard) Check(r *http.Request) bool {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return false
	}

	claims, err := g.jwtManager.ValidateToken(token)
	if err != nil {
		return false
	}

	// Validate user still exists
	user, err := g.provider.FindByID(claims.UserID)
	if err != nil || user == nil {
		return false
	}

	// Cache user for this request
	g.cacheUser(token, user)
	return true
}

// User returns the authenticated user from JWT
func (g *JWTGuard) User(r *http.Request) auth.Authenticatable {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return nil
	}

	// Check cache first
	if user, ok := g.getCachedUser(token); ok {
		return user
	}

	claims, err := g.jwtManager.ValidateToken(token)
	if err != nil {
		return nil
	}

	user, err := g.provider.FindByID(claims.UserID)
	if err != nil {
		return nil
	}

	// Cache for subsequent calls
	g.cacheUser(token, user)
	return user
}

// ID returns the authenticated user ID from JWT
func (g *JWTGuard) ID(r *http.Request) interface{} {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return nil
	}

	claims, err := g.jwtManager.ValidateToken(token)
	if err != nil {
		return nil
	}

	return claims.UserID
}

// Login generates a JWT token for the user
func (g *JWTGuard) Login(w http.ResponseWriter, r *http.Request, user auth.Authenticatable, remember ...bool) error {
	// Generate access token
	token, err := g.jwtManager.GenerateToken(user)
	if err != nil {
		return err
	}

	// For JWT, we typically return the token in response body
	// not as a cookie. The actual response handling should be
	// done by the controller/handler
	w.Header().Set("X-Auth-Token", token)

	// If remember is true, also generate refresh token
	if len(remember) > 0 && remember[0] {
		refreshToken, err := g.jwtManager.GenerateRefreshToken(user)
		if err != nil {
			return err
		}
		w.Header().Set("X-Refresh-Token", refreshToken)
	}

	return nil
}

// LoginByID logs in a user by ID and generates JWT
func (g *JWTGuard) LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
	user, err := g.provider.FindByID(id)
	if err != nil {
		return err
	}

	return g.Login(w, r, user, remember...)
}

// Attempt validates credentials and generates JWT if valid
func (g *JWTGuard) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	// Find user by credentials
	user, err := g.provider.FindByCredentials(credentials)
	if err != nil {
		return false, nil // User not found
	}

	// Validate password
	password, ok := credentials["password"].(string)
	if !ok {
		return false, auth.ErrInvalidCredentials
	}

	if !g.provider.ValidateCredentials(user, map[string]interface{}{"password": password}) {
		return false, nil // Invalid password
	}

	// Generate token
	if err := g.Login(w, r, user, remember...); err != nil {
		return false, err
	}

	return true, nil
}

// Logout revokes the JWT token
func (g *JWTGuard) Logout(w http.ResponseWriter, r *http.Request) error {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return nil
	}

	// Parse token to get JTI
	claims, err := g.jwtManager.ValidateToken(token)
	if err != nil {
		// Even if token is invalid, we proceed with logout
		return nil
	}

	// Revoke token using its actual expiry for blacklist duration
	g.jwtManager.RevokeToken(claims.ID, claims.ExpiresAt.Time)

	// Clear cache
	g.mu.Lock()
	delete(g.userCache, token)
	g.mu.Unlock()

	return nil
}

// SetProvider sets the user provider
func (g *JWTGuard) SetProvider(provider auth.UserProvider) {
	g.provider = provider
}

// GenerateToken generates a JWT token for a user
func (g *JWTGuard) GenerateToken(user auth.Authenticatable, claims ...map[string]interface{}) (string, error) {
	return g.jwtManager.GenerateToken(user, claims...)
}

// GenerateRefreshToken generates a refresh token
func (g *JWTGuard) GenerateRefreshToken(user auth.Authenticatable) (string, error) {
	return g.jwtManager.GenerateRefreshToken(user)
}

// RefreshToken refreshes an access token using refresh token
func (g *JWTGuard) RefreshToken(refreshToken string) (string, error) {
	return g.jwtManager.RefreshToken(refreshToken, g.provider)
}

// ValidateToken validates a JWT token
func (g *JWTGuard) ValidateToken(token string) (*auth.Claims, error) {
	return g.jwtManager.ValidateToken(token)
}

// getTokenFromRequest extracts JWT token from request
func (g *JWTGuard) getTokenFromRequest(r *http.Request) string {
	// Check Authorization header — only accept "Bearer <token>" format
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		if !strings.HasPrefix(authHeader, "Bearer ") {
			return ""
		}
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	// Check X-Auth-Token header
	if token := r.Header.Get("X-Auth-Token"); token != "" {
		return token
	}

	// Check query parameter (restricted to WebSocket upgrade requests only)
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		if token := r.URL.Query().Get("token"); token != "" {
			return token
		}
	}

	// Check form value
	if r.Method == "POST" {
		if token := r.FormValue("token"); token != "" {
			return token
		}
	}

	return ""
}
