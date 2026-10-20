package velocity

import (
	"errors"
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
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/grpc/grpcconfig"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/maintenance"
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

// Config holds all configuration for a Velocity application. Each field is
// the owning package's own config type; ConfigFromEnv is the only code in
// the framework that reads the environment, and New checks the result with
// Validate before anything is built.
type Config struct {
	// Auth configures authentication: schemes, password hashing, login
	// throttling and the session (AUTH_*, HASH_*, SESSION_*).
	Auth auth.Config

	// CSRF configures CSRF protection (CSRF_*).
	CSRF csrf.Config

	// Cache configures the cache store (CACHE_*, REDIS_*).
	Cache cache.Config

	// Crypto configures the encryptor (CRYPTO_*).
	Crypto crypto.Config

	// DB configures the database (DB_*). A zero Driver means no database.
	DB orm.Config

	// Debug enables debug mode (APP_DEBUG, default false).
	Debug bool

	// Env is the application environment (APP_ENV, empty when unset).
	Env string

	// FileRoot is the absolute directory under which Context.File,
	// Context.Download and Context.SaveFile may operate; the router
	// enforces containment via symlink resolution on every request
	// (FILE_ROOT, default the working directory when New runs).
	FileRoot string

	// GRPC configures grpc.NewServer and grpc.NewGateway (GRPC_*,
	// GATEWAY_*). New does not build a gRPC server; an app passes this to
	// them.
	GRPC grpcconfig.Config

	// IdleTimeout is the HTTP server idle timeout (SERVER_IDLE_TIMEOUT,
	// default 120s).
	IdleTimeout time.Duration

	// Key is the application key used for crypto (APP_KEY).
	Key string

	// Log configures the application logger (LOG_*).
	Log log.Config

	// Mail configures the mailer (MAIL_*).
	Mail mail.Config

	// Maintenance configures the maintenance gate and the down/up
	// commands (VELOCITY_MAINTENANCE_*). New passes it to App.Middleware
	// as m.Maintenance(), with AppKey defaulted to Key.
	Maintenance maintenance.Config

	// Port is the HTTP listen port (APP_PORT, default "4000").
	Port string

	// Queue configures the job queue (QUEUE_*, REDIS_*).
	Queue queue.Config

	// ReadHeaderTimeout is the HTTP server read-header timeout
	// (SERVER_READ_HEADER_TIMEOUT, default 10s).
	ReadHeaderTimeout time.Duration

	// ReadTimeout is the HTTP server read timeout (SERVER_READ_TIMEOUT,
	// default 30s).
	ReadTimeout time.Duration

	// Storage configures the storage disks (FILESYSTEM_*, STORAGE_*,
	// AWS_*).
	Storage storage.Config

	// Timezone is the presentation timezone, an IANA location name
	// applied at bootstrap to time.Local and to scheduler cron evaluation;
	// persistence never reads it, ORM and queue storage are always UTC
	// (APP_TIMEZONE, default "UTC").
	Timezone string

	// View configures the view engine (VIEW_*). New builds the engine
	// when any of RootTemplate, ErrorPage or SSREnabled is set; an empty
	// RootTemplate then means the built-in root template. With none of the
	// three set, New builds no engine.
	View view.Config

	// WriteTimeout is the HTTP server write timeout (SERVER_WRITE_TIMEOUT,
	// default 30s).
	WriteTimeout time.Duration

	// envErrs holds the env values ConfigFromEnv could not parse, one error
	// per key; Validate returns them with the structural checks.
	envErrs []error
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
	r := &envReader{}
	// Load .env file if present; warn if it exists but fails to parse.
	// This runs before any app logger exists, so the warning goes through
	// the framework's standalone fallback logger (standard error).
	if err := godotenv.Load(); err != nil {
		if _, statErr := os.Stat(".env"); statErr == nil {
			fallbacklog.Logger{}.Warn("velocity: .env file exists but failed to parse", "error", err)
		}
	}

	// Config.Env is the normalised (lowercased, trimmed) APP_ENV. Unset stays
	// empty, which every gate classifies as production, so a deploy that
	// forgets APP_ENV fails closed instead of inheriting development
	// relaxations. Storing the canonical form once at the
	// boundary keeps the exact-match consumers (scheduler.Job environment
	// filter, problem.Handler) seeing the same string.
	envValue := strings.ToLower(r.raw(app.EnvVar))
	config := Config{
		Env:      envValue,
		Debug:    r.bool("APP_DEBUG", false),
		Port:     r.str("APP_PORT", "4000"),
		Key:      os.Getenv("APP_KEY"),
		FileRoot: os.Getenv("FILE_ROOT"),
		Timezone: r.str("APP_TIMEZONE", "UTC"),
	}

	// Database
	dbConnection := r.driver("DB_CONNECTION", "")
	config.DB = orm.Config{
		Driver:          dbConnection,
		Host:            r.str("DB_HOST", "127.0.0.1"),
		Port:            r.port("DB_PORT", defaultPortForDriver(dbConnection)),
		Database:        os.Getenv("DB_DATABASE"),
		Username:        os.Getenv("DB_USERNAME"),
		Password:        os.Getenv("DB_PASSWORD"),
		Charset:         os.Getenv("DB_CHARSET"),
		SSLMode:         os.Getenv("DB_SSL_MODE"),
		TLS:             os.Getenv("DB_MYSQL_TLS"),
		TimeZone:        os.Getenv("DB_TIMEZONE"),
		MaxIdleConns:    r.int("DB_MAX_IDLE_CONNS", 10),
		MaxOpenConns:    r.int("DB_MAX_OPEN_CONNS", 100),
		ConnMaxLifetime: time.Duration(r.int("DB_CONN_MAX_LIFETIME", 3600)) * time.Second,
		LogQueries:      r.bool("DB_LOG_QUERIES", false),
		SlowThreshold:   r.duration("DB_SLOW_QUERY_THRESHOLD", 0),
	}

	// Session
	sessionCfg := auth.SessionConfig{
		Name:         r.str("SESSION_NAME", "velocity_session"),
		IdleLifetime: r.int("SESSION_IDLE_LIFETIME", 120),
		// Idle timeout in minutes: each request slides it, a session idle
		// for longer ends. Absolute cap in minutes from sign-in, which
		// activity never extends: 0 (the default) applies the framework
		// default of 30 days; negative explicitly disables the cap
		// (unbounded sessions).
		AbsoluteLifetime: r.int("SESSION_ABSOLUTE_LIFETIME", 0),
		// Remember-me credential lifetime in minutes, independent of
		// the session lifetime: 0 (the default) is 30 days.
		RememberLifetime: r.int("SESSION_REMEMBER_LIFETIME", 0),
		Path:             r.str("SESSION_PATH", "/"),
		Domain:           os.Getenv("SESSION_DOMAIN"),
		Secure:           r.bool("SESSION_SECURE", true),
		HttpOnly:         r.bool("SESSION_HTTP_ONLY", true),
		SameSite:         r.sameSite("SESSION_SAME_SITE"),
		// Where the session lives: "cookie" (the default) in the
		// encrypted cookie, "server" in the session's server record
		// with only the id in the cookie (needs a cache backend).
		Store: r.str("SESSION_STORE", auth.SessionStoreCookie),
	}

	// Auth
	config.Auth = auth.Config{
		DefaultScheme:  os.Getenv("AUTH_SCHEME"),
		Schemes:        make(map[string]auth.SchemeConfig),
		BcryptCost:     r.int("HASH_BCRYPT_COST", 10),
		TrustedProxies: r.list("AUTH_TRUSTED_PROXIES"),
		Session:        sessionCfg,
		// AUTH_ATTEMPT_FLOOR is the wall-clock floor for scheme Attempt
		// (H-09); zero falls back to auth.DefaultAttemptFloor (200ms).
		// Operators with high bcrypt cost (12+) should raise this so
		// the real-verify path still fits inside the budget; otherwise
		// the missing-user path padding to 200ms is trivially
		// distinguishable from the wrong-password path running real
		// bcrypt for 500ms+. Accepts time.ParseDuration syntax
		// (e.g. "500ms", "1s"). See F2.
		AttemptFloor: r.duration("AUTH_ATTEMPT_FLOOR", 0),
		// Login throttler. An unset or invalid value is zero, which the
		// throttler reads as its default (see auth.LoginThrottleConfig).
		LoginThrottle: auth.LoginThrottleConfig{
			MaxAttempts:              r.positiveInt("AUTH_LOGIN_MAX_ATTEMPTS"),
			MaxAttemptsPerIdentifier: r.positiveInt("AUTH_LOGIN_MAX_ATTEMPTS_PER_IDENTIFIER"),
			MaxAttemptsPerIP:         r.positiveInt("AUTH_LOGIN_MAX_ATTEMPTS_PER_IP"),
			Decay:                    r.loginDecay("AUTH_LOGIN_DECAY"),
			IdentifierDelay:          r.loginDuration("AUTH_LOGIN_IDENTIFIER_DELAY", true),
			IdentifierDelayMax:       r.loginDuration("AUTH_LOGIN_IDENTIFIER_DELAY_MAX", false),
		},
	}

	// Configure schemes if AUTH_SCHEME is set
	if config.Auth.DefaultScheme != "" {
		// Session/web scheme
		config.Auth.Schemes["web"] = auth.SchemeConfig{
			Driver: "session",
			Options: map[string]interface{}{
				"session": config.Auth.Session,
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
					TTL:              r.int("AUTH_JWT_TTL", 60),
					RefreshTTL:       r.int("AUTH_JWT_REFRESH_TTL", 20160),
					BlacklistEnabled: r.bool("AUTH_JWT_BLACKLIST_ENABLED", true),
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
	csrfCfg.HeaderName = r.str("CSRF_HEADER", csrfCfg.HeaderName)
	csrfCfg.FormField = r.str("CSRF_FORM_FIELD", csrfCfg.FormField)
	csrfCfg.SessionCookieName = r.str("CSRF_SESSION_COOKIE", config.Auth.Session.Name)
	csrfCfg.SingleUse = r.bool("CSRF_SINGLE_USE", false)
	csrfCfg.ErrorMessage = r.str("CSRF_ERROR_MESSAGE", csrfCfg.ErrorMessage)
	csrfCfg.WriteXSRFCookie = r.bool("CSRF_WRITE_XSRF_COOKIE", true)
	csrfCfg.XSRFCookieName = r.str("CSRF_XSRF_COOKIE_NAME", csrfCfg.XSRFCookieName)
	config.CSRF = *csrfCfg

	// Cache
	redisTLS := r.bool("REDIS_TLS", false)
	// One store, "default", on CACHE_DRIVER. Each driver reads only its
	// own fields (memory: MaxEntries; file: Path; redis: the connection;
	// memory and file: MaxValueBytes) and ignores the rest.
	config.Cache = cache.SingleStoreConfig(r.str("CACHE_PREFIX", "velocity_cache"), cache.StoreConfig{
		Driver:        r.driver("CACHE_DRIVER", cache.DriverMemory),
		Path:          os.Getenv("CACHE_PATH"),
		MaxEntries:    r.int("CACHE_MEMORY_MAX_ENTRIES", 0),
		MaxValueBytes: r.int64("CACHE_MAX_VALUE_BYTES", 0),
		Host:          r.str("REDIS_HOST", "127.0.0.1"),
		Port:          r.int("REDIS_PORT", 6379),
		Password:      os.Getenv("REDIS_PASSWORD"),
		Database:      r.int("REDIS_DATABASE", 0),
		TLS:           redisTLS,
	})

	// Log
	config.Log = log.Config{
		Driver: r.driver("LOG_DRIVER", "console"),
		Config: make(map[string]any),
	}
	if logPath := os.Getenv("LOG_PATH"); logPath != "" {
		config.Log.Config["path"] = logPath
	}
	config.Log.Config["days"] = r.int("LOG_DAYS", 14)
	config.Log.Config["level"] = r.str("LOG_LEVEL", "debug")
	// Redaction (see log.WrapWithRedactors): the "stack" driver passes
	// this map to every child, so the flags cover every channel of the
	// app logger.
	if r.bool("LOG_REDACT", false) {
		config.Log.Config["redact"] = true
	}
	if r.bool("LOG_REDACT_EMAILS", false) {
		config.Log.Config["redact_emails"] = true
	}
	if stack := r.list("LOG_STACK"); stack != nil {
		config.Log.Config["stack"] = stack
	}

	// Crypto
	cryptoKey := os.Getenv("CRYPTO_KEY")
	if cryptoKey == "" {
		cryptoKey = config.Key // Fall back to APP_KEY
	}
	config.Crypto = crypto.Config{
		Key:       cryptoKey,
		Cipher:    r.str("CRYPTO_CIPHER", "AES-256-GCM"),
		DisableV0: r.bool("CRYPTO_DISABLE_V0", false),
		Debug:     r.bool("CRYPTO_DEBUG", false),
	}
	if oldKeys := os.Getenv("CRYPTO_OLD_KEYS"); oldKeys != "" {
		config.Crypto.PreviousKeys = strings.Split(oldKeys, ",")
	}

	// Queue
	config.Queue = queue.Config{
		Driver: r.driver("QUEUE_DRIVER", "memory"),
		Redis: queue.RedisConfig{
			Host:     r.str("QUEUE_REDIS_HOST", "localhost"),
			Port:     r.port("QUEUE_REDIS_PORT", "6379"),
			Password: os.Getenv("QUEUE_REDIS_PASSWORD"),
			DB:       r.index("QUEUE_REDIS_DB", "0"),
			TLS:      redisTLS,
		},
		SigningKey: os.Getenv("QUEUE_SIGNING_KEY"),
		// final: do not rename. QUEUE_ACCEPT_UNSIGNED is the 1.0 surface
		// name for the queue payload-signing opt-out.
		AcceptUnsigned: r.bool("QUEUE_ACCEPT_UNSIGNED", false),
		Encrypt:        r.bool("QUEUE_ENCRYPT", false),
	}

	// Storage
	storageDefault := r.driver("STORAGE_DRIVER", "local")
	config.Storage = storage.Config{
		Default: storageDefault,
		Disks:   make(map[string]storage.DiskConfig),
	}
	// Always configure a local disk
	config.Storage.Disks["local"] = storage.DiskConfig{
		Driver: "local",
		Root:   r.str("FILESYSTEM_LOCAL_ROOT", "./storage/app"),
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
	config.Mail = mail.Config{
		Driver:            r.driver("MAIL_DRIVER", "log"),
		FromAddress:       os.Getenv("MAIL_FROM_ADDRESS"),
		FromName:          os.Getenv("MAIL_FROM_NAME"),
		MaxAttachmentSize: r.int64("MAIL_MAX_ATTACHMENT_SIZE", mail.DefaultMaxAttachmentSize),
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
			Port:         r.port("MAIL_PORT", ""),
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
		SSREnabled: r.bool("VIEW_SSR_ENABLED", false),
		SSRURL:     r.str("VIEW_SSR_URL", "http://127.0.0.1:13714"),
		SSRTimeout: r.duration("VIEW_SSR_TIMEOUT", 3*time.Second),
		ErrorPage:  strings.TrimSpace(os.Getenv("VIEW_ERROR_PAGE")),
	}
	config.View.SSRExcept = r.list("VIEW_SSR_EXCEPT")

	// Maintenance. An unset VELOCITY_MAINTENANCE_EXCLUDE_PATHS adds no paths.
	config.Maintenance = maintenance.Config{
		Root:         os.Getenv("VELOCITY_MAINTENANCE_ROOT"),
		AppKey:       config.Key,
		ExcludePaths: r.list("VELOCITY_MAINTENANCE_EXCLUDE_PATHS"),
	}

	// gRPC. Environment is the same normalised APP_ENV as Config.Env.
	config.GRPC = grpcconfig.Config{
		ServerPort:       r.port("GRPC_PORT", ""),
		EnableReflection: r.bool("GRPC_REFLECTION", false),
		GatewayPort:      r.port("GATEWAY_PORT", ""),
		GRPCEndpoint:     os.Getenv("GRPC_ENDPOINT"),
		Environment:      config.Env,
		// final: do not rename. GRPC_INSECURE is the 1.0 surface name for
		// the production TLS opt-out.
		Insecure: r.bool("GRPC_INSECURE", false),
	}
	config.GRPC.MaxRecvMsgSize = r.grpcMsgSize("GRPC_MAX_RECV_SIZE")
	config.GRPC.MaxSendMsgSize = r.grpcMsgSize("GRPC_MAX_SEND_SIZE")

	// Server timeouts
	config.ReadTimeout = r.duration("SERVER_READ_TIMEOUT", 30*time.Second)
	config.WriteTimeout = r.duration("SERVER_WRITE_TIMEOUT", 30*time.Second)
	config.IdleTimeout = r.duration("SERVER_IDLE_TIMEOUT", 120*time.Second)
	config.ReadHeaderTimeout = r.duration("SERVER_READ_HEADER_TIMEOUT", 10*time.Second)

	config.envErrs = r.errs
	return config
}

// envReader reads the environment for ConfigFromEnv. Unset or empty keys
// take the default. A set value that does not parse is recorded in errs and
// its field keeps the default, so Validate (called by New) reports every
// bad key at once instead of the app running with a value it was not given.
type envReader struct {
	errs []error
}

// raw returns key's value with surrounding space trimmed.
func (r *envReader) raw(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// invalid records that key holds raw, which is not want.
func (r *envReader) invalid(key, raw, want string) {
	r.errs = append(r.errs, errchain.Errorf("%w: %s=%q is not %s", ErrInvalidConfig, key, raw, want))
}

// str returns key's value, or def when it is unset or empty. The value is
// not trimmed: secrets and paths are taken as written.
func (r *envReader) str(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// port reads a TCP port held as a string field: a set value must be an
// integer from 1 to 65535. Unset is def.
func (r *envReader) port(key, def string) string {
	raw := r.raw(key)
	if raw == "" {
		return def
	}
	if n, err := strconv.Atoi(raw); err != nil || n < 1 || n > 65535 {
		r.invalid(key, raw, "a port number (1-65535)")
		return def
	}
	return raw
}

// index reads a non-negative integer held as a string field, such as a
// Redis database number. Unset is def.
func (r *envReader) index(key, def string) string {
	raw := r.raw(key)
	if raw == "" {
		return def
	}
	if n, err := strconv.Atoi(raw); err != nil || n < 0 {
		r.invalid(key, raw, "a non-negative integer")
		return def
	}
	return raw
}

// driver reads a driver name, trimmed and lower-cased like the driver
// registries' own lookup, so a later exact comparison (cache.DriverFile,
// "stack") sees the same name the registry resolved. Unset is def.
func (r *envReader) driver(key, def string) string {
	if raw := r.raw(key); raw != "" {
		return strings.ToLower(raw)
	}
	return def
}

// int reads a base-10 integer.
func (r *envReader) int(key string, def int) int {
	raw := r.raw(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		r.invalid(key, raw, "an integer")
		return def
	}
	return n
}

// int64 reads a base-10 integer in raw units (no K/M/G suffixes).
func (r *envReader) int64(key string, def int64) int64 {
	raw := r.raw(key)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		r.invalid(key, raw, "an integer")
		return def
	}
	return n
}

// positiveInt reads an integer above zero; unset is 0, which the consumer
// reads as its own default.
func (r *envReader) positiveInt(key string) int {
	raw := r.raw(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		r.invalid(key, raw, "a positive integer")
		return 0
	}
	return n
}

// duration reads a Go duration such as "30s" or "1m30s".
func (r *envReader) duration(key string, def time.Duration) time.Duration {
	raw := r.raw(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		r.invalid(key, raw, `a duration such as "30s"`)
		return def
	}
	return d
}

// bool reads 1, true, yes or on as true and 0, false, no or off as false,
// in any case.
func (r *envReader) bool(key string, def bool) bool {
	raw := r.raw(key)
	switch strings.ToLower(raw) {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	r.invalid(key, raw, "a boolean (true/false, 1/0, yes/no, on/off)")
	return def
}

// list splits a comma-separated value, trimming entries and dropping empty
// ones. Unset or empty is nil.
func (r *envReader) list(key string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(key), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sameSite reads strict, lax or none (any case); unset is Lax.
func (r *envReader) sameSite(key string) http.SameSite {
	raw := r.raw(key)
	switch strings.ToLower(raw) {
	case "", "lax":
		return http.SameSiteLaxMode
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	}
	r.invalid(key, raw, "one of strict, lax, none")
	return http.SameSiteLaxMode
}

// loginDuration reads a login-throttle duration as a Go duration ("2s",
// "500ms") or bare seconds. Unset is 0 (the default). An explicit zero is 0
// too, unless zeroDisables: then it is -1, the value
// auth.LoginThrottleConfig reads as "disabled". Negative is invalid.
func (r *envReader) loginDuration(key string, zeroDisables bool) time.Duration {
	raw := r.raw(key)
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		seconds, aerr := strconv.Atoi(raw)
		if aerr != nil {
			r.invalid(key, raw, `a duration such as "2s" or a number of seconds`)
			return 0
		}
		d = time.Duration(seconds) * time.Second
	}
	switch {
	case d < 0:
		r.invalid(key, raw, "a non-negative duration")
		return 0
	case d == 0 && zeroDisables:
		return -1
	}
	return d
}

// loginDecay reads AUTH_LOGIN_DECAY as a positive Go duration or bare
// seconds. Unset is 0 (the default).
func (r *envReader) loginDecay(key string) time.Duration {
	raw := r.raw(key)
	if raw == "" {
		return 0
	}
	before := len(r.errs)
	d := r.loginDuration(key, false)
	if len(r.errs) == before && d == 0 {
		r.invalid(key, raw, "a positive duration")
	}
	return d
}

// grpcMsgSize reads a gRPC message-size limit in bytes. Unset is 0 (the
// grpc package default); a set value must be a positive integer. The grpc
// package clamps a value above its 1 GiB ceiling, with a warning.
func (r *envReader) grpcMsgSize(key string) int {
	return r.positiveInt(key)
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

// Validate checks the root Config and every subsystem config that has no
// environment-aware relaxation, and returns all problems at once, joined
// with errors.Join: the env values ConfigFromEnv could not parse, the
// structural checks, and the driver names (an unknown or unimported driver
// fails here, at New, instead of on first use). Called from New() before
// any resource is allocated.
//
// The session cookie security attributes, CSRF, and Crypto validation are
// intentionally NOT chained here: those checks have dev-mode warning paths
// (session cookie/CSRF) or an env-conditional fallback (Crypto.Key empty =>
// warn in dev) that New() applies after the logger is up.
//
// Returns nil on success. Every joined error wraps ErrInvalidConfig, so
// errors.Is(err, ErrInvalidConfig) holds for the result.
func (c Config) Validate() error {
	errs := append([]error(nil), c.envErrs...)
	add := func(err error) {
		if err != nil {
			errs = append(errs, errchain.Errorf("%w: %w", ErrInvalidConfig, err))
		}
	}
	if c.Port != "" {
		if _, err := strconv.Atoi(c.Port); err != nil {
			errs = append(errs, errchain.Errorf("%w: APP_PORT=%q is not a valid port number", ErrInvalidConfig, c.Port))
		}
	}
	if c.ReadTimeout < 0 || c.WriteTimeout < 0 || c.IdleTimeout < 0 || c.ReadHeaderTimeout < 0 {
		errs = append(errs, errchain.Errorf("%w: server timeouts must be non-negative", ErrInvalidConfig))
	}
	add(c.Auth.Session.ValidateLifetimes())
	switch c.Auth.Session.Store {
	case "", auth.SessionStoreCookie, auth.SessionStoreServer:
	default:
		errs = append(errs, errchain.Errorf("%w: SESSION_STORE=%q is not one of %q, %q", ErrInvalidConfig, c.Auth.Session.Store, auth.SessionStoreCookie, auth.SessionStoreServer))
	}
	add(c.DB.Validate())
	add(c.Cache.Validate())
	add(c.Log.Validate())
	add(c.Mail.Validate())
	add(c.Queue.Validate())
	add(storage.ValidateConfig(c.Storage))
	// View validation runs at the root level so a bad view config fails
	// here whether or not New builds the engine (see buildsViewEngine):
	// VIEW_SSR_ENABLED=true with VIEW_SSR_TIMEOUT=0 is rejected before
	// anything is constructed.
	add(c.View.Validate())
	return errors.Join(errs...)
}

// buildsViewEngine reports whether New builds the view engine for cfg:
// any of RootTemplate, ErrorPage or SSREnabled is set. view.NewEngine
// fills an empty RootTemplate with the built-in template.
func buildsViewEngine(cfg view.Config) bool {
	return cfg.RootTemplate != "" || cfg.ErrorPage != "" || cfg.SSREnabled
}
