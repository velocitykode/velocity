package guards

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
)

// rememberRandReader is the entropy source for remember-me tokens. Tests may
// swap this out to simulate rand.Read failures.
var rememberRandReader io.Reader = rand.Reader

// sessionCtxKey is an unexported context key type to avoid collisions.
type sessionCtxKey struct{}

// sessionHolder is a mutable container for session data stored in request context.
type sessionHolder struct {
	session auth.Session
}

// WithSessionContext returns a new request with a session cache attached to its context.
// Call this from middleware to enable per-request session caching that is automatically
// cleaned up when the request completes.
func WithSessionContext(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionCtxKey{}, &sessionHolder{}))
}

// SessionGuard implements session-based authentication
type SessionGuard struct {
	provider  auth.UserProvider
	store     auth.SessionStore
	config    auth.SessionConfig
	hasher    auth.Hasher
	encryptor crypto.Encryptor
	throttler contract.LoginThrottler
}

// NewSessionGuard creates a new session guard.
// The encryptor parameter is optional — pass nil if crypto is not configured
// (session guard will still work if a non-cookie store is used later).
func NewSessionGuard(provider auth.UserProvider, config auth.SessionConfig, encryptor ...crypto.Encryptor) (*SessionGuard, error) {
	var enc crypto.Encryptor
	if len(encryptor) > 0 {
		enc = encryptor[0]
	}

	store, err := session.NewCookieStore(config, enc)
	if err != nil {
		return nil, err
	}

	return &SessionGuard{
		provider:  provider,
		store:     store,
		config:    config,
		hasher:    auth.NewBcryptHasher(10),
		encryptor: enc,
		throttler: auth.NoopLoginThrottler{},
	}, nil
}

// SetLoginThrottler installs a rate-limiter for Attempt() calls. Passing nil
// reverts to the no-op throttler.
func (g *SessionGuard) SetLoginThrottler(t contract.LoginThrottler) {
	if t == nil {
		g.throttler = auth.NoopLoginThrottler{}
		return
	}
	g.throttler = t
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
		return g.checkRememberCookie(r) != nil
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
		user := g.checkRememberCookie(r)
		if user != nil {
			// Re-establish session for the remembered user
			session.Put("user_id", user.GetAuthIdentifier())
			return user
		}
		return nil
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
		// Cache in request context if available
		if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok {
			holder.session = session
		}
	}

	// Regenerate session ID for security. A failure here must abort the
	// login — proceeding with the old session ID opens a session-fixation
	// window (an attacker who planted the cookie keeps access).
	if err := session.Regenerate(); err != nil {
		return fmt.Errorf("velocity/auth: login aborted: session regenerate failed: %w", err)
	}

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

// Attempt attempts to log in with credentials. The configured LoginThrottler
// is consulted before the credential check; failed attempts call
// RecordFailure and successes call RecordSuccess.
func (g *SessionGuard) Attempt(w http.ResponseWriter, r *http.Request, credentials map[string]interface{}, remember ...bool) (bool, error) {
	throttler := g.throttler
	if throttler == nil {
		throttler = auth.NoopLoginThrottler{}
	}
	key := auth.ThrottleKey(r, credentials)
	if !throttler.Allow(r, key) {
		return false, auth.ErrLoginThrottled
	}

	// Find user by credentials
	user, err := g.provider.FindByCredentials(credentials)
	if err != nil {
		throttler.RecordFailure(r, key)
		return false, nil // User not found
	}

	// Validate password
	password, ok := credentials["password"].(string)
	if !ok {
		throttler.RecordFailure(r, key)
		return false, auth.ErrInvalidCredentials
	}

	if !g.provider.ValidateCredentials(user, map[string]interface{}{"password": password}) {
		throttler.RecordFailure(r, key)
		return false, nil // Invalid password
	}

	// Login user
	if err := g.Login(w, r, user, remember...); err != nil {
		return false, err
	}

	throttler.RecordSuccess(r, key)
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
	// Check request context cache first
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder.session != nil {
		return holder.session
	}

	// Get from store
	session, err := auth.GetSessionFromRequest(r, g.store, g.config.Name)
	if err != nil {
		return nil
	}

	// Cache in request context if available
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok {
		holder.session = session
	}

	return session
}

