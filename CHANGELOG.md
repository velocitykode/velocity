# Changelog

All notable changes to Velocity will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0-rc.1] - 2026-04-18

### Migration from 0.x

**Breaking changes on the 1.0 API surface. Update every item below before upgrading.**

- **Deleted `http/` package.** The legacy router was superseded by `router/`. Remove any import of `github.com/velocitykode/velocity/http`.
- **Deleted `validate/` package.** Use `github.com/velocitykode/velocity/validation` for rules/data/request validation. The request-binding helper `validate.Form[T]` is now `github.com/velocitykode/velocity/validation/vform.Form[T]`.
- **Deleted root-package type aliases.** `velocity.AuthConfig`, `GuardConfig`, `ProviderConfig`, `SessionConfig`, `JWTConfig`, `LogConfig`, `CSRFConfig`, `CryptoConfig`, `ViewConfig`, `MailConfig` are gone. Use the canonical package types: `auth.Config`, `auth.GuardConfig`, `log.LogConfig`, `csrf.Config`, `crypto.Config`, `view.Config`, `mail.MailConfig`, …
- **Removed deprecated env-var fallbacks.** Only the new names are read:
  - `AUTH_JWT_SECRET`, `AUTH_JWT_ALGO`, `AUTH_JWT_TTL`, `AUTH_JWT_REFRESH_TTL`, `AUTH_JWT_BLACKLIST_ENABLED` (old: `JWT_*`)
  - `VIEW_SSR_ENABLED`, `VIEW_SSR_URL`, `VIEW_SSR_TIMEOUT`, `VIEW_SSR_EXCEPT` (old: `INERTIA_SSR_*`)
  - `MAIL_MAILGUN_DOMAIN`, `MAIL_MAILGUN_SECRET`, `MAIL_MAILGUN_ENDPOINT`, `MAIL_MAILGUN_WEBHOOK_SIGNING_KEY` (old: `MAILGUN_*`)
  - `MAIL_POSTMARK_TOKEN`, `MAIL_POSTMARK_MESSAGE_STREAM` (old: `POSTMARK_*`)
  - `DB_MYSQL_TLS` is still read, but now via `ConfigFromEnv` → `DBConfig.TLS`; the mysql driver no longer reaches for `os.Getenv`.
  - `DB_SSL_MODE` is still read, but now via `ConfigFromEnv` → `DBConfig.SSLMode`; the postgres driver no longer reaches for `os.Getenv`.
