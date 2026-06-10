package velocity

import (
	"errors"
	"fmt"
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

// ErrInvalidConfig is the sentinel for configuration validation failures.
// Sub-config Validate() methods wrap their own sentinels (e.g.
// auth.ErrInsecureSessionConfig, crypto.ErrInvalidKey) so callers can branch
// on the specific failure; the root Config.Validate() wraps this so a
// generic "is the config valid?" check needs only one errors.Is target.
var ErrInvalidConfig = errors.New("velocity: invalid configuration")

// Config holds all configuration for a Velocity application.
// It replaces the scattered os.Getenv() calls across packages.
type Config struct {
	// App
	Env   string // APP_ENV, empty when unset
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

	sessionSameSiteRaw string
	csrfSameSiteRaw    string

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

	// FileRoot is the absolute directory under which Context.File,
	// Context.Download, and Context.SaveFile may operate. Sourced from
	// FILE_ROOT; defaults to the process current working directory at
	// the time New() runs. The router enforces containment via symlink
	// resolution against this path on every request.
	FileRoot string

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
	Driver string // CACHE_DRIVER: memory, file, redis, database
	Prefix string // CACHE_PREFIX, default "velocity_cache"
	Path   string // CACHE_PATH (for file driver)
	// MemoryMaxEntries caps the memory driver's entry count.
	// CACHE_MEMORY_MAX_ENTRIES: 0 = default (1,000,000), negative = unlimited.
	MemoryMaxEntries int
	// MaxValueBytes caps the serialized size of a single cached value for
	// the memory and file drivers.
	// CACHE_MAX_VALUE_BYTES: 0 (default) = unlimited.
	MaxValueBytes int64
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
	Encrypt       bool   // QUEUE_ENCRYPT: encrypt job-state payload data at rest with the app encryptor
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

// WithSchedulerInProcess starts the scheduler loop inside the same process
// as the HTTP server. By default the scheduler is constructed but never
// run under Serve(); only the `vel schedule:work` CLI invokes Run(ctx).
// Use this for single-process deployments that don't want to manage a
// separate scheduler worker. Multi-process deployments (where a dedicated
// `vel schedule:work` worker runs alongside `vel serve`) should leave it
// off so jobs are not duplicated.
//
// The scheduler is started after Router.Freeze() and before
// http.Server.ListenAndServe; it is bound to the App's shutdownCtx so
// signal-driven shutdown stops the loop and Shutdown() drains in-flight
// jobs through the existing scheduler.Shutdown teardown.
func WithSchedulerInProcess() Option {
	return func(a *App) {
		a.runScheduler = true
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

	// Read APP_ENV through the canonical reader so Config.Env is the
	// normalised (lowercased + trimmed) value. Leave unset APP_ENV empty:
	// security gates key off IsTestingEnv / IsDevOrTestEnv, where "" is
	// neither, so unset deployments fail closed instead of inheriting
	// development relaxations. Storing the canonical form once at the
	// boundary keeps the exact-match consumers (scheduler.Job environment
	// filter, exceptions.Handler) seeing the same string.
	envValue := app.Env()
	config := Config{
		Env:      envValue,
		Debug:    envOrDefault("APP_DEBUG", "false") == "true",
		Port:     envOrDefault("APP_PORT", "4000"),
		Key:      os.Getenv("APP_KEY"),
		FileRoot: os.Getenv("FILE_ROOT"),
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

	sessionSameSiteRaw := os.Getenv("SESSION_SAME_SITE")

	// Session
	config.Session = auth.SessionConfig{
		Name:     envOrDefault("SESSION_NAME", "velocity_session"),
		Lifetime: envIntOrDefault("SESSION_LIFETIME", 120),
		Path:     envOrDefault("SESSION_PATH", "/"),
		Domain:   os.Getenv("SESSION_DOMAIN"),
		Secure:   os.Getenv("SESSION_SECURE") != "false",
		HttpOnly: envOrDefault("SESSION_HTTP_ONLY", "true") == "true",
		SameSite: parseSameSite(sessionSameSiteRaw),
	}
	config.sessionSameSiteRaw = sessionSameSiteRaw

	// Auth
	config.Auth = auth.Config{
		DefaultGuard:   os.Getenv("AUTH_GUARD"),
		Guards:         make(map[string]auth.GuardConfig),
		Providers:      make(map[string]auth.ProviderConfig),
		BcryptCost:     envIntOrDefault("HASH_BCRYPT_COST", 10),
		TrustedProxies: splitTrustedProxies(os.Getenv("AUTH_TRUSTED_PROXIES")),
		// AUTH_ATTEMPT_FLOOR is the wall-clock floor for guard Attempt
		// (H-09); zero falls back to auth.DefaultAttemptFloor (200ms).
		// Operators with high bcrypt cost (12+) should raise this so
		// the real-verify path still fits inside the budget; otherwise
		// the missing-user path padding to 200ms is trivially
		// distinguishable from the wrong-password path running real
		// bcrypt for 500ms+. Accepts time.ParseDuration syntax
		// (e.g. "500ms", "1s"). See F2.
		AttemptFloor: envDurationOrDefault("AUTH_ATTEMPT_FLOOR", 0),
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

	// CSRF: seed from csrf.DefaultConfig() so new fields added to the
	// package default (e.g. WriteXSRFCookie, XSRFCookieName,
	// MaxFormBodyBytes, Mode) propagate to velocity.New apps without a
	// matching edit here. Env overrides only the fields with explicit
	// knobs; everything else inherits the package default.
	csrfCfg := csrf.DefaultConfig()
	csrfCfg.TokenLifetime = envDurationOrDefault("CSRF_TOKEN_LIFETIME", csrfCfg.TokenLifetime)
	csrfCfg.HeaderName = envOrDefault("CSRF_HEADER", csrfCfg.HeaderName)
	csrfCfg.FormField = envOrDefault("CSRF_FORM_FIELD", csrfCfg.FormField)
	csrfCfg.CookieName = envOrDefault("CSRF_COOKIE_NAME", csrfCfg.CookieName)
	csrfCfg.SessionCookieName = envOrDefault("CSRF_SESSION_COOKIE", config.Session.Name)
	csrfSameSiteRaw := os.Getenv("CSRF_SAME_SITE")
	if csrfSameSiteRaw != "" {
		csrfCfg.SameSite = parseSameSite(csrfSameSiteRaw)
	}
	config.csrfSameSiteRaw = csrfSameSiteRaw
	csrfCfg.Secure = os.Getenv("CSRF_SECURE") != "false"
	csrfCfg.HttpOnly = envOrDefault("CSRF_HTTP_ONLY", "true") == "true"
	csrfCfg.SingleUse = os.Getenv("CSRF_SINGLE_USE") == "true"
	csrfCfg.ErrorMessage = envOrDefault("CSRF_ERROR_MESSAGE", csrfCfg.ErrorMessage)
	csrfCfg.WriteXSRFCookie = envOrDefault("CSRF_WRITE_XSRF_COOKIE", "true") == "true"
	csrfCfg.XSRFCookieName = envOrDefault("CSRF_XSRF_COOKIE_NAME", csrfCfg.XSRFCookieName)
	config.CSRF = *csrfCfg

	// Cache
	redisTLS := os.Getenv("REDIS_TLS") == "true"
	config.Cache = CacheConfig{
		Driver:           envOrDefault("CACHE_DRIVER", "memory"),
		Prefix:           envOrDefault("CACHE_PREFIX", "velocity_cache"),
		Path:             os.Getenv("CACHE_PATH"),
		MemoryMaxEntries: envIntOrDefault("CACHE_MEMORY_MAX_ENTRIES", 0),
		MaxValueBytes:    envInt64OrDefault("CACHE_MAX_VALUE_BYTES", 0),
		RedisHost:        envOrDefault("REDIS_HOST", "127.0.0.1"),
		RedisPort:        envIntOrDefault("REDIS_PORT", 6379),
		RedisPassword:    os.Getenv("REDIS_PASSWORD"),
		RedisDatabase:    envIntOrDefault("REDIS_DATABASE", 0),
		RedisTLS:         redisTLS,
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
		Encrypt:       os.Getenv("QUEUE_ENCRYPT") == "true",
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

// splitTrustedProxies parses a comma-separated list of IPs/CIDRs from
// the AUTH_TRUSTED_PROXIES env var. Empty input returns nil, which the
// auth layer treats as "no proxies trusted" (forwarded headers ignored,
// the secure default). Whitespace around each entry is trimmed.
func splitTrustedProxies(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

func parseSameSiteStrict(envName, value string) (http.SameSite, error) {
	switch value {
	case "":
		return http.SameSiteLaxMode, nil
	case "strict":
		return http.SameSiteStrictMode, nil
	case "lax":
		return http.SameSiteLaxMode, nil
	case "none":
		return http.SameSiteNoneMode, nil
	default:
		return http.SameSiteLaxMode, fmt.Errorf("%w: %s=%q is not one of strict|lax|none", ErrInvalidConfig, envName, value)
	}
}

// Validate checks the root Config for structural problems and delegates to
// per-subsystem Validate() methods that have no environment-aware
// relaxations. Called from New() before any resource is allocated so
// configuration typos (unknown driver names, malformed ports, negative
// timeouts) fail fast with a clear error.
//
// Session, CSRF, and Crypto validation are intentionally NOT chained here:
// those checks have dev-mode warning paths (Session/CSRF) or an
// env-conditional fallback (Crypto.Key empty => warn in dev) that New()
// applies after the logger is up. Calling them here would short-circuit
// the dev relaxations and break test fixtures that boot with permissive
// configs.
//
// Returns nil on success. On failure the returned error wraps
// ErrInvalidConfig so callers that want a generic "is this config OK?"
// branch can use errors.Is(err, ErrInvalidConfig).
func (c Config) Validate() error {
	if c.Port != "" {
		if _, err := strconv.Atoi(c.Port); err != nil {
			return fmt.Errorf("%w: APP_PORT=%q is not a valid port number", ErrInvalidConfig, c.Port)
		}
	}
	if c.ReadTimeout < 0 || c.WriteTimeout < 0 || c.IdleTimeout < 0 || c.ReadHeaderTimeout < 0 {
		return fmt.Errorf("%w: server timeouts must be non-negative", ErrInvalidConfig)
	}
	if _, err := parseSameSiteStrict("SESSION_SAME_SITE", c.sessionSameSiteRaw); err != nil {
		return err
	}
	if _, err := parseSameSiteStrict("CSRF_SAME_SITE", c.csrfSameSiteRaw); err != nil {
		return err
	}
	if err := c.DB.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if err := c.Cache.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if err := c.Queue.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if err := c.Storage.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	// View validation must run unconditionally at the root level: New()
	// only constructs view.NewEngine when RootTemplate != "" (see app.go),
	// so without this hook a VIEW_SSR_ENABLED=true + VIEW_SSR_TIMEOUT=0
	// config would bypass the fast-fail check entirely.
	if err := c.View.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return nil
}

// Validate checks the DBConfig for STRUCTURAL problems only. A zero-value
// DBConfig (no Connection) is valid: the framework treats it as "no database
// configured" and skips initDB.
//
// Validate deliberately does NOT check the driver name against an allowlist.
// Whether a named driver is actually wired is the orm driver registry's
// concern: initDB resolves the driver via orm.NewManager, and an unknown or
// unwired name surfaces as a typed driverregistry.NotFoundError at that point
// (which also lists the drivers that ARE registered). Consulting the registry
// here would couple config loading to import order and produce a worse error
// than the registry's own.
func (c DBConfig) Validate() error {
	if c.Connection == "" {
		return nil
	}
	if c.MaxIdleConns < 0 || c.MaxOpenConns < 0 {
		return fmt.Errorf("DB_MAX_IDLE_CONNS / DB_MAX_OPEN_CONNS must be non-negative")
	}
	if c.ConnMaxLifetime < 0 || c.SlowThreshold < 0 {
		return fmt.Errorf("DB_CONN_MAX_LIFETIME / DB_SLOW_QUERY_THRESHOLD must be non-negative")
	}
	return nil
}

// Validate checks the CacheConfig for STRUCTURAL problems only. An empty
// Driver is treated as "memory" by initCache; Validate accepts that.
//
// The driver name is not checked against an allowlist: an unknown or unwired
// driver is the cache driver registry's concern and surfaces as a typed
// NotFoundError at resolution time. Validate only enforces the per-driver
// structural requirements (file needs a path) and numeric sanity.
func (c CacheConfig) Validate() error {
	if c.Driver == "" {
		return nil
	}
	if c.Driver == "file" && c.Path == "" {
		// File driver requires CACHE_PATH so the store has a directory to
		// write into; an empty path resolves to the process CWD which
		// silently pollutes the deployment.
		return fmt.Errorf("CACHE_PATH is required when CACHE_DRIVER=file")
	}
	if c.RedisPort < 0 || c.RedisDatabase < 0 {
		return fmt.Errorf("REDIS_PORT / REDIS_DATABASE must be non-negative")
	}
	return nil
}

// Validate checks the QueueConfig for STRUCTURAL problems only. An empty
// Driver is accepted (initQueue defaults to memory). The driver name is not
// checked against an allowlist: an unknown or unwired driver surfaces as a
// typed NotFoundError when the queue driver registry resolves it. The config
// carries no other structurally-constrained queue fields, so a well-formed
// QueueConfig is always valid here.
func (c QueueConfig) Validate() error {
	return nil
}

// Validate checks the StorageConfig for STRUCTURAL problems only. Each disk
// must name a non-empty driver and satisfy that driver's required fields (s3
// needs a bucket), and the default disk (if set) must exist in the Disks map.
//
// The driver name is not checked against an allowlist: an unknown or unwired
// disk driver surfaces as a typed NotFoundError when the storage driver
// registry resolves it.
func (c StorageConfig) Validate() error {
	for name, disk := range c.Disks {
		if disk.Driver == "" {
			return fmt.Errorf("storage disk %q has empty driver", name)
		}
		if disk.Driver == "s3" && disk.Bucket == "" {
			return fmt.Errorf("storage disk %q uses s3 driver but Bucket is empty", name)
		}
	}
	if c.Default != "" {
		if _, ok := c.Disks[c.Default]; !ok && len(c.Disks) > 0 {
			return fmt.Errorf("STORAGE_DRIVER=%q does not match any configured disk", c.Default)
		}
	}
	return nil
}
