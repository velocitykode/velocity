package velocity

import (
	stdlog "log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/view"
)

// Config holds all configuration for a Velocity application.
// It replaces the scattered os.Getenv() calls across packages.
type Config struct {
	// App
	Name  string // APP_NAME, default "Velocity"
	Env   string // APP_ENV, default "development"
	Debug bool   // APP_DEBUG, default false
	Port  string // PORT, default "4000"
	Key   string // APP_KEY (used for crypto)

	// Database
	DB DBConfig

	// Auth
	Auth AuthConfig

	// Cache
	Cache CacheConfig

	// Log
	Log LogConfig

	// Queue
	Queue QueueConfig

	// Storage
	Storage StorageConfig

	// CSRF
	CSRF CSRFConfig

	// Session
	Session SessionConfig

	// View
	View ViewConfig

	// Crypto
	Crypto CryptoConfig

	// Mail
	Mail MailConfig

	// Server timeouts
	ReadTimeout  time.Duration // SERVER_READ_TIMEOUT, default 30s
	WriteTimeout time.Duration // SERVER_WRITE_TIMEOUT, default 30s
	IdleTimeout  time.Duration // SERVER_IDLE_TIMEOUT, default 120s

	// Scheduler (no config needed, created fresh)
}

// DBConfig holds database configuration.
// Kept as a root type because it differs structurally from orm.ManagerConfig.
type DBConfig struct {
	Connection      string        // DB_CONNECTION: sqlite, postgres, mysql
	Host            string        // DB_HOST, default "127.0.0.1"
	Port            string        // DB_PORT, default per driver
	Database        string        // DB_DATABASE
	Username        string        // DB_USERNAME
	Password        string        // DB_PASSWORD
	Charset         string        // DB_CHARSET
	Collation       string        // DB_COLLATION
	Prefix          string        // DB_PREFIX
	Schema          string        // DB_SCHEMA
	SSLMode         string        // DB_SSL_MODE
	Timezone        string        // DB_TIMEZONE
	MaxIdleConns    int           // DB_MAX_IDLE_CONNS, default 10
	MaxOpenConns    int           // DB_MAX_OPEN_CONNS, default 100
	ConnMaxLifetime time.Duration // DB_CONN_MAX_LIFETIME, default 3600s
	LogQueries      bool          // DB_LOG_QUERIES
	SlowThreshold   time.Duration // DB_SLOW_QUERY_THRESHOLD
}

// Type aliases — these reuse the canonical package types to avoid duplication.
// Consumers can use either velocity.AuthConfig or auth.Config interchangeably.
type (
	AuthConfig     = auth.Config
	GuardConfig    = auth.GuardConfig
	ProviderConfig = auth.ProviderConfig
	SessionConfig  = auth.SessionConfig
	JWTConfig      = auth.JWTConfig
	LogConfig      = log.LogConfig
	CSRFConfig     = csrf.Config
	CryptoConfig   = crypto.Config
	ViewConfig     = view.Config
	MailConfig     = mail.MailConfig
)

// CacheConfig holds cache configuration.
// Kept as a root type because it uses a flat structure (single driver)
// while cache.Config uses a multi-store map pattern.
type CacheConfig struct {
	Driver        string // CACHE_DRIVER: memory, file, redis, database
	Prefix        string // CACHE_PREFIX, default "velocity_cache"
	Path          string // CACHE_PATH (for file driver)
	RedisHost     string // REDIS_HOST, default "127.0.0.1"
	RedisPort     int    // REDIS_PORT, default 6379
	RedisPassword string // REDIS_PASSWORD
	RedisDatabase int    // REDIS_DATABASE, default 0
}

// QueueConfig holds queue configuration.
// Kept as a root type because it uses a flat structure while
// queue.QueueConfig nests Redis fields in a sub-struct.
type QueueConfig struct {
	Driver        string // QUEUE_DRIVER: memory, redis, database
	RedisHost     string // QUEUE_REDIS_HOST, default "localhost"
	RedisPort     string // QUEUE_REDIS_PORT, default "6379"
	RedisPassword string // QUEUE_REDIS_PASSWORD
	RedisDB       string // QUEUE_REDIS_DB, default "0"
}