- **`APP_KEY` is mandatory outside `APP_ENV=testing`.** `velocity.New` returns `velocity.ErrNoAppKey` when the key is missing. Generate one with `vel key:generate`.
- **Queue driver startup never falls back silently.** `QUEUE_DRIVER=redis` with an unreachable Redis, or `QUEUE_DRIVER=database` without a DB connection, now fail app boot. To keep the in-memory driver, set `QUEUE_DRIVER=memory` explicitly.
- **ORM query builder returns errors instead of panicking.** Every chain step (`Where`, `WhereIn`, `OrderBy`, `GroupBy`, `Having`, `Select`, `Pluck`, …) captures its first validation error into `Query[T].err`. Terminal methods (`Get`, `First`, `Count`, `Update`, `Delete`, `ForceDelete`, `Pluck`, `InsertGetId`) return `q.err` ahead of executing. Call `q.Err()` for mid-chain inspection. Tests that used `require.Panics` on malformed identifiers must switch to asserting an error return.
- **ORM `Manager` methods now take a `context.Context`.** `Raw`, `Exec`, `Begin`, `Transaction` are context-aware. Pass `ctx` from the request handler or `context.Background()` from startup code. `Manager.Close()` is removed — use `Shutdown(ctx)`.
- **ORM `Database` interface slimmed.** `SetTypedEventDispatcher` is gone; `SetEventDispatcher(func(any) error)` is the sole event wiring API and matches `contract.EventDispatcherAware`.
- **ORM driver interface simplified.** Non-Context `Query`/`QueryRow`/`Exec`/`Begin` removed. Use `QueryContext`/`QueryRowContext`/`ExecContext`/`BeginTx(ctx, opts)`.
- **Queue driver interface slimmed.** Non-Context `Push`/`PushDelayed`/`Pop`/`Close` removed. Use `PushCtx`/`PushDelayedCtx`/`PopCtx`/`Shutdown(ctx)`.
- **`queue.PendingBatch.Dispatch` now takes a context.** Call sites: `.Dispatch(ctx, driver)` instead of `.Dispatch(driver)`.
- **`velocity.NewTestApp` moved.** The public constructor is now `velocitytest.NewApp` (in `github.com/velocitykode/velocity/velocitytest`). The old name remains only as a test-only internal helper and does not ship in production binaries.
- **Declarative bootstrap types moved to `chain/`.** `velocity.ProviderRegistry`, `velocity.MiddlewareStack`, `velocity.Routing`, and `velocity.Commands` are now `chain.ProviderRegistry`, `chain.MiddlewareStack`, `chain.Routing`, and `chain.Commands`. The optional provider interfaces moved with them: `chain.RouteProvider`, `chain.MiddlewareProvider`, `chain.EventProvider`, `chain.ScheduleProvider`, `chain.CommandProvider`. The root `App.Providers(fn)`, `App.Middleware(fn)`, `App.Routes(fn)`, `App.Commands(fn)` methods keep their names — only the callback parameter types changed.
  **Migration:**
  ```go
  // Before:
  import "github.com/velocitykode/velocity"
  v.Providers(func(r *velocity.ProviderRegistry) { ... })
     .Middleware(func(m *velocity.MiddlewareStack) { ... })
     .Routes(func(r *velocity.Routing) { ... })
     .Commands(func(c *velocity.Commands) { ... })

  // After:
  import (
      "github.com/velocitykode/velocity"
      "github.com/velocitykode/velocity/chain"
  )
  v.Providers(func(r *chain.ProviderRegistry) { ... })
     .Middleware(func(m *chain.MiddlewareStack) { ... })
     .Routes(func(r *chain.Routing) { ... })
     .Commands(func(c *chain.Commands) { ... })
  ```
  Consumer `CommandProvider`, `RouteProvider`, `MiddlewareProvider`, `EventProvider`, and `ScheduleProvider` implementations move their parameter types from `velocity.*` to `chain.*` the same way. `App.Exceptions(fn)` is unchanged — `exceptions.ExceptionHandler` already lives in the leaf `exceptions/` package.
- **Dropped root-package type aliases `velocity.ServiceProvider` and `velocity.Services`.** Use `app.ServiceProvider` and `app.Services` directly.

### Added

- **`velocity.BuildInfo`** — single source of truth for version metadata (`Version`, `Commit`, `Date`). Populated at build time via `-ldflags` from the `Makefile` `build` target and `console.Build`. Defaults to `"1.0.0-rc.1"`/`"devel"`/`"unknown"` for `go run` / tests.
- **`velocity.ErrNoAppKey`** — sentinel returned by `New` when `APP_KEY`/`CRYPTO_KEY` is unset outside `APP_ENV=testing`.
- **`contract.EventDispatcherAware`** — uniform `SetEventDispatcher(func(any) error)` interface. Every subsystem (cache, queue, scheduler, router, ORM, mail, csrf, view/bond, crypto, notification) implements it; bootstrap wires them all via a single interface assertion. A new `event_dispatcher_aware.go` holds compile-time `var _ contract.EventDispatcherAware = (*X)(nil)` checks so signature drift fails the build.
- **`app.RegisterExtension[T]` / `app.ExtensionAs[T]`** — generic typed accessors for `Services.Extensions`. `RegisterExtension` errors on duplicate keys; `ExtensionAs` returns a wrapped error for missing keys or type mismatches. The underlying `map[string]any` is still accessible but callers are encouraged to use the helpers.
- **`SERVER_READ_HEADER_TIMEOUT`** (default 10s) controls `http.Server.ReadHeaderTimeout`. `BaseContext` now returns the app-level shutdown context so handlers observe graceful shutdown.
- **`validation/vform.Form[T]`** — replacement for `validate.Form[T]`; lives in its own leaf package so `validation` stays router-free.
- **`orm.DBConfig.TLS`** — typed MySQL `tls=` value (mirrors `DBConfig.SSLMode` for postgres).

