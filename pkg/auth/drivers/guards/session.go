package guards

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/velocitykode/velocity/pkg/auth"
	"github.com/velocitykode/velocity/pkg/auth/drivers/session"
	"github.com/velocitykode/velocity/pkg/crypto"
)

// SessionGuard implements session-based authentication
type SessionGuard struct {
	provider auth.UserProvider
	store    auth.SessionStore
	config   auth.SessionConfig
	hasher   auth.Hasher
	sessions map[*http.Request]auth.Session // Request-scoped session cache
}

// NewSessionGuard creates a new session guard
func NewSessionGuard(provider auth.UserProvider, config auth.SessionConfig) (*SessionGuard, error) {
	// Create session store based on driver
	var store auth.SessionStore
	var err error

	switch config.Driver {
	case "cookie", "":
		store, err = session.NewCookieStore(config)
	default:
		// Default to cookie store
		store, err = session.NewCookieStore(config)
	}

	if err != nil {
		return nil, err
	}

	return &SessionGuard{
		provider: provider,
		store:    store,
		config:   config,
		hasher:   auth.GetHasher(),
		sessions: make(map[*http.Request]auth.Session),
	}, nil
}

// Check if user is authenticated
func (g *SessionGuard) Check(r *http.Request) bool {
	session := g.getSession(r)
	if session == nil {
		return false
	}

	// Check if user ID exists in session
	userID := session.Get("user_id")
	if userID == nil {
		// Check remember cookie
		return g.checkRememberCookie(r)
	}

	// Validate user still exists
	user, err := g.provider.FindByID(userID)
	return err == nil && user != nil
}

// User returns the authenticated user
func (g *SessionGuard) User(r *http.Request) auth.Authenticatable {
	session := g.getSession(r)
	if session == nil {
		return nil
	}

	userID := session.Get("user_id")
	if userID == nil {
		// Try remember cookie
		if g.checkRememberCookie(r) {
			userID = session.Get("user_id")
		} else {
			return nil
		}
	}

	user, err := g.provider.FindByID(userID)
	if err != nil {
		return nil
	}

	return user
}

// ID returns the authenticated user ID
func (g *SessionGuard) ID(r *http.Request) interface{} {
	session := g.getSession(r)
	if session == nil {
		return nil
	}

	return session.Get("user_id")
}

// Login logs in a user
func (g *SessionGuard) Login(w http.ResponseWriter, r *http.Request, user auth.Authenticatable, remember ...bool) error {
	session := g.getSession(r)
	if session == nil {
		var err error
		session, err = g.store.Create("")
		if err != nil {
			return err
		}
		g.sessions[r] = session
	}

	// Regenerate session ID for security
	session.Regenerate()

	// Store user ID in session
	session.Put("user_id", user.GetAuthIdentifier())

	// Handle remember me
	if len(remember) > 0 && remember[0] {
		if err := g.setRememberCookie(w, user); err != nil {
			return err
		}
	}

	// Save session
	return session.Save(w)
}

// LoginByID logs in a user by ID
func (g *SessionGuard) LoginByID(w http.ResponseWriter, r *http.Request, id interface{}, remember ...bool) error {
	user, err := g.provider.FindByID(id)
	if err != nil {
		return err
	}

	return g.Login(w, r, user, remember...)
}

// Attempt attempts to log in with credentials
func (g *SessionGuard) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
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

	// Login user
	if err := g.Login(w, r, user, remember...); err != nil {
		return false, err
	}

	return true, nil
}

// Logout logs out the user
func (g *SessionGuard) Logout(w http.ResponseWriter, r *http.Request) error {
	session := g.getSession(r)
	if session == nil {
		return nil
	}

	// Clear remember cookie
	g.clearRememberCookie(w)

	// Invalidate session
	if err := session.Invalidate(); err != nil {
		return err
	}

	// Save invalidated session (will delete cookie)
	return session.Save(w)
}

// SetProvider sets the user provider
func (g *SessionGuard) SetProvider(provider auth.UserProvider) {
	g.provider = provider
}

// getSession gets or creates session for request
func (g *SessionGuard) getSession(r *http.Request) auth.Session {
	// Check cache first
	if session, ok := g.sessions[r]; ok {
		return session
	}

	// Get from store
	session, err := auth.GetSessionFromRequest(r, g.store, g.config.Name)
	if err != nil {
		return nil
	}

	// Cache for this request
	g.sessions[r] = session
	return session
}

// checkRememberCookie checks and validates remember cookie
func (g *SessionGuard) checkRememberCookie(r *http.Request) bool {
	cookie, err := r.Cookie("remember_" + g.config.Name)
	if err != nil {
		return false
	}

	// Decrypt cookie value
	_, err = crypto.Decrypt(cookie.Value)
	if err != nil {
		return false
	}

	// Parse remember token format: userID|token
	// This is simplified - in production, store tokens in database
	// and validate against stored tokens

	return false // Simplified for now
}

// setRememberCookie sets remember me cookie
func (g *SessionGuard) setRememberCookie(w http.ResponseWriter, user auth.Authenticatable) error {
	// Generate remember token
	token := generateRememberToken()

	// Update user's remember token
	user.SetRememberToken(token)
	if err := g.provider.UpdateRememberToken(user, token); err != nil {
		return err
	}

	// Create cookie value: userID|token
	value := string(user.GetAuthIdentifier().(string)) + "|" + token

	// Encrypt value
	encrypted, err := crypto.Encrypt(value)
	if err != nil {
		return err
	}

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "remember_" + g.config.Name,
		Value:    encrypted,
		Path:     g.config.Path,
		Domain:   g.config.Domain,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
		HttpOnly: true,
		Secure:   g.config.Secure,
		SameSite: g.config.SameSite,
		Expires:  time.Now().Add(30 * 24 * time.Hour),
	})

	return nil
}

// clearRememberCookie clears remember me cookie
func (g *SessionGuard) clearRememberCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "remember_" + g.config.Name,
		Value:    "",
		Path:     g.config.Path,
		Domain:   g.config.Domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   g.config.Secure,
		SameSite: g.config.SameSite,
	})
}

// generateRememberToken generates a random remember token
func generateRememberToken() string {
	// Use crypto package to generate secure token
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		// Fallback
		return base64.URLEncoding.EncodeToString([]byte(time.Now().String()))
	}
	return base64.URLEncoding.EncodeToString(token)
}
