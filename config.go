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
	Env   string // APP_ENV, default "development"
	Debug bool   // APP_DEBUG, default false
	Port  string // APP_PORT, default "4000"
	Key   string // APP_KEY (used for crypto)

	// Database
	DB DBConfig

	// Auth
	Auth auth.Config

	// Cache
	Cache CacheConfig

	// Log
	Log log.LogConfig

	// Queue
	Queue QueueConfig

	// Storage
	Storage StorageConfig

	// CSRF
	CSRF csrf.Config

	// Session
	Session auth.SessionConfig

	// View
	View view.Config

	// Crypto
	Crypto crypto.Config

	// Mail
	Mail mail.MailConfig

	// Server timeouts
	ReadTimeout       time.Duration // SERVER_READ_TIMEOUT, default 30s
	WriteTimeout      time.Duration // SERVER_WRITE_TIMEOUT, default 30s
	IdleTimeout       time.Duration // SERVER_IDLE_TIMEOUT, default 120s
	ReadHeaderTimeout time.Duration // SERVER_READ_HEADER_TIMEOUT, default 10s

	// Scheduler (no config needed, created fresh)
}

// DBConfig holds database configuration. Maps the DB_* env vars onto the
// fields forwarded into orm.ManagerConfig via initDB.
type DBConfig struct {
	Connection      string        // DB_CONNECTION: sqlite, postgres, mysql
	Host            string        // DB_HOST, default "127.0.0.1"
	Port            string        // DB_PORT, default per driver
	Database        string        // DB_DATABASE
	Username        string        // DB_USERNAME
	Password        string        // DB_PASSWORD
	Charset         string        // DB_CHARSET
	SSLMode         string        // DB_SSL_MODE (postgres)
	TLS             string        // DB_MYSQL_TLS (mysql: true/false/skip-verify/preferred)
	MaxIdleConns    int           // DB_MAX_IDLE_CONNS, default 10
	MaxOpenConns    int           // DB_MAX_OPEN_CONNS, default 100
	ConnMaxLifetime time.Duration // DB_CONN_MAX_LIFETIME, default 3600s
	LogQueries      bool          // DB_LOG_QUERIES
	SlowThreshold   time.Duration // DB_SLOW_QUERY_THRESHOLD
}

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
	RedisTLS      bool   // REDIS_TLS: enable TLS for Redis connections
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
	RedisTLS      bool   // REDIS_TLS: enable TLS for Redis connections
	SigningKey    string // QUEUE_SIGNING_KEY: HMAC key for payload signing (SENSITIVE)
}