### Changed

- **Logger init failures now fail boot.** `log.NewLogger` errors from `velocity.New` propagate up instead of being swallowed by a console fallback.
- **`frameworkVersion` constant removed** — the version is now a variable on `velocity.BuildInfo` injected at link time.
- **`ConfigFromEnv` cleanup.** Reads `DB_MYSQL_TLS` once at config time instead of leaking to drivers; removes stdlib-log warnings on deprecated env names (those env names are no longer read at all).
- **Route diagnostic logs in websocket / auth / broadcast / scheduler / queue through `log.Logger`.** Replaced ad-hoc `fmt.Printf`/`fmt.Fprintln(os.Stderr)`/`log.Printf` calls and the package-local stdlib-log adapters (`scheduler.defaultLogger`, `queue.stdlibLogger`) with a framework-logger injection. Each package now exposes a narrow `Logger` interface and a `SetLogger` setter (wired from `velocity.New` via `Scheduler.SetLogger`, `auth.Manager.SetLogger`, `queue.MemoryDriver.SetLogger`, and `queue.SetSigningLogger`; `websocket.Server` and `broadcast/drivers.WebSocketDriver` expose `SetLogger` for consumer wiring). Loggers are stored atomically (`atomic.Value`) so request/drop hot paths that hold other locks can read them without deadlock. When no logger is installed, packages fall back to silent null loggers rather than emitting through Go's standard `log`.
- **Refactor CLI dispatcher into `command` interface — no external-API change.** The 489-line `run.go` switch was split into an internal `command` interface (`name()`, `description()`, `run(*App, []string) error`) with a per-command type and a `newCommandRegistry()` built-in map. Each previous case lives in its own file (`cmd_migrate.go`, `cmd_make.go`, `cmd_ops.go`, `cmd.go`). `App.Run()`, `App.printHelp()`, `App.printUserCommands()`, the custom-command `run` path (chain.Commands), and the internal `serve:run` subprocess entry all preserve byte-identical behavior.
- **Config struct hygiene sweep.** Dropped config fields, `DefaultConfig` helpers, and `Validate` methods that had zero readers after the chain/, bond/view, CLI, and logger-routing refactors. All removals are pure dead code — no observable behavior changes:
  - **Removed:** `velocity.Config.Name` (APP_NAME was loaded into it but never read; consumer apps read APP_NAME via `config.Get`).
  - **Removed:** `velocity.DBConfig.Collation`, `.Prefix`, `.Schema`, `.Timezone` — populated from env but never forwarded to `orm.ManagerConfig`, so they never reached the driver. `DB_COLLATION` / `DB_PREFIX` / `DB_SCHEMA` / `DB_TIMEZONE` are no longer read by `ConfigFromEnv`.
  - **Removed:** `velocity.DiskConfig.Endpoint` — never read; `initStorage` does not forward it and `storage.DiskConfig` has no corresponding field.
  - **Removed:** `auth.SessionConfig.Driver` — stored but never read; the cookie store is the only session driver and it's selected unconditionally by `guards.NewSessionGuard`. `SESSION_DRIVER` is no longer read.
  - **Removed:** `auth.DefaultConfig()`, `auth.Config.Validate()` — never called.
  - **Removed:** `auth.ProviderConfig.Options` — never read.
  - **Removed:** `csrf.Config.ErrorTemplate` — never read.
  - **Removed:** `csrf.Config.Validate()` — never called.
  - **Removed:** `crypto.DefaultConfig()` — never called.
  - **Removed:** `mail.DefaultConfig()`, `mail.MailConfig.Validate()` — never called. The associated helpers `validateLocalPort`, `validateLocalEncryption`, `validateMailgunEndpoint`, `validatePostmarkStream`, and the `allowedLocalEncryptions` / `allowedMailgunSchemes` tables are gone with them. `mail.IsAllowedPostmarkStream` / `mail.ConfigureAllowedPostmarkStreams` stay — the postmark driver uses them at send time.
  - **Removed:** `log.DefaultLogConfig()`, `log.LogConfig.Validate()` — never called.
  - **Removed:** `orm.DefaultManagerConfig()`, `orm.ManagerConfig.Validate()` — never called.
  - **Removed:** `cache.DefaultConfig()`, `cache.Config.Validate()` — never called. `cache.StoreConfig.Validate()` stays; it's called from `Manager.createStore`.
  - **Removed:** `cache.StoreConfig.Table` — reserved for a never-implemented database driver and never read.
  - **Removed:** `config.ChannelConfig.MaxSize`, `.MaxBackups`, `.Format` — never read; `log.Manager.createLogger` reads Driver/Level/Path/MaxAge/Options only.