// StorageConfig holds storage configuration.
// Kept as a root type because the DiskConfig fields differ slightly
// from storage.DiskConfig (Endpoint field).
type StorageConfig struct {
	Default string                // STORAGE_DRIVER, default "local"
	Disks   map[string]DiskConfig // Disk configurations
}

// DiskConfig holds configuration for a single storage disk.
type DiskConfig struct {
	Driver     string // "local", "s3", "memory"
	Root       string // Root path for local driver
	URL        string // Base URL for file access
	Visibility string // Default visibility (public/private)
	// S3 fields
	Bucket   string
	Region   string
	Key      string
	Secret   string
	Endpoint string
	// Memory driver
	MaxSize int64 // Maximum memory usage in bytes
}

// Option is a function that configures the App.
type Option func(*App)

// WithConfig sets a custom configuration.
func WithConfig(config Config) Option {
	return func(a *App) {
		a.config = &config
	}
}

// WithPort sets the server port.
func WithPort(port string) Option {
	return func(a *App) {
		a.config.Port = port
	}
}

// WithReadTimeout sets the HTTP server read timeout.
func WithReadTimeout(d time.Duration) Option {
	return func(a *App) {
		a.config.ReadTimeout = d
	}
}

// WithWriteTimeout sets the HTTP server write timeout.
func WithWriteTimeout(d time.Duration) Option {
	return func(a *App) {
		a.config.WriteTimeout = d
	}
}

// WithIdleTimeout sets the HTTP server idle timeout.
func WithIdleTimeout(d time.Duration) Option {
	return func(a *App) {
		a.config.IdleTimeout = d
	}
}

// WithoutEvents disables the event dispatcher entirely.
// No framework events (request, query, cache, etc.) will be fired.
// Useful in tests where events add overhead or cause side effects.
func WithoutEvents() Option {
	return func(a *App) {
		a.noEvents = true
	}
}

// WithFakeEvents replaces the event dispatcher with a fake that records
// dispatched events without executing listeners. Use the returned
// *events.FakeDispatcher for assertions:
//
//	fake := events.NewFakeDispatcher()
//	app, _ := velocity.NewTestApp(velocity.WithFakeEvents(fake))
//	// ... trigger actions ...
//	fake.AssertDispatched(router.RequestHandled{}, nil)
func WithFakeEvents(fake *events.FakeDispatcher) Option {
	return func(a *App) {
		a.Services.Events = fake
	}
}

// WithProviders appends service providers to the application.
// Providers are registered and booted in the order they are given.
func WithProviders(providers ...app.ServiceProvider) Option {
	return func(a *App) {
		a.providers = append(a.providers, providers...)
	}
}

