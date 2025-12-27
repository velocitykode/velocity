package auth

import (
	"net/http"
	"os"
	"strconv"

	"golang.org/x/crypto/bcrypt"
)

// init automatically initializes auth from environment variables
func init() {
	// Check if AUTH_GUARD is set
	if os.Getenv("AUTH_GUARD") == "" {
		return // No auto-init if not configured
	}

	// Build configuration from environment
	config := Config{
		DefaultGuard: os.Getenv("AUTH_GUARD"),
		Guards:       make(map[string]GuardConfig),
		Providers:    make(map[string]ProviderConfig),
	}

	// Configure session/web guard
	sessionLifetime, _ := strconv.Atoi(os.Getenv("SESSION_LIFETIME"))
	if sessionLifetime == 0 {
		sessionLifetime = 120 // Default 2 hours
	}

	sessionConfig := SessionConfig{
		Driver:   getEnvOrDefault("SESSION_DRIVER", "cookie"),
		Name:     getEnvOrDefault("SESSION_NAME", "velocity_session"),
		Lifetime: sessionLifetime,
		Path:     getEnvOrDefault("SESSION_PATH", "/"),
		Domain:   os.Getenv("SESSION_DOMAIN"),
		Secure:   os.Getenv("SESSION_SECURE") == "true",
		HttpOnly: getEnvOrDefault("SESSION_HTTP_ONLY", "true") == "true",
		SameSite: parseSameSite(os.Getenv("SESSION_SAME_SITE")),
	}

	config.Guards["web"] = GuardConfig{
		Driver:   "session",
		Provider: "users",
		Options: map[string]interface{}{
			"session": sessionConfig,
		},
	}
	config.Guards["session"] = config.Guards["web"] // Alias

	// Configure JWT/API guard
	jwtTTL, _ := strconv.Atoi(os.Getenv("JWT_TTL"))
	if jwtTTL == 0 {
		jwtTTL = 60 // Default 1 hour
	}

	jwtRefreshTTL, _ := strconv.Atoi(os.Getenv("JWT_REFRESH_TTL"))
	if jwtRefreshTTL == 0 {
		jwtRefreshTTL = 20160 // Default 2 weeks
	}

	jwtConfig := JWTConfig{
		Secret:           os.Getenv("JWT_SECRET"),
		Algorithm:        getEnvOrDefault("JWT_ALGO", "HS256"),
		TTL:              jwtTTL,
		RefreshTTL:       jwtRefreshTTL,
		BlacklistEnabled: os.Getenv("JWT_BLACKLIST_ENABLED") == "true",
	}

	config.Guards["api"] = GuardConfig{
		Driver:   "jwt",
		Provider: "users",
		Options: map[string]interface{}{
			"jwt": jwtConfig,
		},
	}
	config.Guards["jwt"] = config.Guards["api"] // Alias

	// Configure default user provider
	config.Providers["users"] = ProviderConfig{
		Driver: "orm",
		Model:  getEnvOrDefault("AUTH_MODEL", "User"),
	}

	// Initialize hasher with bcrypt
	bcryptCost, _ := strconv.Atoi(os.Getenv("HASH_BCRYPT_COST"))
	if bcryptCost == 0 {
		bcryptCost = bcrypt.DefaultCost
	}
	InitHasher(NewBcryptHasher(bcryptCost))

	// Initialize the auth manager
	// The ORM provider will be initialized later when the database is available
	Init(config)
}

// getEnvOrDefault gets environment variable or returns default
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// parseSameSite parses SameSite cookie attribute
func parseSameSite(value string) http.SameSite {
	switch value {
	case "strict":
		return http.SameSiteStrictMode
	case "lax":
		return http.SameSiteLaxMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}