- **`view/` / `bond/` deduplication.** `view/` is now a thin façade over `bond/` with no overlapping types. Deleted:
  - `view.Props` type alias (use `bond.Props`).
  - `view.SharePropsFunc` type alias (use `func(*http.Request) (bond.Props, error)` inline, or call `(*view.Engine).SetSharePropsFunc` directly).
  - `view.Lazy`, `view.Optional`, `view.Always`, `view.Defer`, `view.LazyProp` — all were shims re-exporting bond helpers. Import `bond` and call `bond.Lazy` / `bond.Optional` / `bond.Always` / `bond.Defer` directly.
  - `view.SimpleFlashProvider`, `view.SimpleValidationProvider` — in-memory scaffolding that was never wired to production code; cookie-based flash (`ctx.WithErrors` / `ctx.WithInput` + `bond/flash.go`) is the supported flow.
  - `view.Success`, `view.Error` — placeholder helpers that only called `http.Redirect`. Use `http.Redirect` or `(*view.Engine).Redirect` directly.
  - `view.LoadTemplateFromFile` — a two-line `os.ReadFile` wrapper. Call `os.ReadFile` at startup.
  - `view.DefaultViewConfig`, `view.Config.Validate` — unused helpers.
  - `(*view.Engine).RenderWithErrors` — unused; build a `bond.Props` with `"errors"`/`"old"` keys and call `Render` directly, or rely on the automatic flash-cookie injection in `bond.Render`.

  The `view/helpers.go` + `view/helpers_test.go` files are gone; prop-type assertions in tests should import `bond` directly.

### Security — Kernel-enforced file-path containment (`os.Root`)
- **Removed** `router.ValidateFilePathWithin(path, root string) (string, error)` along
  with its companion sentinel `router.ErrSymlinkEscape`. The previous implementation
  performed containment via `os.Lstat` + `filepath.EvalSymlinks` + prefix comparison,
  which left a TOCTOU window: an attacker could swap a symlink target between the
  validation call and the caller's subsequent `os.Open`. The predicate-only shape
  also encouraged callers to re-resolve the path, re-introducing the same race.
- **Added** `router.OpenFileIn(root *os.Root, relative string) (*os.File, error)`
  which returns an already-opened handle from the kernel-enforced root. On Linux
  this delegates to `openat2` with `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS`; other
  platforms use the strongest equivalent the Go runtime provides. Callers never
  re-resolve the path, so there is no window left for an attacker to race.
- **Added** sentinels `router.ErrPathOutsideRoot` (traversal / symlink escape) and
  `router.ErrNilRoot` (nil `*os.Root` argument). Missing files pass through
  unwrapped so `errors.Is(err, os.ErrNotExist)` continues to work.