// checkRememberCookie checks and validates remember cookie.
// Returns the authenticated user if the cookie is valid, nil otherwise.
func (g *SessionGuard) checkRememberCookie(r *http.Request) auth.Authenticatable {
	cookie, err := r.Cookie("remember_" + g.config.Name)
	if err != nil {
		return nil
	}

	// Decrypt cookie value
	if g.encryptor == nil {
		return nil
	}
	decrypted, err := g.encryptor.Decrypt(cookie.Value)
	if err != nil {
		return nil
	}

	// Parse remember token format: userID|token
	parts := strings.SplitN(decrypted, "|", 2)
	if len(parts) != 2 {
		return nil
	}

	userID := parts[0]
	token := parts[1]

	// Look up user by ID
	user, err := g.provider.FindByID(userID)
	if err != nil || user == nil {
		return nil
	}

	// Verify remember token with constant-time comparison.
	// We hash the incoming token with SHA-256 and compare against the stored
	// hash. Legacy rows that still hold a raw token continue to work because
	// we fall through to a direct compare.
	storedToken := user.GetRememberToken()
	if storedToken == "" {
		return nil
	}
	candidateHash := hashRememberToken(token)
	if subtle.ConstantTimeCompare([]byte(storedToken), []byte(candidateHash)) == 1 {
		return user
	}
	if subtle.ConstantTimeCompare([]byte(storedToken), []byte(token)) == 1 {
		return user
	}
	return nil
}

// rememberCookieLifetime returns the cookie TTL for remember-me:
// min(session lifetime, remember-me default). Returns an error when the
// session lifetime is zero so callers refuse to create the cookie.
func (g *SessionGuard) rememberCookieLifetime() (time.Duration, error) {
	const defaultRememberDuration = 30 * 24 * time.Hour
	if g.config.Lifetime <= 0 {
		return 0, errors.New("velocity/auth: session lifetime must be positive to enable remember-me")
	}
	sessionLifetime := time.Duration(g.config.Lifetime) * time.Minute
	if sessionLifetime < defaultRememberDuration {
		return sessionLifetime, nil
	}
	return defaultRememberDuration, nil
}

// setRememberCookie sets remember me cookie.
// The raw token is encrypted into the cookie; only its SHA-256 hash is
// persisted on the user record. Cookie TTL is min(session lifetime,
// 30 days). Refuses to issue a cookie when the session lifetime is zero.
func (g *SessionGuard) setRememberCookie(w http.ResponseWriter, user auth.Authenticatable) error {
	ttl, err := g.rememberCookieLifetime()
	if err != nil {
		return err
	}

	// Generate remember token.
	token, err := generateRememberToken()
	if err != nil {
		return err
	}

	// Store only the hash of the token on the user record.
	hashed := hashRememberToken(token)
	user.SetRememberToken(hashed)
	if err := g.provider.UpdateRememberToken(user, hashed); err != nil {
		return err
	}

	// Create cookie value: userID|token (raw token; cookie is encrypted).
	userID, ok := user.GetAuthIdentifier().(string)
	if !ok {
		return fmt.Errorf("velocity/auth: expected string user identifier, got %T", user.GetAuthIdentifier())
	}
	value := userID + "|" + token

	// Encrypt value
	if g.encryptor == nil {
		return errors.New("velocity/auth: encryptor not configured, cannot set remember cookie")
	}
	encrypted, err := g.encryptor.Encrypt(value)
	if err != nil {
		return err
	}

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "remember_" + g.config.Name,
		Value:    encrypted,
		Path:     g.config.Path,
		Domain:   g.config.Domain,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   g.config.Secure,
		SameSite: g.config.SameSite,
		Expires:  time.Now().Add(ttl),
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

// generateRememberToken generates a random remember token.
// Returns an error rather than panicking when the entropy source fails.
func generateRememberToken() (string, error) {
	token := make([]byte, 32)
	if _, err := io.ReadFull(rememberRandReader, token); err != nil {
		return "", fmt.Errorf("velocity/auth: failed to generate remember token: %w", err)
	}
	return base64.URLEncoding.EncodeToString(token), nil
}

// hashRememberToken returns the hex-encoded SHA-256 digest of the raw
// remember-me token. Only the hash is stored server-side; the raw token
// lives in the user's cookie. This limits the blast radius if the users
// table leaks.
func hashRememberToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
