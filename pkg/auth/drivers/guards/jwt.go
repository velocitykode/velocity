package guards

import (
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/pkg/auth"
)

// JWTGuard implements JWT-based authentication for APIs
type JWTGuard struct {
	provider   auth.UserProvider
	jwtManager *auth.JWTManager
	config     auth.JWTConfig
	userCache  map[string]auth.Authenticatable // Simple cache for request
}

// NewJWTGuard creates a new JWT guard
func NewJWTGuard(provider auth.UserProvider, config auth.JWTConfig) *JWTGuard {
	return &JWTGuard{
		provider:   provider,
		jwtManager: auth.NewJWTManager(config),
		config:     config,
		userCache:  make(map[string]auth.Authenticatable),
	}
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
	g.userCache[token] = user
	return true
}

// User returns the authenticated user from JWT
func (g *JWTGuard) User(r *http.Request) auth.Authenticatable {
	token := g.getTokenFromRequest(r)
	if token == "" {
		return nil
	}

	// Check cache first
	if user, ok := g.userCache[token]; ok {
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
	g.userCache[token] = user
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

	// Revoke token
	g.jwtManager.RevokeToken(claims.ID)

	// Clear cache
	delete(g.userCache, token)

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
	// Check Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		// Bearer token
		parts := strings.Split(authHeader, " ")
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			return parts[1]
		}
		// Plain token
		return authHeader
	}

	// Check X-Auth-Token header
	if token := r.Header.Get("X-Auth-Token"); token != "" {
		return token
	}

	// Check query parameter (useful for websockets)
	if token := r.URL.Query().Get("token"); token != "" {
		return token
	}

	// Check form value
	if r.Method == "POST" {
		if token := r.FormValue("token"); token != "" {
			return token
		}
	}

	return ""
}