- **Migration:**
  ```go
  // Before (TOCTOU + user-space containment):
  if _, err := router.ValidateFilePathWithin(req.Filename, uploadDir); err != nil {
      return err
  }
  f, err := os.Open(filepath.Join(uploadDir, req.Filename))

  // After (kernel-enforced, no re-resolve):
  root, err := os.OpenRoot(uploadDir)   // once at startup; persist on your driver
  if err != nil { return err }
  defer root.Close()
  f, err := router.OpenFileIn(root, req.Filename)
  ```
  `os.Root` and `os.OpenRoot` shipped in Go 1.24; Velocity already requires Go 1.26.
- **storage/local**: `LocalDriver` now holds an `*os.Root` opened at construction
  and performs every Put/Get/Delete/Move/Copy/list/stat through it. The driver
  implements `contract.ShutdownAware`, and `storage.Manager.Shutdown(ctx)` walks
  each disk so the file descriptor is released during app shutdown.
- **storage/local**: absolute paths (`/etc/passwd`) are now explicitly rejected by
  `normalizeRelative` with `ErrInvalidPath`, in addition to the kernel-level
  rejection the root would produce anyway. This gives callers a clearer error.

### Changed — Validation consolidation
- The `validate` package is now a deprecated thin shim that forwards every call to
  `validation`. Every exported symbol (`Rules`, `Messages`, `Check`, `CheckData`,
  `CheckWithDB`, `Errors`, `FormRequest`, `WithMessages`, `WithAuthorization`, and
  `Form[T]`) is annotated `// Deprecated:` with a one-line migration hint.
  **Migration:** replace `validate.Check(r, validate.Rules{...})` (map of slices)
  with `validation.Check(r, validation.Rules{...})` using pipe-separated strings
  (`"required|min:3"`). `validate.Form[T](ctx)` is still the only entry point that
  needs `router.Context` and remains supported as a shim.
- `validation.Result` is the single error-carrying type. Callers get it directly
  from `validation.Check*`; the shim still returns `*validate.Errors` with the
  same shape for backwards compatibility.
- Added sentinel `validation.ErrValidationFailed` for `errors.Is` checks. The
  existing `router.ErrValidationAborted` (introduced in ebc9168) is unchanged and
  remains the signal that a redirect response has already been written.
- `SetLocale`/`Locale` were **removed** from the `Validator` interface. Velocity
  ships English messages only; a translator abstraction is deferred until there
  is a real localisation story. Pass custom strings via `SetMessages`/`Messages`
  for now.

### Added — Validation rules
- 20 new built-in rules to close parity with Laravel: `date`, `date_format`,
  `regex`, `ip`, `ipv4`, `ipv6`, `json`, `uuid`, `ulid`, `starts_with`,
  `ends_with`, `file`, `mimes`, `image`, `password`, `timezone`, `gt`, `gte`,
  `lt`, `lte`. (`between` already existed.)
- `regex` rejects patterns that are not anchored with `^...$` and refuses
  nested quantifiers (`(...+)+`, `(...*)*`, etc.) to prevent catastrophic
  backtracking; each call is additionally bounded by a 10 ms wall-clock cap.
- `validation/testing` helper package: `NewTestValidator()` and
  `RuleAssertion(t, rule, input, expected)` for table-driven tests of custom
  rules.

### Fixed — Validation correctness
- `email` no longer gates on a hand-rolled regex; the rule now defers to
  `net/mail.ParseAddress` which is the canonical RFC 5322 check.
- `url` now uses `url.Parse` plus an `http`/`https` scheme allowlist instead of
  a regex.
- `url_public` wraps `net.LookupIP` in a 5-second `context.WithTimeout` so that
  a slow resolver cannot hang a request indefinitely.
- Database-rule identifier quoting documented for dotted identifiers (schema-
  qualified `schema.table` and `table.column` names) across PostgreSQL, MySQL
  and SQLite, with a test matrix covering each driver.