// StorageConfig holds storage configuration.
// Kept as a root type because the DiskConfig fields mirror the env-var
// layout rather than the storage package's internal driver config.
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
	Bucket string
	Region string
	Key    string
	Secret string
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
//	app, _ := velocitytest.NewApp(velocity.WithFakeEvents(fake))
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
		Env:   envOrDefault("APP_ENV", "development"),
		Debug: envOrDefault("APP_DEBUG", "false") == "true",
		Port:  envOrDefault("APP_PORT", "4000"),
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
		SSLMode:         os.Getenv("DB_SSL_MODE"),
		TLS:             os.Getenv("DB_MYSQL_TLS"),
		MaxIdleConns:    envIntOrDefault("DB_MAX_IDLE_CONNS", 10),
		MaxOpenConns:    envIntOrDefault("DB_MAX_OPEN_CONNS", 100),
		ConnMaxLifetime: time.Duration(envIntOrDefault("DB_CONN_MAX_LIFETIME", 3600)) * time.Second,
		LogQueries:      os.Getenv("DB_LOG_QUERIES") == "true",
		SlowThreshold:   envDurationOrDefault("DB_SLOW_QUERY_THRESHOLD", 0),
	}

	// Session
	config.Session = auth.SessionConfig{
		Name:     envOrDefault("SESSION_NAME", "velocity_session"),
		Lifetime: envIntOrDefault("SESSION_LIFETIME", 120),
		Path:     envOrDefault("SESSION_PATH", "/"),
		Domain:   os.Getenv("SESSION_DOMAIN"),
		Secure:   os.Getenv("SESSION_SECURE") != "false",
		HttpOnly: envOrDefault("SESSION_HTTP_ONLY", "true") == "true",
		SameSite: parseSameSite(os.Getenv("SESSION_SAME_SITE")),
	}

	// Auth
	config.Auth = auth.Config{
		DefaultGuard: os.Getenv("AUTH_GUARD"),
		Guards:       make(map[string]auth.GuardConfig),
		Providers:    make(map[string]auth.ProviderConfig),
		BcryptCost:   envIntOrDefault("HASH_BCRYPT_COST", 10),
	}

	// Configure guards if AUTH_GUARD is set
	if config.Auth.DefaultGuard != "" {
		// Session/web guard
		config.Auth.Guards["web"] = auth.GuardConfig{
			Driver:   "session",
			Provider: "users",
			Options: map[string]interface{}{
				"session": config.Session,
			},
		}
		config.Auth.Guards["session"] = config.Auth.Guards["web"]

		// JWT/API guard
		jwtAlgo := os.Getenv("AUTH_JWT_ALGO")
		if jwtAlgo == "" {
			jwtAlgo = "HS256"
		}

		config.Auth.Guards["api"] = auth.GuardConfig{
			Driver:   "jwt",
			Provider: "users",
			Options: map[string]interface{}{
				"jwt": auth.JWTConfig{
					Secret:           os.Getenv("AUTH_JWT_SECRET"),
					Algorithm:        jwtAlgo,
					TTL:              envIntOrDefault("AUTH_JWT_TTL", 60),
					RefreshTTL:       envIntOrDefault("AUTH_JWT_REFRESH_TTL", 20160),
					BlacklistEnabled: os.Getenv("AUTH_JWT_BLACKLIST_ENABLED") != "false",
				},
			},
		}
		config.Auth.Guards["jwt"] = config.Auth.Guards["api"]

		// Default user provider
		config.Auth.Providers["users"] = auth.ProviderConfig{
			Driver: "orm",
			Model:  envOrDefault("AUTH_MODEL", "User"),
		}
	}

	// CSRF
	config.CSRF = csrf.Config{
		TokenLifetime:     envDurationOrDefault("CSRF_TOKEN_LIFETIME", 24*time.Hour),
		HeaderName:        envOrDefault("CSRF_HEADER", "X-CSRF-Token"),
		FormField:         envOrDefault("CSRF_FORM_FIELD", "_token"),
		CookieName:        envOrDefault("CSRF_COOKIE_NAME", "csrf_token"),
		SessionCookieName: envOrDefault("CSRF_SESSION_COOKIE", config.Session.Name),
		SameSite:          parseSameSite(os.Getenv("CSRF_SAME_SITE")),
		Secure:            os.Getenv("CSRF_SECURE") != "false",
		HttpOnly:          envOrDefault("CSRF_HTTP_ONLY", "true") == "true",
		SingleUse:         os.Getenv("CSRF_SINGLE_USE") == "true",
		ErrorMessage:      envOrDefault("CSRF_ERROR_MESSAGE", "CSRF token validation failed. Please refresh and try again."),
	}

	// Cache
	redisTLS := os.Getenv("REDIS_TLS") == "true"
	config.Cache = CacheConfig{
		Driver:        envOrDefault("CACHE_DRIVER", "memory"),
		Prefix:        envOrDefault("CACHE_PREFIX", "velocity_cache"),
		Path:          os.Getenv("CACHE_PATH"),
		RedisHost:     envOrDefault("REDIS_HOST", "127.0.0.1"),
		RedisPort:     envIntOrDefault("REDIS_PORT", 6379),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		RedisDatabase: envIntOrDefault("REDIS_DATABASE", 0),
		RedisTLS:      redisTLS,
	}

	// Log
	config.Log = log.LogConfig{
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
	config.Crypto = crypto.Config{
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
		RedisTLS:      redisTLS,
		SigningKey:    os.Getenv("QUEUE_SIGNING_KEY"),
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
	config.Mail = mail.MailConfig{
		Driver:            envOrDefault("MAIL_DRIVER", "log"),
		FromAddress:       os.Getenv("MAIL_FROM_ADDRESS"),
		FromName:          os.Getenv("MAIL_FROM_NAME"),
		MaxAttachmentSize: envInt64OrDefault("MAIL_MAX_ATTACHMENT_SIZE", mail.DefaultMaxAttachmentSize),
		Mailgun: mail.MailgunConfig{
			Domain:            os.Getenv("MAIL_MAILGUN_DOMAIN"),
			Secret:            os.Getenv("MAIL_MAILGUN_SECRET"),
			Endpoint:          os.Getenv("MAIL_MAILGUN_ENDPOINT"),
			WebhookSigningKey: os.Getenv("MAIL_MAILGUN_WEBHOOK_SIGNING_KEY"),
		},
		Postmark: mail.PostmarkConfig{
			Token:         os.Getenv("MAIL_POSTMARK_TOKEN"),
			MessageStream: os.Getenv("MAIL_POSTMARK_MESSAGE_STREAM"),
		},
		Local: mail.LocalConfig{
			Host:         os.Getenv("MAIL_HOST"),
			Port:         os.Getenv("MAIL_PORT"),
			Username:     os.Getenv("MAIL_USERNAME"),
			Password:     os.Getenv("MAIL_PASSWORD"),
			Encryption:   os.Getenv("MAIL_ENCRYPTION"),
			SendmailPath: os.Getenv("MAIL_SENDMAIL_PATH"),
		},
	}

	// View / SSR
	config.View = view.Config{
		SSREnabled: os.Getenv("VIEW_SSR_ENABLED") == "true",
		SSRURL:     envOrDefault("VIEW_SSR_URL", "http://127.0.0.1:13714"),
		SSRTimeout: envDurationOrDefault("VIEW_SSR_TIMEOUT", 3*time.Second),
	}
	if except := os.Getenv("VIEW_SSR_EXCEPT"); except != "" {
		for _, p := range strings.Split(except, ",") {
			if p = strings.TrimSpace(p); p != "" {
				config.View.SSRExcept = append(config.View.SSRExcept, p)
			}
		}
	}

	// Server timeouts
	config.ReadTimeout = envDurationOrDefault("SERVER_READ_TIMEOUT", 30*time.Second)
	config.WriteTimeout = envDurationOrDefault("SERVER_WRITE_TIMEOUT", 30*time.Second)
	config.IdleTimeout = envDurationOrDefault("SERVER_IDLE_TIMEOUT", 120*time.Second)
	config.ReadHeaderTimeout = envDurationOrDefault("SERVER_READ_HEADER_TIMEOUT", 10*time.Second)

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

// envInt64OrDefault reads an int64 from the environment (raw bytes, no
// K/M/G suffix parsing - the codebase has no precedent for that today).
// Falls back to defaultValue on empty or unparseable input.
func envInt64OrDefault(key string, defaultValue int64) int64 {
	if value := os.Getenv(key); value != "" {
		if v, err := strconv.ParseInt(value, 10, 64); err == nil {
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
