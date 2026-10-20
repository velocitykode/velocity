package velocity

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/storage"
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
	// Timezone is the application's PRESENTATION timezone (APP_TIMEZONE,
	// default "UTC"): an IANA location name applied at bootstrap to
	// time.Local and to scheduler cron evaluation. Persistence never
	// reads it - ORM/queue storage is unconditionally UTC.
	Timezone string // APP_TIMEZONE, default "UTC"

	// Database. A zero Driver means no database.
	DB orm.ManagerConfig

	// Auth
	Auth auth.Config

	// Cache
	Cache cache.Config

	// Log
	Log log.LogConfig

	// Queue
	Queue queue.QueueConfig

	// Storage
	Storage storage.Config

	// CSRF
	CSRF csrf.Config

	// Session
	Session auth.SessionConfig

	sessionSameSiteRaw string

	// View configures the view engine. New builds the engine when any of
	// RootTemplate, ErrorPage (VIEW_ERROR_PAGE) or SSREnabled
	// (VIEW_SSR_ENABLED) is set; an empty RootTemplate then means the
	// built-in root template. With none of the three set, New builds no
	// engine.
	View view.Config

	// Crypto
	Crypto crypto.Config

	// Mail
	Mail mail.MailConfig

	// Maintenance configures the maintenance gate and the down/up
	// commands. New passes it to App.Middleware as m.Maintenance(), with
	// AppKey defaulted to Key.
	Maintenance contract.MaintenanceConfig

	// GRPC is the configuration for grpc.NewServer and grpc.NewGateway.
	// New does not build a gRPC server; an app passes this to them.
	GRPC contract.GRPCConfig

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