### Security
- **auth/jwt**: enforce algorithm allowlist before signature verification; reject `none` unconditionally; add RS256/RS384/RS512 support (mismatched alg families now fail fast, closing the classic HMAC-verify-with-public-key confusion). `NewJWTManager` now returns `(*JWTManager, error)`.
- **auth/jwt**: add `JWTConfig.Validate()` (secret length ≥32 for HMAC, TTL > 0, allowlisted algorithm, RSA key pair for RSxxx).
- **auth/session**: replace `crypto/rand` panics in session ID and remember-token generation with propagated errors; mirror of jti fix.
- **auth/session**: remember-me cookies now store a SHA-256 hash server-side (raw token only lives in the encrypted cookie); cookie TTL is clamped to `min(session lifetime, 30d)`; refuses creation when `Lifetime == 0`.
- **csrf**: replace silent session-ID generation with a `csrf.session_fallback` event; token comparison stays on `subtle.ConstantTimeCompare`; rename `Config.HTTPOnly` → `Config.HttpOnly` to match `net/http.Cookie.HttpOnly`.
- **crypto/aes**: replace `fmt.Sprintf("base64:%s.%s", ...)` MAC concatenation with domain-separated HMAC writes (`"velocity\x00" || iv || ct`).
- **crypto/aes wire format versioning (dual-read window)**: payloads now carry a
  `v1:` sentinel prefix that selects the domain-separated MAC path. Decrypt
  transparently accepts both formats for one release cycle:
  - **On encrypt:** emit `v1:` only. No operator action required at install.
  - **On decrypt:** `v1:` → domain-separated MAC; no prefix → legacy (pre-sweep)
    `HMAC-SHA256(hmacKey, "base64:"+valueB64+"."+ivB64)`. Operators who upgraded
    before this release and produced a mix of v0 (pre-sweep) and v1 payloads
    get a zero-downtime rollforward.
  - **Operator signal:** every successful v0 decrypt dispatches a
    `crypto.legacy_decrypt` event; a one-shot WARN (`velocity/crypto: legacy
    v0 payload decrypted, rotate before v2.0`) logs once per Encryptor
    instance. Monitor the event stream to gauge how much pre-versioned
    ciphertext remains.
  - **Sunset:** v0 decrypt is deprecated in this release and **removed in
    v2.0**. Before upgrading to v2.0, operators must rotate session cookies
    and re-encrypt any stored ciphertext (encrypted DB columns, signed URLs,
    remember-me tokens) so that nothing produced before this release lingers.
  - GCM-mode payloads also carry the `v1:` sentinel for format uniformity;
    v0 GCM envelopes remain decryptable since GCM integrity is cipher-provided.
- **crypto**: `NewEncryptor` now calls `Config.Validate()` up-front. Only AES-128/192/256 key sizes are accepted; `Validate()` returns `ErrInvalidKey` / `ErrInvalidCipher` sentinels.
- **auth/middleware**: stop logging raw `RemoteAddr`; hash the client IP with a per-process salt (new `auth.SetAuditSalt` lets operators pin the salt for correlated log pipelines).

### Added
- **contract.LoginThrottler**: new interface for rate-limiting login attempts. Default implementation is `auth.NoopLoginThrottler`. Wire into session/JWT guards via `SetLoginThrottler`; `Attempt()` now consults the throttler before credential checks and emits `auth.ErrLoginThrottled`.

## [0.0.3] - 2025-12-29

### Added
- Initial public release
- 21 core packages: auth, cache, config, crypto, events, hash, http, log, mail, orm, queue, router, scheduler, session, storage, str, validation, view, websocket, broadcast, async
- Driver-based architecture for all packages
- Environment-based configuration
- Inertia.js integration for React frontend
- Custom query-builder ORM with migrations, soft deletes, and relations (not GORM)
- Background job processing
- Real-time features with WebSockets

[1.0.0-rc.1]: https://github.com/velocitykode/velocity/releases/tag/v1.0.0-rc.1