// ConfigFromEnv loads configuration from environment variables and .env file.
func ConfigFromEnv() Config {
	// Load .env file if present; warn if it exists but fails to parse.
	if err := godotenv.Load(); err != nil {
		if _, statErr := os.Stat(".env"); statErr == nil {
			stdlog.Println("[WARN] .env file exists but failed to parse:", err)
		}
	}

	config := Config{
		Name:  envOrDefault("APP_NAME", "Velocity"),
		Env:   envOrDefault("APP_ENV", "development"),
		Debug: envOrDefault("APP_DEBUG", "false") == "true",
		Port:  envOrDefault("PORT", "4000"),
		Key:   os.Getenv("APP_KEY"),
	}

	// Database
	dbConnection := os.Getenv("DB_CONNECTION")
	config.DB = DBConfig{
		Connection:      dbConnection,
		Host:            envOrDefault("DB_HOST", "127.0.0.1"),
		Port:            envOrDefault("DB_PORT", defaultPortForDriver(dbConnection)),
		Database:        os.Getenv("DB_DATABASE"),
		Username:        os.Getenv("DB_USERNAME"),
		Password:        os.Getenv("DB_PASSWORD"),
		Charset:         os.Getenv("DB_CHARSET"),
		Collation:       os.Getenv("DB_COLLATION"),
		Prefix:          os.Getenv("DB_PREFIX"),
		Schema:          os.Getenv("DB_SCHEMA"),
		SSLMode:         os.Getenv("DB_SSL_MODE"),
		Timezone:        os.Getenv("DB_TIMEZONE"),
		MaxIdleConns:    envIntOrDefault("DB_MAX_IDLE_CONNS", 10),
		MaxOpenConns:    envIntOrDefault("DB_MAX_OPEN_CONNS", 100),
		ConnMaxLifetime: time.Duration(envIntOrDefault("DB_CONN_MAX_LIFETIME", 3600)) * time.Second,
		LogQueries:      os.Getenv("DB_LOG_QUERIES") == "true",
		SlowThreshold:   envDurationOrDefault("DB_SLOW_QUERY_THRESHOLD", 0),
	}

	// Session
	config.Session = SessionConfig{
		Driver:   envOrDefault("SESSION_DRIVER", "cookie"),
		Name:     envOrDefault("SESSION_NAME", "velocity_session"),
		Lifetime: envIntOrDefault("SESSION_LIFETIME", 120),
		Path:     envOrDefault("SESSION_PATH", "/"),
		Domain:   os.Getenv("SESSION_DOMAIN"),
		Secure:   os.Getenv("SESSION_SECURE") != "false",
		HttpOnly: envOrDefault("SESSION_HTTP_ONLY", "true") == "true",
		SameSite: parseSameSite(os.Getenv("SESSION_SAME_SITE")),
	}

	// Auth
	config.Auth = AuthConfig{
		DefaultGuard: os.Getenv("AUTH_GUARD"),
		Guards:       make(map[string]GuardConfig),
		Providers:    make(map[string]ProviderConfig),
		BcryptCost:   envIntOrDefault("HASH_BCRYPT_COST", 10),
	}

	// Configure guards if AUTH_GUARD is set
	if config.Auth.DefaultGuard != "" {
		// Session/web guard
		config.Auth.Guards["web"] = GuardConfig{
			Driver:   "session",
			Provider: "users",
			Options: map[string]interface{}{
				"session": config.Session,
			},
		}
		config.Auth.Guards["session"] = config.Auth.Guards["web"]

		// JWT/API guard
		jwtTTL := envIntOrDefault("JWT_TTL", 60)
		jwtRefreshTTL := envIntOrDefault("JWT_REFRESH_TTL", 20160)
		jwtSecret := os.Getenv("JWT_SECRET")

		config.Auth.Guards["api"] = GuardConfig{
			Driver:   "jwt",
			Provider: "users",
			Options: map[string]interface{}{
				"jwt": JWTConfig{
					Secret:           jwtSecret,
					Algorithm:        envOrDefault("JWT_ALGO", "HS256"),
					TTL:              jwtTTL,
					RefreshTTL:       jwtRefreshTTL,
					BlacklistEnabled: os.Getenv("JWT_BLACKLIST_ENABLED") != "false",
				},
			},
		}
		config.Auth.Guards["jwt"] = config.Auth.Guards["api"]

		// Default user provider
		config.Auth.Providers["users"] = ProviderConfig{
			Driver: "orm",
			Model:  envOrDefault("AUTH_MODEL", "User"),
		}
	}

	// CSRF
	config.CSRF = CSRFConfig{
		TokenLifetime:     envDurationOrDefault("CSRF_TOKEN_LIFETIME", 24*time.Hour),
		HeaderName:        envOrDefault("CSRF_HEADER", "X-CSRF-Token"),
		FormField:         envOrDefault("CSRF_FORM_FIELD", "_token"),
		CookieName:        envOrDefault("CSRF_COOKIE_NAME", "csrf_token"),
		SessionCookieName: envOrDefault("CSRF_SESSION_COOKIE", config.Session.Name),
		SameSite:          parseSameSite(os.Getenv("CSRF_SAME_SITE")),
		Secure:            os.Getenv("CSRF_SECURE") != "false",
		HTTPOnly:          envOrDefault("CSRF_HTTP_ONLY", "true") == "true",
		SingleUse:         os.Getenv("CSRF_SINGLE_USE") == "true",
		ErrorMessage:      envOrDefault("CSRF_ERROR_MESSAGE", "CSRF token validation failed. Please refresh and try again."),
	}

	// Cache
	config.Cache = CacheConfig{
		Driver:        envOrDefault("CACHE_DRIVER", "memory"),
		Prefix:        envOrDefault("CACHE_PREFIX", "velocity_cache"),
		Path:          os.Getenv("CACHE_PATH"),
		RedisHost:     envOrDefault("REDIS_HOST", "127.0.0.1"),
		RedisPort:     envIntOrDefault("REDIS_PORT", 6379),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		RedisDatabase: envIntOrDefault("REDIS_DATABASE", 0),
	}

	// Log
	config.Log = LogConfig{
		Driver: envOrDefault("LOG_DRIVER", "console"),
		Config: make(map[string]any),
	}
	if logPath := os.Getenv("LOG_PATH"); logPath != "" {
		config.Log.Config["path"] = logPath
	}
	config.Log.Config["days"] = envIntOrDefault("LOG_DAYS", 14)
	config.Log.Config["level"] = envOrDefault("LOG_LEVEL", "debug")
	if stackStr := os.Getenv("LOG_STACK"); stackStr != "" {
		var stack []string
		for _, ch := range strings.Split(stackStr, ",") {
			if ch = strings.TrimSpace(ch); ch != "" {
				stack = append(stack, ch)
			}
		}
		config.Log.Config["stack"] = stack
	}

	// Crypto
	cryptoKey := os.Getenv("CRYPTO_KEY")
	if cryptoKey == "" {
		cryptoKey = config.Key // Fall back to APP_KEY
	}
	config.Crypto = CryptoConfig{
		Key:    cryptoKey,
		Cipher: envOrDefault("CRYPTO_CIPHER", "AES-256-GCM"),
	}
	if oldKeys := os.Getenv("CRYPTO_OLD_KEYS"); oldKeys != "" {
		config.Crypto.PreviousKeys = strings.Split(oldKeys, ",")
	}

	// Queue
	config.Queue = QueueConfig{
		Driver:        envOrDefault("QUEUE_DRIVER", "memory"),
		RedisHost:     envOrDefault("QUEUE_REDIS_HOST", "localhost"),
		RedisPort:     envOrDefault("QUEUE_REDIS_PORT", "6379"),
		RedisPassword: os.Getenv("QUEUE_REDIS_PASSWORD"),
		RedisDB:       envOrDefault("QUEUE_REDIS_DB", "0"),
	}

	// Storage
	storageDefault := envOrDefault("STORAGE_DRIVER", "local")
	config.Storage = StorageConfig{
		Default: storageDefault,
		Disks:   make(map[string]DiskConfig),
	}
	// Always configure a local disk
	config.Storage.Disks["local"] = DiskConfig{
		Driver: "local",
		Root:   envOrDefault("FILESYSTEM_LOCAL_ROOT", "./storage/app"),
	}
	// Configure S3 disk if credentials are present
	if s3Bucket := os.Getenv("AWS_BUCKET"); s3Bucket != "" {
		config.Storage.Disks["s3"] = DiskConfig{
			Driver: "s3",
			Bucket: s3Bucket,
			Region: os.Getenv("AWS_DEFAULT_REGION"),
			Key:    os.Getenv("AWS_ACCESS_KEY_ID"),
			Secret: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			URL:    os.Getenv("AWS_URL"),
		}
	}

	// Mail
	config.Mail = MailConfig{
		Driver: envOrDefault("MAIL_DRIVER", "log"),
	}

	// Server timeouts
	config.ReadTimeout = envDurationOrDefault("SERVER_READ_TIMEOUT", 30*time.Second)
	config.WriteTimeout = envDurationOrDefault("SERVER_WRITE_TIMEOUT", 30*time.Second)
	config.IdleTimeout = envDurationOrDefault("SERVER_IDLE_TIMEOUT", 120*time.Second)

	return config
}

// Helper functions

func envOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func envIntOrDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if v, err := strconv.Atoi(value); err == nil {
			return v
		}
	}
	return defaultValue
}

func envDurationOrDefault(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return defaultValue
}

func defaultPortForDriver(driver string) string {
	switch driver {
	case "mysql":
		return "3306"
	case "postgres":
		return "5432"
	default:
		return ""
	}
}

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