// WithListener makes Serve serve HTTP on a caller-supplied net.Listener
// instead of binding the configured port (WithPort, PORT); the start line
// reports the listener's address. Once Serve serves on it the listener is
// the app's, closed when the app shuts down as it would close its own; a
// Serve that fails before serving (a bootstrap error) leaves it open.
func WithListener(lis net.Listener) Option {
	return func(a *App) {
		a.listener = lis
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

// WithFailedEventHook installs fn as the hook every failed framework event
// dispatch is handed to: a listener that returned an error or panicked, a
// dispatcher that panicked (as a recovered panic, contract.RecoveredPanic),
// or an event the router or the ORM dropped before any listener saw it. fn
// receives the failure and the event as it was dispatched, whatever its
// type (app-defined events included). Each failure is also counted
// (App.FailedEventCount) and the first failure of each event name is
// logged at warn level through the app logger, with or without a hook.
//
// Per failed delivery, the failure is counted once and fn is called once,
// however many of its listeners failed, detached deliveries included
// (no-queue DispatchAsync or DispatchAfter, a debounced or coalesced
// dispatch); the error handler reports each failed listener once. The
// listeners of a dispatch that wait for a transaction's commit
// (ShouldDispatchAfterCommit) are a delivery of their own, run at the
// commit: counted once and handed to fn once however many of them fail,
// while the transaction returns their failure.
//
// The count is exact; for a statement event the ORM drops under overload,
// fn is best-effort. The ORM counts the drop inside the statement and
// hands the drop line and fn to a goroutine of its own, so neither holds
// the statement's database connection; when fn falls so far behind that
// its backlog is full, a further drop is still counted but its line and fn
// call are skipped rather than stall the query. Compare FailedEventCount
// with the drops fn saw to tell. Bounded storage, queries that never wait
// and a call to fn for every drop cannot all hold at once.
//
// fn runs on the goroutine that saw the failure (a request, a job, a
// background pump), so it must be quick and safe for concurrent use. A
// panic in fn is recovered and counted as one more failure; fn is not
// called for it. A failure fn causes on its own goroutine while it runs
// (a cache read whose listener fails, say) is counted and logged, and fn is
// not called for it either. fn may query the database, but must not flush
// or shut down the ORM (Manager.FlushQueryEvents, Manager.Shutdown) when
// handed a dropped statement event: those return
// orm.ErrQueryEventsFlushFromPump there. A nil fn installs no hook.
func WithFailedEventHook(fn func(err error, event any)) Option {
	return func(a *App) {
		a.eventFailures.SetHook(fn)
	}
}

// WithFakeEvents replaces the event dispatcher with a fake that records
// dispatched events without executing listeners. Assert through eventstest,
// which fails the test directly:
//
//	fake := events.NewFakeDispatcher()
//	app, _ := velocitytest.NewApp(velocity.WithFakeEvents(fake))
//	// ... trigger actions ...
//	eventstest.AssertDispatched(t, fake, router.RequestHandled{}, nil)
func WithFakeEvents(fake *events.FakeDispatcher) Option {
	return func(a *App) {
		a.Services.Events = fake
	}
}

// WithFakeQueue replaces the queue driver with one supplied by the test
// (typically queuetest.NewFakeQueue()) so pushed jobs are recorded for
// assertions instead of dispatched to a real broker. Mirrors WithFakeEvents.
func WithFakeQueue(fake queue.Driver) Option {
	return func(a *App) {
		a.Services.Queue = fake
	}
}

// WithFakeMail replaces the mailer with one supplied by the test (typically
// mailtest.NewFakeMailer()) so sent messages are recorded for assertions
// instead of dispatched to a real transport. Mirrors WithFakeQueue. The
// notification manager is built from this mailer, so notifications routed via
// mail are recorded too.
func WithFakeMail(fake mail.Mailer) Option {
	return func(a *App) {
		a.Services.Mail = fake
	}
}

// WithModules appends modules to the application.
// Modules are initialized and started in the order they are given.
func WithModules(modules ...app.Module) Option {
	return func(a *App) {
		a.modules = append(a.modules, modules...)
	}
}

// WithSchedulerInProcess starts the scheduler loop inside the same process
// as the HTTP server. By default the scheduler is constructed but never
// run under Serve(); only the `vel schedule work` CLI invokes Run(ctx).
// Use this for single-process deployments that don't want to manage a
// separate scheduler worker. Multi-process deployments (where a dedicated
// `vel schedule work` worker runs alongside `vel serve`) should leave it
// off so jobs are not duplicated.
//
// The scheduler is started after Router.Freeze() and before
// http.Server.ListenAndServe. The loop runs against context.Background()
// rather than the App's shutdownCtx; teardown happens via App.Shutdown ->
// Scheduler.Shutdown(ctx), which closes the stop channel and waits for
// in-flight jobs to drain.
func WithSchedulerInProcess() Option {
	return func(a *App) {
		a.runScheduler = true
	}
}

// ConfigFromEnv loads configuration from environment variables and .env file.
func ConfigFromEnv() Config {
	// Load .env file if present; warn if it exists but fails to parse.
	// This runs before any app logger exists, so the warning goes through
	// the framework's standalone fallback logger (standard error).
	if err := godotenv.Load(); err != nil {
		if _, statErr := os.Stat(".env"); statErr == nil {
			fallbacklog.Logger{}.Warn("velocity: .env file exists but failed to parse", "error", err)
		}
	}

	// Read APP_ENV through the canonical reader so Config.Env is the
	// normalised (lowercased + trimmed) value. Leave unset APP_ENV empty:
	// security gates key off IsTestingEnv / IsDevOrTestEnv, where "" is
	// neither, so unset deployments fail closed instead of inheriting
	// development relaxations. Storing the canonical form once at the
	// boundary keeps the exact-match consumers (scheduler.Job environment
	// filter, problem.Handler) seeing the same string.
	envValue := app.Env()
	config := Config{
		Env:      envValue,
		Debug:    envOrDefault("APP_DEBUG", "false") == "true",
		Port:     envOrDefault("APP_PORT", "4000"),
		Key:      os.Getenv("APP_KEY"),
		FileRoot: os.Getenv("FILE_ROOT"),
		Timezone: envOrDefault("APP_TIMEZONE", "UTC"),
	}

	// Database
	dbConnection := os.Getenv("DB_CONNECTION")
	config.DB = orm.ManagerConfig{
		Driver:          dbConnection,
		Host:            envOrDefault("DB_HOST", "127.0.0.1"),
		Port:            envOrDefault("DB_PORT", defaultPortForDriver(dbConnection)),
		Database:        os.Getenv("DB_DATABASE"),
		Username:        os.Getenv("DB_USERNAME"),
		Password:        os.Getenv("DB_PASSWORD"),
		Charset:         os.Getenv("DB_CHARSET"),
		SSLMode:         os.Getenv("DB_SSL_MODE"),
		TLS:             os.Getenv("DB_MYSQL_TLS"),
		TimeZone:        os.Getenv("DB_TIMEZONE"),
		MaxIdleConns:    envIntOrDefault("DB_MAX_IDLE_CONNS", 10),
		MaxOpenConns:    envIntOrDefault("DB_MAX_OPEN_CONNS", 100),
		ConnMaxLifetime: time.Duration(envIntOrDefault("DB_CONN_MAX_LIFETIME", 3600)) * time.Second,
		LogQueries:      os.Getenv("DB_LOG_QUERIES") == "true",
		SlowThreshold:   envDurationOrDefault("DB_SLOW_QUERY_THRESHOLD", 0),
	}

	sessionSameSiteRaw := os.Getenv("SESSION_SAME_SITE")

	// Session
	config.Session = auth.SessionConfig{
		Name:         envOrDefault("SESSION_NAME", "velocity_session"),
		IdleLifetime: envIntOrDefault("SESSION_IDLE_LIFETIME", 120),
		// Idle timeout in minutes: each request slides it, a session idle
		// for longer ends. Absolute cap in minutes from sign-in, which
		// activity never extends: 0 (the default) applies the framework
		// default of 30 days; negative explicitly disables the cap
		// (unbounded sessions).
		AbsoluteLifetime: envIntOrDefault("SESSION_ABSOLUTE_LIFETIME", 0),
		// Remember-me credential lifetime in minutes, independent of
		// the session lifetime: 0 (the default) is 30 days.
		RememberLifetime: envIntOrDefault("SESSION_REMEMBER_LIFETIME", 0),
		Path:             envOrDefault("SESSION_PATH", "/"),
		Domain:           os.Getenv("SESSION_DOMAIN"),
		Secure:           os.Getenv("SESSION_SECURE") != "false",
		HttpOnly:         envOrDefault("SESSION_HTTP_ONLY", "true") == "true",
		SameSite:         parseSameSite(sessionSameSiteRaw),
		// Where the session lives: "cookie" (the default) in the
		// encrypted cookie, "server" in the session's server record
		// with only the id in the cookie (needs a cache backend).
		Store: envOrDefault("SESSION_STORE", auth.SessionStoreCookie),
	}
	config.sessionSameSiteRaw = sessionSameSiteRaw

	// Auth
	config.Auth = auth.Config{
		DefaultScheme:  os.Getenv("AUTH_SCHEME"),
		Schemes:        make(map[string]auth.SchemeConfig),
		BcryptCost:     envIntOrDefault("HASH_BCRYPT_COST", 10),
		TrustedProxies: splitTrustedProxies(os.Getenv("AUTH_TRUSTED_PROXIES")),
		// AUTH_ATTEMPT_FLOOR is the wall-clock floor for scheme Attempt
		// (H-09); zero falls back to auth.DefaultAttemptFloor (200ms).
		// Operators with high bcrypt cost (12+) should raise this so
		// the real-verify path still fits inside the budget; otherwise
		// the missing-user path padding to 200ms is trivially
		// distinguishable from the wrong-password path running real
		// bcrypt for 500ms+. Accepts time.ParseDuration syntax
		// (e.g. "500ms", "1s"). See F2.
		AttemptFloor: envDurationOrDefault("AUTH_ATTEMPT_FLOOR", 0),
		// Login throttler. An unset or invalid value is zero, which the
		// throttler reads as its default (see auth.LoginThrottleConfig).
		LoginThrottle: auth.LoginThrottleConfig{
			MaxAttempts:              envPositiveInt("AUTH_LOGIN_MAX_ATTEMPTS"),
			MaxAttemptsPerIdentifier: envPositiveInt("AUTH_LOGIN_MAX_ATTEMPTS_PER_IDENTIFIER"),
			MaxAttemptsPerIP:         envPositiveInt("AUTH_LOGIN_MAX_ATTEMPTS_PER_IP"),
			Decay:                    envLoginDecay("AUTH_LOGIN_DECAY"),
			IdentifierDelay:          envLoginDuration("AUTH_LOGIN_IDENTIFIER_DELAY", true),
			IdentifierDelayMax:       envLoginDuration("AUTH_LOGIN_IDENTIFIER_DELAY_MAX", false),
		},
	}

	// Configure schemes if AUTH_SCHEME is set
	if config.Auth.DefaultScheme != "" {
		// Session/web scheme
		config.Auth.Schemes["web"] = auth.SchemeConfig{
			Driver: "session",
			Options: map[string]interface{}{
				"session": config.Session,
			},
		}
		config.Auth.Schemes["session"] = config.Auth.Schemes["web"]

		// JWT/API scheme
		jwtAlgo := os.Getenv("AUTH_JWT_ALGO")
		if jwtAlgo == "" {
			jwtAlgo = "HS256"
		}

		config.Auth.Schemes["api"] = auth.SchemeConfig{
			Driver: "jwt",
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
		config.Auth.Schemes["jwt"] = config.Auth.Schemes["api"]
	}

	// CSRF: seed from csrf.DefaultConfig() so new fields added to the
	// package default (e.g. WriteXSRFCookie, XSRFCookieName,
	// MaxFormBodyBytes, Mode) propagate to velocity.New apps without a
	// matching edit here. Env overrides only the fields with explicit
	// knobs; everything else inherits the package default.
	csrfCfg := csrf.DefaultConfig()
	csrfCfg.HeaderName = envOrDefault("CSRF_HEADER", csrfCfg.HeaderName)
	csrfCfg.FormField = envOrDefault("CSRF_FORM_FIELD", csrfCfg.FormField)
	csrfCfg.SessionCookieName = envOrDefault("CSRF_SESSION_COOKIE", config.Session.Name)
	csrfCfg.SingleUse = os.Getenv("CSRF_SINGLE_USE") == "true"
	csrfCfg.ErrorMessage = envOrDefault("CSRF_ERROR_MESSAGE", csrfCfg.ErrorMessage)
	csrfCfg.WriteXSRFCookie = envOrDefault("CSRF_WRITE_XSRF_COOKIE", "true") == "true"
	csrfCfg.XSRFCookieName = envOrDefault("CSRF_XSRF_COOKIE_NAME", csrfCfg.XSRFCookieName)
	config.CSRF = *csrfCfg

	// Cache
	redisTLS := os.Getenv("REDIS_TLS") == "true"
	// One store, "default", on CACHE_DRIVER. Each driver reads only its
	// own fields (memory: MaxEntries; file: Path; redis: the connection;
	// memory and file: MaxValueBytes) and ignores the rest.
	config.Cache = cache.SingleStoreConfig(envOrDefault("CACHE_PREFIX", "velocity_cache"), cache.StoreConfig{
		Driver:        cacheDriverFromEnv(os.Getenv("CACHE_DRIVER")),
		Path:          os.Getenv("CACHE_PATH"),
		MaxEntries:    envIntOrDefault("CACHE_MEMORY_MAX_ENTRIES", 0),
		MaxValueBytes: envInt64OrDefault("CACHE_MAX_VALUE_BYTES", 0),
		Host:          envOrDefault("REDIS_HOST", "127.0.0.1"),
		Port:          envIntOrDefault("REDIS_PORT", 6379),
		Password:      os.Getenv("REDIS_PASSWORD"),
		Database:      envIntOrDefault("REDIS_DATABASE", 0),
		TLS:           redisTLS,
	})

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
	// Redaction (see log.WrapWithRedactors): the "stack" driver passes
	// this map to every child, so the flags cover every channel of the
	// app logger.
	if strings.EqualFold(os.Getenv("LOG_REDACT"), "true") {
		config.Log.Config["redact"] = true
	}
	if strings.EqualFold(os.Getenv("LOG_REDACT_EMAILS"), "true") {
		config.Log.Config["redact_emails"] = true
	}
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
		Key:       cryptoKey,
		Cipher:    envOrDefault("CRYPTO_CIPHER", "AES-256-GCM"),
		DisableV0: os.Getenv("CRYPTO_DISABLE_V0") == "true",
		Debug:     os.Getenv("CRYPTO_DEBUG") == "true",
	}
	if oldKeys := os.Getenv("CRYPTO_OLD_KEYS"); oldKeys != "" {
		config.Crypto.PreviousKeys = strings.Split(oldKeys, ",")
	}

	// Queue
	config.Queue = queue.QueueConfig{
		Driver: envOrDefault("QUEUE_DRIVER", "memory"),
		Redis: queue.RedisConfig{
			Host:     envOrDefault("QUEUE_REDIS_HOST", "localhost"),
			Port:     envOrDefault("QUEUE_REDIS_PORT", "6379"),
			Password: os.Getenv("QUEUE_REDIS_PASSWORD"),
			DB:       envOrDefault("QUEUE_REDIS_DB", "0"),
			TLS:      redisTLS,
		},
		SigningKey: os.Getenv("QUEUE_SIGNING_KEY"),
		// final: do not rename. QUEUE_ACCEPT_UNSIGNED is the 1.0 surface
		// name for the queue payload-signing opt-out. Common truthy
		// spellings are accepted so a typo does not silently keep the
		// guard on when the operator meant to opt out.
		AcceptUnsigned: envTruthy("QUEUE_ACCEPT_UNSIGNED"),
		Encrypt:        os.Getenv("QUEUE_ENCRYPT") == "true",
	}

	// Storage
	storageDefault := envOrDefault("STORAGE_DRIVER", "local")
	config.Storage = storage.Config{
		Default: storageDefault,
		Disks:   make(map[string]storage.DiskConfig),
	}
	// Always configure a local disk
	config.Storage.Disks["local"] = storage.DiskConfig{
		Driver: "local",
		Root:   envOrDefault("FILESYSTEM_LOCAL_ROOT", "./storage/app"),
	}
	// Configure S3 disk if credentials are present
	if s3Bucket := os.Getenv("AWS_BUCKET"); s3Bucket != "" {
		config.Storage.Disks["s3"] = storage.DiskConfig{
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

	// View / SSR. VIEW_ERROR_PAGE or VIEW_SSR_ENABLED=true alone makes New
	// build the view engine (with the built-in root template); the other
	// VIEW_SSR_* variables tune SSR once it is enabled.
	config.View = view.Config{
		SSREnabled: os.Getenv("VIEW_SSR_ENABLED") == "true",
		SSRURL:     envOrDefault("VIEW_SSR_URL", "http://127.0.0.1:13714"),
		SSRTimeout: envDurationOrDefault("VIEW_SSR_TIMEOUT", 3*time.Second),
		ErrorPage:  strings.TrimSpace(os.Getenv("VIEW_ERROR_PAGE")),
	}
	if except := os.Getenv("VIEW_SSR_EXCEPT"); except != "" {
		for _, p := range strings.Split(except, ",") {
			if p = strings.TrimSpace(p); p != "" {
				config.View.SSRExcept = append(config.View.SSRExcept, p)
			}
		}
	}

	// Maintenance. An unset VELOCITY_MAINTENANCE_EXCLUDE_PATHS adds no
	// paths; entries are trimmed and empty ones skipped by the gate.
	config.Maintenance = contract.MaintenanceConfig{
		Root:   os.Getenv("VELOCITY_MAINTENANCE_ROOT"),
		AppKey: config.Key,
	}
	if raw := os.Getenv("VELOCITY_MAINTENANCE_EXCLUDE_PATHS"); raw != "" {
		config.Maintenance.ExcludePaths = strings.Split(raw, ",")
	}

	// gRPC. Environment is the same normalised APP_ENV as Config.Env.
	config.GRPC = contract.GRPCConfig{
		ServerPort:       os.Getenv("GRPC_PORT"),
		EnableReflection: os.Getenv("GRPC_REFLECTION") == "true",
		GatewayPort:      os.Getenv("GATEWAY_PORT"),
		GRPCEndpoint:     os.Getenv("GRPC_ENDPOINT"),
		Environment:      config.Env,
		// final: do not rename. GRPC_INSECURE is the 1.0 surface name for
		// the production TLS opt-out.
		Insecure: os.Getenv("GRPC_INSECURE") == "true",
	}
	config.GRPC.MaxRecvMsgSize = envGRPCMsgSize("GRPC_MAX_RECV_SIZE", &config.GRPC.Warnings)
	config.GRPC.MaxSendMsgSize = envGRPCMsgSize("GRPC_MAX_SEND_SIZE", &config.GRPC.Warnings)

	// Server timeouts
	config.ReadTimeout = envDurationOrDefault("SERVER_READ_TIMEOUT", 30*time.Second)
	config.WriteTimeout = envDurationOrDefault("SERVER_WRITE_TIMEOUT", 30*time.Second)
	config.IdleTimeout = envDurationOrDefault("SERVER_IDLE_TIMEOUT", 120*time.Second)
	config.ReadHeaderTimeout = envDurationOrDefault("SERVER_READ_HEADER_TIMEOUT", 10*time.Second)

	return config
}

// Helper functions

// envTruthy reports whether key holds 1, true, yes or on (any case,
// surrounding space ignored).
func envTruthy(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// envPositiveInt reads a positive integer; unset, malformed, zero or
// negative is 0.
func envPositiveInt(key string) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// grpcDefaultMaxMsgSize is the gRPC message-size default the grpc package
// applies to a zero size; quoted in the warnings below.
const grpcDefaultMaxMsgSize = 4 * 1024 * 1024

// envGRPCMsgSize reads a gRPC message-size limit in bytes. Unset is 0 (the
// default). A non-integer or zero value is 0 with a warning naming the key;
// any other integer is passed on for the grpc package to range-check
// (negative falls back, above 1 GiB clamps, each with its own warning).
func envGRPCMsgSize(key string, warnings *[]string) int {
	raw := os.Getenv(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	switch {
	case err != nil:
		*warnings = append(*warnings, fmt.Sprintf("velocity/grpc: %s=%q is not an integer; using the %d default", key, raw, grpcDefaultMaxMsgSize))
		return 0
	case n == 0:
		*warnings = append(*warnings, fmt.Sprintf("velocity/grpc: %s=0 is not positive; using the %d default", key, grpcDefaultMaxMsgSize))
	}
	return n
}

// cacheDriverFromEnv maps CACHE_DRIVER onto the store driver: exactly
// "file" or "redis" select that driver; anything else, unset or unknown,
// is the memory driver.
func cacheDriverFromEnv(raw string) string {
	switch raw {
	case cache.DriverFile, cache.DriverRedis:
		return raw
	}
	return cache.DriverMemory
}

// envLoginDecay reads AUTH_LOGIN_DECAY as a Go duration or bare seconds,
// untrimmed. Unset, malformed, zero or negative is 0 (the default).
func envLoginDecay(key string) time.Duration {
	raw := os.Getenv(key)
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

// envLoginDuration reads a login-throttle duration as a Go duration
// ("2s", "500ms") or bare seconds. Unset, malformed or negative is 0 (the
// default). An explicit zero is 0 too, unless zeroDisables: then it is -1,
// the value auth.LoginThrottleConfig reads as "disabled".
func envLoginDuration(key string, zeroDisables bool) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		seconds, aerr := strconv.Atoi(raw)
		if aerr != nil {
			return 0
		}
		d = time.Duration(seconds) * time.Second
	}
	switch {
	case d < 0:
		return 0
	case d == 0 && zeroDisables:
		return -1
	}
	return d
}

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
		return http.SameSiteLaxMode, errchain.Errorf("%w: %s=%q is not one of strict|lax|none", ErrInvalidConfig, envName, value)
	}
}

// Validate checks the root Config for structural problems and delegates to
// per-subsystem Validate() methods that have no environment-aware
// relaxations. Called from New() before any resource is allocated so
// configuration typos (unknown driver names, malformed ports, negative
// timeouts) fail fast with a clear error.
//
// The session lifetimes (SessionConfig.ValidateLifetimes) are checked here:
// an out-of-range lifetime is a value error with no dev relaxation. The
// session cookie security attributes, CSRF, and Crypto validation are
// intentionally NOT chained here: those checks have dev-mode warning paths
// (session cookie/CSRF) or an env-conditional fallback (Crypto.Key empty =>
// warn in dev) that New() applies after the logger is up. Calling them here
// would short-circuit the dev relaxations and break test fixtures that boot
// with permissive configs.
//
// Returns nil on success. On failure the returned error wraps
// ErrInvalidConfig so callers that want a generic "is this config OK?"
// branch can use errors.Is(err, ErrInvalidConfig).
func (c Config) Validate() error {
	if c.Port != "" {
		if _, err := strconv.Atoi(c.Port); err != nil {
			return errchain.Errorf("%w: APP_PORT=%q is not a valid port number", ErrInvalidConfig, c.Port)
		}
	}
	if c.ReadTimeout < 0 || c.WriteTimeout < 0 || c.IdleTimeout < 0 || c.ReadHeaderTimeout < 0 {
		return errchain.Errorf("%w: server timeouts must be non-negative", ErrInvalidConfig)
	}
	if _, err := parseSameSiteStrict("SESSION_SAME_SITE", c.sessionSameSiteRaw); err != nil {
		return err
	}
	if err := c.Session.ValidateLifetimes(); err != nil {
		return errchain.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	switch c.Session.Store {
	case "", auth.SessionStoreCookie, auth.SessionStoreServer:
	default:
		return errchain.Errorf("%w: SESSION_STORE=%q is not one of %q, %q", ErrInvalidConfig, c.Session.Store, auth.SessionStoreCookie, auth.SessionStoreServer)
	}
	if err := c.DB.Validate(); err != nil {
		return errchain.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if err := c.Cache.Validate(); err != nil {
		return errchain.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if err := c.Storage.Validate(); err != nil {
		return errchain.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	// Queue is not validated here: queue.QueueConfig.Validate checks the
	// *sql.DB that New wires in later, and NewQueue runs it then.
	// View validation runs at the root level so a bad view config fails
	// here whether or not New builds the engine (see buildsViewEngine):
	// VIEW_SSR_ENABLED=true with VIEW_SSR_TIMEOUT=0 is rejected before
	// anything is constructed.
	if err := c.View.Validate(); err != nil {
		return errchain.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return nil
}

// buildsViewEngine reports whether New builds the view engine for cfg:
// any of RootTemplate, ErrorPage or SSREnabled is set. view.NewEngine
// fills an empty RootTemplate with the built-in template.
func buildsViewEngine(cfg view.Config) bool {
	return cfg.RootTemplate != "" || cfg.ErrorPage != "" || cfg.SSREnabled
}
