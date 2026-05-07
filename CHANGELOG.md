# Changelog

All notable changes to Velocity will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Breaking changes

- **`orm.ToSnakeCase` (and the auto-derived table/column names that flow from it) now splits acronym->word and digit->word boundaries.** Previously consecutive uppercase letters collapsed into a single token, so `SSHKey` mapped to table `sshkey` (and pluralized to `sshkeys`), `URLPath` mapped to column `urlpath`, `OAuthID` to `oauthid`, `Field1Name` to `field1name`. The new mapping is `ssh_key` / `url_path` / `o_auth_id` / `field1_name` respectively. Apps with acronym-named or digit-bearing model types that relied on the previous mapping must either override `TableName()` on the model to pin the legacy name, or run a migration to rename the table/column to the new convention. The `console` scaffolder (`vel make:model`, `vel make:migration`, etc.) now uses the same algorithm via `orm.ToSnakeCase`, so newly generated migrations match the runtime ORM.

### Added

- **`orm.Model[T].WithContext(ctx)`** on Model, UUIDModel, SoftDeleteModel, SoftDeleteUUIDModel, ImmutableModel, ImmutableUUIDModel. Returns `*Query[T]` so the static-helper entry points (`Find`, `FindBy`, `First`, `Last`, `All`, `Create`, etc.) can carry a context without rewriting to the verbose chain form. Example: `User{}.WithContext(ctx).Where("id=?", id).First(&u)`.
- **`orm.Query[T].WhereGroup(func(*Query[T]))` / `OrWhereGroup(...)`**: emits parenthesized AND/OR sub-conditions. Replaces the previous flat `Where(...).Where(...).OrWhere(...)` chain that bound `OR` against the wrong predicate. Implemented via a new `Condition.Group` field plus a shared recursive `compileConditions` in the postgres/mysql/sqlite grammars.
- **`orm.ImmutableModel[T]` / `orm.ImmutableUUIDModel[T]`**: append-only base models for tables without an `updated_at` column (e.g. `audit_logs`). Provides the same static helpers as `Model[T]` minus the update path. Save on a persisted record returns `orm.ErrImmutableModelUpdate`. Use `orm.Save(manager, &record)` for inserts.
- **`auth.SessionGuard.CheckWithError(r *http.Request) (bool, error)`**: companion to `Check` that surfaces *why* a request is unauthenticated. Returns `(false, auth.ErrSessionRevoked)` when the cookie is valid but the matching server-side session record was deleted; `(false, nil)` for ordinary unauthenticated states (no cookie, missing user); `(false, err)` on transient store failures (fail-closed). Lets middleware deliver "your session was signed out remotely" UX without breaking the `auth.Guard` interface, which still returns `bool`.
- **Auth public surface for server-side session lifecycle:**
  - `auth.ErrSessionRevoked`: sentinel returned by `CheckWithError` when a cookie's session record was administratively removed.
  - `auth.ErrRememberClearPartial`: sentinel returned (wrapped, with `errors.Join`'d causes) by `Manager.RevokeAllSessions` when the store delete succeeded but at least one guard's `RememberTokenClearer` failed.
  - `auth.ServerSessionStoreReceiver`: optional interface a `Guard` implements to receive the store via `Manager.SetServerSessionStore`. Non-receivers (e.g. JWT) are silently skipped.
  - `auth.RememberTokenClearer`: optional interface a `Guard` implements so `Manager.RevokeAllSessions` can also nuke the user's remember-me token.

### Changed

- **`auth.Manager.RevokeAllSessions` return semantics.** Previously this method returned `nil` whenever the underlying `ServerSessionStore.DeleteAllForUser` succeeded, regardless of any subsequent work. It now also runs every registered guard's `RememberTokenClearer` and, if any clearer fails, returns an error wrapping `auth.ErrRememberClearPartial` joined with the underlying causes (via `errors.Join`). The store delete is still the load-bearing security action and runs first; clearers run only after it succeeds. Adopters with `if err := mgr.RevokeAllSessions(ctx, uid); err != nil { return err }` will now see clearer failures they did not see before. To preserve the prior best-effort behavior, gate the return on the sentinel: `if err := mgr.RevokeAllSessions(ctx, uid); err != nil && !errors.Is(err, auth.ErrRememberClearPartial) { return err }`.

### Fixed

- **`auth.SessionGuard` now consults `ServerSessionStore` on every request when one is installed.** Previously `Manager.RevokeSession` / `RevokeAllSessions` deleted the store row but `SessionGuard.Check` / `User` only validated the encrypted cookie, so a "logged out" browser stayed authenticated until the cookie's TTL elapsed. The guard now performs `store.Get(sessionID)` on every authenticated request; a missing or expired record returns the new `auth.ErrSessionRevoked` sentinel and `Check` returns `false`. New `SessionGuard.CheckWithError(r) (bool, error)` lets middleware distinguish revoked from expired from no-cookie for UX. `Login` writes the session record (id, user id, created/last-seen/expires, IP, User-Agent) to the store; `Logout` deletes it. `LastSeenAt` write-back is debounced to one `Put` per 60s to avoid amplifying read RTTs into double round-trips. Cookie-only behavior is preserved when no store is installed; `Manager.SetServerSessionStore` propagates to every guard implementing the new `auth.ServerSessionStoreReceiver` interface (and to guards registered after the store is installed). IP is captured from `r.RemoteAddr` with the `:port` suffix stripped via `net.SplitHostPort`; `X-Forwarded-For` / trusted-proxy support is deferred.
- **`auth.Manager.RevokeAllSessions` also clears the user's remember-me token.** Without this, a "sign out everywhere" admin action would still let the revoked browser resurrect a fresh session on the next request via its remember cookie. Manager now walks every registered guard and calls `ClearRememberTokensForUser` on guards implementing the new `auth.RememberTokenClearer` interface; `SessionGuard` implements it by calling `UserProvider.UpdateRememberToken(user, "")`. `Manager.RevokeSession` (single-session) intentionally does NOT clear the remember token, since remember tokens are per-user and wiping one would log the user out across every device; if you need that, call `RevokeAllSessions`. (See **Changed** above for the new partial-failure return-error contract.)
- **`Pluck()` now honors `Distinct()`**. Previously the `SelectQuery{}` literal in `Pluck` omitted `Distinct`, so `Model[User]{}.Distinct().Pluck("role")` silently returned duplicates. Asserted across all three driver grammars.
- **WHERE compilation no longer mis-binds nested AND/OR**. The flat-slice condition compiler is replaced by a recursive walk that emits parens around grouped predicates.
- **Side fix in postgres `CompileUpdate` / `CompileDelete`**: `WhereIn(...).Update(...)` and `WhereIn(...).Delete()` previously emitted `col IN $1` and bound the slice as a single param, producing a runtime driver error or silent corruption. The shared `compileConditions` helper now expands `IN` / `NOT IN` / `BETWEEN` / `NOT BETWEEN` correctly in UPDATE and DELETE WHERE clauses on every driver. Adopters who routed those queries through `NewRawQuery` to work around the breakage can drop the workaround.
- **`Model[T]` no longer mandates an `updated_at` column for read paths**. The `query.Update` injection of `updated_at` is now gated on the model actually having that field (cached reflection lookup), so embedding `Model[T]` in a struct without a `UpdatedAt` field no longer breaks `Update`. Tables that genuinely need append-only semantics should still use `ImmutableModel[T]` for the API guarantees.

## [0.32.0] - 2026-04-19

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
- **`APP_KEY` is mandatory outside `APP_ENV=testing` and `APP_ENV=development`.** `velocity.New` returns `velocity.ErrNoAppKey` when the key is missing in any other environment (including unset `APP_ENV`, which most production deployments have). `APP_ENV=testing` bypasses the check silently; `APP_ENV=development` bypasses it with a boot-time `a.Log.Warn(...)` so developers see that crypto is disabled without being blocked. Generate a key with `vel key:generate`.
- **Queue driver startup never falls back silently.** `QUEUE_DRIVER=redis` with an unreachable Redis, or `QUEUE_DRIVER=database` without a DB connection, now fail app boot. To keep the in-memory driver, set `QUEUE_DRIVER=memory` explicitly.
- **ORM query builder returns errors instead of panicking.** Every chain step (`Where`, `WhereIn`, `OrderBy`, `GroupBy`, `Having`, `Select`, `Pluck`, …) captures its first validation error into `Query[T].err`. Terminal methods (`Get`, `First`, `Count`, `Update`, `Delete`, `ForceDelete`, `Pluck`, `InsertGetId`) return `q.err` ahead of executing. Call `q.Err()` for mid-chain inspection. Tests that used `require.Panics` on malformed identifiers must switch to asserting an error return.
- **ORM `Manager` methods now take a `context.Context`.** `Raw`, `Exec`, `Begin`, `Transaction` are context-aware. Pass `ctx` from the request handler or `context.Background()` from startup code. `Manager.Close()` is removed — use `Shutdown(ctx)`. Note: the query builder (`orm.Query[T]`) remains permissive — it falls back to `context.Background()` when `.WithContext(ctx)` is not called. For cancellation in request handlers, call `.WithContext(ctx)` explicitly; the tighter `Manager`-level context discipline does not propagate into the builder chain automatically.
- **ORM `Database` interface slimmed.** `SetTypedEventDispatcher` is gone; `SetEventDispatcher(func(any) error)` is the sole event wiring API and matches `contract.EventDispatcherAware`.
- **ORM driver interface simplified.** Non-Context `Query`/`QueryRow`/`Exec`/`Begin` removed. Use `QueryContext`/`QueryRowContext`/`ExecContext`/`BeginTx(ctx, opts)`.
- **Queue driver interface slimmed.** Non-Context `Push`/`PushDelayed`/`Pop`/`Close` removed. Use `PushCtx`/`PushDelayedCtx`/`PopCtx`/`Shutdown(ctx)`.
- **`queue.PendingBatch.Dispatch` now takes a context.** Call sites: `.Dispatch(ctx, driver)` instead of `.Dispatch(driver)`.
- **`queue.Worker.Start` now requires a `context.Context`.** The worker's internal ctx is derived from it so application-level shutdown contexts propagate into job-execution contexts. Previously `w.ctx` was rooted at `context.Background()` and ignored any caller-supplied lifecycle ctx — only an explicit `Worker.Stop()` could abort an in-flight job. **Migration:** change `worker.Start()` call sites to `worker.Start(ctx)`. The framework's built-in `queue:work` command already passes the correct ctx.
- **`cache/drivers.Lock` interface methods now require `context.Context`.** `Get(ctx)`, `Release(ctx)`, `Run(ctx, cb)`, `Block(ctx, timeout, cb)`, `ForceRelease(ctx)` — previously ctx-less, so request cancellation never reached Redis. `Owner()` is unchanged. The `cache.Manager.Lock(key, ttl...)` / `RestoreLock(key, owner)` surface is unchanged — ctx flows through the returned lock's methods. **Migration:** update every `lock.Get()` → `lock.Get(ctx)`, etc. In HTTP handlers use `ctx.Request.Context()`; elsewhere use the relevant request- or job-scoped ctx, or `context.Background()` if no cancellation is desired.
- **CSRF middleware refuses requests without a session cookie** (in the default `ModeSession`). Previously the middleware generated a per-request ephemeral session ID for any unsessioned request and issued a CSRF token scoped to it — letting an attacker bind a token to a self-chosen ID and replay it. Requests without a session cookie now receive `ErrNoSession` → 419. **Migration:** ensure session middleware runs upstream of the CSRF middleware for all CSRF-protected routes. Apps that genuinely need CSRF without server-side sessions must wait for `csrf.ModeDoubleSubmit` (reserved; not yet implemented) or supply their own token strategy. The new `csrf.NewE(cfg) (*CSRF, error)` constructor is preferred over `csrf.New(cfg)` for fail-fast Mode validation.
- **Session and CSRF cookie configs are validated at boot.** `auth.SessionConfig.Validate(env)` and `csrf.Config.Validate(env)` enforce: `HttpOnly=true` (unless the new `AllowJSAccess=true` opt-in), `Secure=true` outside `APP_ENV=testing` / `APP_ENV=development`, non-zero `SameSite`, and `SameSite=None` requires `Secure=true`. `velocity.New()` fails boot on violation in production, logs `a.Log.Warn` in development, stays silent in testing. **Migration:** apps that shipped with `SESSION_SECURE=false` / `CSRF_SECURE=false` / `SESSION_HTTP_ONLY=false` / `CSRF_HTTP_ONLY=false` in production will fail to boot until the config is fixed. Recommended fix is to set those to their secure defaults; the `AllowJSAccess=true` escape hatch exists for rare cases that genuinely need client-side access.
- **JWT algorithm validation is strict.** `auth` no longer falls through to HS256 for unknown algorithm strings. `GenerateToken` / `GenerateRefreshToken` return `ErrUnsupportedSigningMethod` when `AUTH_JWT_ALGO` is a typo or unimplemented alg. **Migration:** verify `AUTH_JWT_ALGO` is one of the supported values (HS256/HS384/HS512, RS256/RS384/RS512, ES256/ES384/ES512 — whatever the JWT guard actually implements).
- **`velocity.NewTestApp` moved.** The public constructor is now `velocitytest.NewApp` (in `github.com/velocitykode/velocity/velocitytest`). The old name remains only as a test-only internal helper and does not ship in production binaries.
- **Declarative bootstrap types re-homed in `chain/` and `app/` for import-cycle reasons; consumer-facing names unchanged.** `velocity.Routing`, `velocity.Commands`, `velocity.MiddlewareStack`, `velocity.ProviderRegistry`, `velocity.Services`, `velocity.ServiceProvider`, and the optional provider interfaces (`velocity.RouteProvider`, `velocity.MiddlewareProvider`, `velocity.EventProvider`, `velocity.ScheduleProvider`, `velocity.CommandProvider`) all remain at their 0.x import paths. They are now type aliases for types in `chain/` (declarative bootstrap) and `app/` (service container), which is where framework internals reference them. Existing consumer code using `velocity.X` needs no changes; packages that already migrated to `chain.X` during the rc cycle keep working because both paths resolve to the same Go type. `App.Exceptions(fn)` is unchanged — `exceptions.ExceptionHandler` already lives in the leaf `exceptions/` package.

  The canonical path for application code is `velocity.X`. `chain.X` and `app.X` are implementation homes — imported by framework internals and by third-party service providers that need to embed or reference those types, not by application code. The `velocity-cli` generators and `velocity-template`/`velocity-template-api` starters emit `*velocity.Routing`, `*velocity.Commands`, and so on; the chain paths are not recommended for new code.

### Intentional deprecations (scoped)

The 1.0 API surface ships with **no backward-compat shims** — every 0.x name that was marked `Deprecated:` has been deleted or renamed. Two deliberate exceptions, both scoped and signposted:

- **`view.Lazy` / `view.LazyProp` → `view.Optional` / `view.OptionalProp`.** Mirrors Inertia.js's own `Inertia::lazy()` sunset. The type aliases and forwarding functions exist so application code that had already adopted the 0.x names does not break at the type-check step; migration is a mechanical rename. No removal target — tracks upstream Inertia.js.
- **Crypto v0 wire-format dual-read.** `crypto.Decrypt` accepts both the new `v1:` sentinel envelope and legacy pre-sweep payloads for one release cycle. Every v0 decrypt dispatches `crypto.legacy_decrypt` and logs a one-shot WARN. **Removed in v2.0** — operators must rotate session cookies and re-encrypt stored ciphertext before upgrading. This is the transition window the crypto sweep needs; without it, a 1.0 upgrade would invalidate every existing session cookie the moment it deploys.

Everything else — the Close()/Stop() shims, legacy env-var fallbacks, ORM `EventName()`, router `PermissiveCORSConfig`, the `validate/` package, etc. — is deleted outright, with migration notes in the sections below.

### Added

- **`velocity.BuildInfo`** — single source of truth for version metadata (`Version`, `Commit`, `Date`). Populated at build time via `-ldflags` from the `Makefile` `build` target and `console.Build`. Defaults to `"devel"`/`"devel"`/`"unknown"` for `go run` / tests.
- **`velocity.ErrNoAppKey`** — sentinel returned by `New` when `APP_KEY`/`CRYPTO_KEY` is unset outside `APP_ENV=testing` / `APP_ENV=development` (the dev bypass logs a boot-time WARN instead of erroring, so local workflows aren't blocked).
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
  - **Removed:** `velocity.Config.Name` (APP_NAME was loaded into it but never read; consumer apps read APP_NAME via `os.Getenv` or their own config loader).
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
- **`view/` dead-code cleanup.** Deleted from `view/`:
  - `view.SimpleFlashProvider`, `view.SimpleValidationProvider` — in-memory scaffolding never wired to production; cookie-based flash (`ctx.WithErrors` / `ctx.WithInput` + `bond/flash.go`) is the supported flow.
  - `view.Success`, `view.Error` — placeholder helpers that only called `http.Redirect`. Call `http.Redirect` or `(*view.Engine).Redirect` directly.
  - `view.LoadTemplateFromFile` — a two-line `os.ReadFile` wrapper.
  - `view.DefaultViewConfig`, `view.Config.Validate` — unused.
  - `(*view.Engine).RenderWithErrors` — unused; cookie-flash injection handles the flow at render time.

  Files removed: `view/helpers.go`, `view/helpers_test.go`.

- **Deleted `config/` package.** The 11-line `config.Get` helper was a trivial wrapper around `os.Getenv` with a fallback; the `config.LoggingConfig` and `config.ChannelConfig` types belonged to `log/` but lived in a separate package for historical reasons. `LoggingConfig` and `ChannelConfig` are now defined directly in `log/` (same shape, same methods, same JSON tags); `grpc/init.go` reads env vars via `os.Getenv` through local `envOr` / `envInt` helpers. Zero consumer impact — `log.NewManager(cfg)` continues to work and now takes `log.LoggingConfig` instead of `config.LoggingConfig`.

- **Unified event naming on `Name() string` across `orm`, `events`, `scheduler`, `router`.** `orm.QueryExecuted.EventName()`, `orm.QueryFailed.EventName()`, and `orm.TxRecover.EventName()` are removed; the canonical method is `Name() string` (matching `events.Event`, `scheduler.Event`, and `router.Event`). The `orm.Event` interface now requires `Name() string` instead of `EventName() string`. The previous `Deprecated:` markers on the `Name()` shims are gone with the `EventName()` originals. Migration: rename any `e.EventName()` call to `e.Name()`.

- **Renamed — `router.PermissiveCORSConfig()` → `router.InsecureAllowAllCORS()`.** Zero external callers; deprecation marker dropped. The new name carries the warning the old one relied on a doc comment to communicate: combined with `AllowCredentials`, wildcard origins echo the request origin back, allowing any site to make credentialed requests. Production traffic should use `DefaultCORSConfig` with an explicit allowlist.

- **Clarified — `view/` is the public rendering surface.** `view/` stays the stable façade; `bond/` is the Inertia.js protocol implementation and is not intended for direct import by application code. `view/` re-exposes the prop API so consumers never need to import `bond`:
  - Types: `view.Props`, `view.SharePropsFunc`, `view.LazyProp` (deprecated — use `view.OptionalProp`), `view.OptionalProp`, `view.AlwaysProp`, `view.DeferProp` — identity-preserving type aliases over the `bond` types.
  - Helpers: `view.Lazy` (deprecated — use `view.Optional`), `view.Optional`, `view.Always`, `view.Defer` — forwarding functions with mirrored deprecation guidance.
  - The `Deprecated: use Optional` marker on `view.Lazy` / `view.LazyProp` mirrors the same marker on `bond.Lazy` / `bond.LazyProp` so consumers see the Inertia.js sunset guidance at the facade layer without having to chase through protocol docs.

  Upgrade: if your app imports `bond` directly (`import "github.com/velocitykode/velocity/bond"`), swap the import for `view` and rename `bond.X` → `view.X`. Because the types are aliases (not fresh definitions), `view.Props{}` and `bond.Props{}` are the same Go type at runtime — no conversion needed for gradual migration.

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
- `validation.Result` is the single error-carrying type returned from
  `validation.Check*`. (The `validate/` package — which previously wrapped
  `validation` — has been deleted entirely; see Migration from 0.x above.)
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

### Fixed
- **`Serve()` recursion in hot-reload subprocess.** `./vel serve` spawns a
  `serve:run` subprocess; that subprocess previously recursed Serve() → Run() →
  runCommand("serve:run") → serveRunCmd.run() → Serve() until the goroutine stack
  hit 1 GB and the process crashed with `fatal error: stack overflow`. Split
  `Serve()` into a public dispatcher (args-check + delegate to Run() or serveHTTP)
  and a private `serveHTTP()` helper; `serveRunCmd.run` now calls `serveHTTP()`
  directly, bypassing Run() dispatch. No public API change.

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

### Removed — Compat sweep
Deleted nine deprecated `Close()`/`Stop()` shims and one internal compat wrapper that slipped past the 1.0 audit. No replacements are provided — each call site must migrate to the listed successor. Drivers and manager fallbacks that branched on the now-removed `Close()` interface are simplified to the single `Shutdown(ctx)` path.

- **`cache.Manager.Close()`** and the `Close() error` member of `cache.CacheManager` → `cache.Manager.Shutdown(ctx)`.
- **`cache/drivers.MemoryStore.Close()`, `FileStore.Close()`, `RedisStore.Close()`** → `Shutdown(ctx)` on each. The `Manager.Shutdown` fallback that re-tried `Close()` on unknown stores is gone; every built-in store implements `ShutdownAware`.
- **`log.Closer` interface** and the `else if closer, ok := l.(Closer)` branch in `StackLogger.Shutdown` → implement `Shutdowner` (`Shutdown(ctx) error`).
- **`log.StackLogger.Close()`** → `Shutdown(ctx)`.
- **`log/drivers.FileLogger.Close()`** → `Shutdown(ctx)`.
- **`csrf/stores.SessionStore.Close()`** and the `Close()`-shape fallback inside `csrf.CSRF.Shutdown` → `SessionStore.Shutdown(ctx)`.
- **`grpc.NewError` and `grpc.NewErrorf`** → `grpc.NewGRPCError` / `grpc.NewGRPCErrorf`.
- **Private `router.parseTrustedProxies`** (unexported compat wrapper) → exported `router.ParseTrustedProxies`.
- **Scheduler stop chain**: `scheduler.Scheduler.Stop()`, `scheduler.Manager.StopAll()`, `scheduler.Kernel.Stop()`, and the `Stop()` member of the `scheduler.TaskScheduler` interface → `Scheduler.Shutdown(ctx)`. Internal `Run()`'s `<-ctx.Done()` branch now calls `Shutdown(ctx)` directly.

### Fixed — Final pre-1.0 sweep (wave 2)

A second re-review surfaced five remaining MUST FIX items. All landed in one batch.

**WebSocket (`websocket/server.go`, `websocket/client.go`)**
- `Server.Shutdown(ctx)` replaces `Server.Stop()` (deleted). The run-loop goroutine and every per-client read/write pump are tracked on an internal `sync.WaitGroup`; `Shutdown` waits for them to drain bounded by the caller's context, returning `ctx.Err()` on timeout. `writePump` now observes the server stop channel directly so it exits without waiting for the ping ticker.
- Compile-time `var _ contract.ShutdownAware = (*websocket.Server)(nil)` assertion in `event_dispatcher_aware.go`.
- **Migration:** every `server.Stop()` call becomes `server.Shutdown(context.Background())` (or a bounded ctx for graceful drain).

**Mail (`mail/message.go`, `mail/init.go`, `notification/channels/mail.go`)**
- Attachment DoS cap: `AttachFile` / `AttachData` now reject payloads larger than `MailConfig.MaxAttachmentSize` (default 25 MiB, tunable via `MAIL_MAX_ATTACHMENT_SIZE`). New `ErrAttachmentTooLarge` typed error. Zero-value config resolves to the default — "unlimited" is not expressible through config.
- SMTP header injection: `Header`, `Subject`, `From`, `To`, `CC`, `BCC`, `ReplyTo` now reject CR, LF, NUL, and C0 control characters in any value. `Header` additionally enforces RFC 5322 §3.2.3 name grammar. Violations are stored as a deferred error on `*Message` and surfaced from `Manager.Send`, the `NewMailer`-returned `checkedMailer`, and the notification mail channel before any driver sees the message. New `ErrInvalidHeader` typed error and `Message.Err()` accessor. No RFC 5322 folding support in 1.0 (conservative stance).

**gRPC (`grpc/server.go`)**
- `*grpc.Server` now implements `contract.EventDispatcherAware` via `SetEventDispatcher(func(any) error)` (mutex-guarded, nil-safe). Compile-time assertions for both `EventDispatcherAware` and `ShutdownAware` pinned in `event_dispatcher_aware.go`. No events are dispatched yet — `grpc.request.completed` / `grpc.request.failed` will land in a follow-up. Consumers registering `*grpc.Server` under `Services.Extensions` are auto-wired by the existing `wireInstanceEvents` extensions loop.

**CLI (`cmd.go`, `cmd_make.go`, `cmd_ops.go`)**
- Unknown-command handlers return errors instead of calling `os.Exit(1)`. Previously bypassed `Serve()`'s deferred `shutdownCancel` (and any caller-installed defers), preventing in-flight work from draining. Scope covers six sites: `runCommand` unknown-built-in, `runCmd.run` unknown-custom, `requireMakeName` missing-name, and three inline `make:*` paths. The top-level `Serve()` / `Run()` caller is responsible for converting errors to exit codes.

### Fixed — Release-blocker sweep

A pre-1.0 review surfaced five MUST FIX clusters across bootstrap, ORM, queue, cache, and security. All landed in one batch before tagging.

**Bootstrap & lifecycle (`app.go`, `serve.go`)**
- `Serve()` now defers `shutdownCancel()`, so the `context.WithCancel` created in `New()` is released on every exit path — including the CLI-dispatch path where `os.Args` routes `Serve()` into `Run()`.
- `serveHTTP()` pairs `signal.Notify` with `defer signal.Stop`, preventing a SIGINT/SIGTERM subscription leak across in-process restarts (tests, hot-reload).
- `New()` now maintains a deferred cleanup stack that closes every already-opened subsystem (logger, DB pool, cache, CSRF, view, queue, storage, scheduler, mail, notification) if a later init stage or the provider lifecycle fails. Success path disarms the stack.
- `serveHTTP()`'s bootstrap-error branch now runs `App.Shutdown` with a 30-second deadline and joins its error with the bootstrap error; partially-wired chain providers are torn down cleanly.

**ORM (`orm/query.go`, `orm/drivers`)**
- `Query.Update` no longer mutates the caller's `updates` map as a side effect. The map is copied internally before the `updated_at` timestamp is injected.
- The `updated_at`/`deleted_at` sentinel is now a typed `orm.RawSQL` marker (`orm.NOW` / `orm.CurrentTimestamp`) rather than the plain string `"NOW()"`. Previously any value whose runtime string content equalled `"NOW()"` (e.g. a user-controlled comment column) was promoted to raw SQL by a string-content check in the MySQL and PostgreSQL grammars — a SQL-injection vector. The fix closes the vector on all three drivers by matching the marker type, not its string content. SQLite, which previously had no raw-SQL branch at all, now honours the same marker.

**Queue (`queue/worker.go`)**
- Worker ctx now derives from the caller-supplied context (see Migration: `Worker.Start(ctx)`). Application shutdown propagates into job execution; cancellation no longer requires an explicit `Worker.Stop()`.
- Job-retry re-queueing (`PushDelayedCtx` on failure) now runs on a detached 5-second timeout context instead of the worker ctx. A slow driver (Redis partition, DB lock) can no longer hold graceful shutdown open past the retry-push budget; if the push exceeds 5s the job is marked failed.

**Cache (`cache/drivers/redis_lock.go`)**
- `RedisLock.Get`/`Release`/`Block`/`ForceRelease` no longer silently use `context.Background()`. A cancelled request-scoped ctx now propagates into the underlying SETNX / EVAL / DEL so callers who cancel (e.g. via `http.Request.Context()`) get prompt cancellation instead of blocking until Redis responds. `Block` also wakes on ctx cancellation between retries rather than waiting out the full timeout. Memory lock matches the same contract.

### Security

- **JWT**: closed algorithm-downgrade vector — `getSigningMethod` no longer falls through to HS256 for unknown algorithm strings. `GenerateToken` and `GenerateRefreshToken` now return `ErrUnsupportedSigningMethod` instead of silently signing with HS256.
- **Session guard**: `Login()` now aborts when `session.Regenerate()` fails, closing a session-fixation window where the user was bound to the attacker-chosen session ID on store I/O error.
- **Rate limiter**: `X-Real-IP` is now strictly parsed as a single IP (rejecting comma / whitespace / tab separated payloads) to prevent throttle-key spoofing via injected multi-value headers. Only honoured when the immediate peer is in the trusted-proxy list.
- **CSRF**: middleware no longer generates ephemeral session IDs when no session cookie is present — requests without a session are rejected with `ErrNoSession` (419 response). New `csrf.Mode` enum makes the binding strategy explicit; only `ModeSession` is implemented. `csrf.ModeDoubleSubmit` is reserved for a future release. See Migration.
- **Cookie config**: `auth.SessionConfig` and `csrf.Config` now carry `Validate(env string) error` methods enforcing `HttpOnly=true` (unless `AllowJSAccess=true` opt-in), `Secure=true` outside testing/development, non-zero `SameSite`, and `SameSite=None ⇒ Secure=true`. Wired into `velocity.New()` — production boot fails on violation, development logs a warning, testing is silent. See Migration.

### Changed — Shutdown contract consolidation
- Every ad-hoc `interface{ Shutdown(context.Context) error }` assertion in `serve.go` (5 sites), `csrf/csrf.go`, `cache/manager.go`, and `storage/storage.go` now uses `contract.ShutdownAware` directly. The contract already existed (see Added for 0.32.0); it's just the single source of truth now rather than a parallel inline pattern. No behavioural change — type-asserting against `contract.ShutdownAware` is identical at runtime to the inline form. The benefit is that future subsystem additions only have to look at one place to know the shape, and signature drift fails at compile time instead of silently failing the type assertion.

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

[0.32.0]: https://github.com/velocitykode/velocity/releases/tag/v0.32.0
