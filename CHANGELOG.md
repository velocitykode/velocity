# Changelog

All notable changes to Velocity will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Identity rename: providers are modules, auth guards are schemes

Velocity's extension and auth vocabulary drops its borrowed names. Every
change below is an identifier rename: registration order, the two-phase
lifecycle, failure unwinding, shutdown ordering, and every command's
behavior are untouched.

- **`app.ServiceProvider` is `app.Module`**, and its `Register`/`Boot`
  methods are `Init`/`Start` (`Shutdown` is unchanged). The surrounding
  surface follows: `WithProviders` -> `WithModules`, `App.Providers` ->
  `App.Modules`, `chain.ProviderRegistry` -> `chain.ModuleRegistry`
  (`Providers()` -> `Modules()`), and the optional `RouteProvider`,
  `MiddlewareProvider`, `EventProvider`, `ScheduleProvider`, and
  `CommandProvider` interfaces become the matching `*Module` interfaces.
  Root aliases track the new names (`velocity.Module`,
  `velocity.ModuleRegistry`, `velocity.RouteModule`, …). Lifecycle error
  labels now read "module init failed" / "chain module start failed".

- **`auth.Guard` is `auth.Scheme`** (`SessionGuard` -> `SessionScheme`,
  `JWTGuard` -> `JWTScheme`), **`auth.Gate` is `auth.Access`**
  (`Manager.Gate()` -> `Manager.Access()`, `GateCallback` ->
  `AccessCallback`, `UserGate` -> `UserAccess`, `NewGate` -> `NewAccess`,
  `ErrGateNotFound` -> `ErrAccessNotFound`), and **`auth.UserProvider` is
  `auth.UserStore`** (`ormauth.Provider` -> `ormauth.Store`,
  `velocity.ORMUserProvider` -> `velocity.ORMUserStore`,
  `authtest.RunUserProviderContractTests` -> `RunUserStoreContractTests`).

  The `Manager` surface follows:
  `RegisterGuard`/`SetDefaultGuard`/`Guard`/`DefaultGuard` become
  `RegisterScheme`/`SetDefaultScheme`/`Scheme`/`DefaultScheme`;
  `SetProvider`/`RegisterProvider`/`DefaultProvider`/`Provider` become
  `SetUserStore`/`RegisterUserStore`/`DefaultUserStore`/`UserStore`;
  `DefaultProviderName` is `DefaultUserStoreName` (value `"default"`
  unchanged); `ErrGuardNotFound` is `ErrSchemeNotFound`. `auth.Config`
  fields `DefaultGuard`/`Guards` are `DefaultScheme`/`Schemes`, and
  `auth.GuardConfig` is `auth.SchemeConfig`. The `Scheme` interface method
  `SetProvider` is `SetUserStore`. `PasswordNeedsRehashEvent.GuardName` is
  `SchemeName`. `contract.AuthManager` de-prefixes to `Allows`/`Authorize`.

- **The event listener opt-in `ShouldQueue()` is `Async()`.**

- **`router.Context.WithErrors`/`WithInput` are `FlashErrors`/`FlashInput`.**

- **The CLI drops its colon grammar for space-separated subcommands.**
  `make:*` becomes `gen *` (`make:provider` -> `gen module`, matching the
  module rename), `route:list` -> `routes`, `migrate:X` -> `migrate X`,
  `db:wipe` -> `db wipe`, `cache:clear` -> `cache clear`, `queue:work` ->
  `queue work`, `schedule:work` -> `schedule work`, and `key:generate` ->
  `key generate`. A subcommand wins over its bare parent (`migrate fresh`
  over `migrate`), while `vel migrate --pretend` and `vel run seed` still
  resolve to the one-word command with the rest passed through.
  `console.MakeProvider`/`MakeProviderOptions` become
  `MakeModule`/`MakeModuleOptions`, and `gen module` emits a `Module`
  (`Init`/`Start`/`Shutdown`); the generated output path
  (`internal/providers`) is unchanged.

**Env:** `AUTH_GUARD` is now `AUTH_SCHEME`, the only environment key
carrying the old word. Scheme map keys (`web`, `session`, `api`, `jwt`),
driver values (`session`, `jwt`), event name strings, and the flash cookie
names are untouched.

### The auth user store is ORM-backed, and the auth model is swappable

`auth.ORMUserProvider` used no ORM and ignored its model. It held a raw
`*sql.DB`, hand-wrote its four statements, and reimplemented placeholder
dialect selection (`ph(n)`) that the ORM grammar already owns. Its
`modelType` field was assigned and never read: every statement hardcoded
`users` and the columns `id, name, email, password, remember_token`, so
`AUTH_MODEL=Admin` produced byte-identical SQL against `users` with no error
and no warning.

- **New package `auth/providers/ormauth`.** `auth` still does not import
  `orm` (the direction is fixed - `auth` sits under router-side packages
  that must not drag the query engine); the new leaf imports both. Every
  read and write goes through `orm.Model[T]`, so table naming, placeholder
  dialect, identifier quoting, and soft-delete scoping are the ORM's.

- **The model is a type parameter, not a config string.** Velocity's ORM
  resolves its table from a compile-time Go type, and Go cannot turn the
  name `"Admin"` into a type - a linker that sees no reference to a type is
  free to discard it. The auth model is therefore chosen in code:

  ```go
  func (m *AuthModule) Start(s *app.Services) error {
      userStore := ormauth.New[models.Admin](
          ormauth.WithIdentifierColumn("username"),
          ormauth.WithPasswordColumn("pass_hash"),
      )
      if err := userStore.Validate(); err != nil {
          return err
      }
      s.Auth.SetUserStore(userStore)
      return nil
  }
  ```

  Swapping the model is editing the type parameter, so a typo is a compile
  error instead of a boot failure. Identifier, password, and remember-token
  columns are options; a model implementing `auth.Authenticatable` is used
  directly, otherwise the columns are mapped onto that interface through ORM
  metadata (`string`, `*string`, and `sql.NullString` carriers).

- **Applications declare the model through the root package**, so no app
  needs to import the user store leaf:

  ```go
  func (m *AppModule) Init(s *velocity.Services) error {
      return velocity.SetAuthModel[models.User](s)
  }
  ```

  `velocity.SetAuthModel[T]` validates the model, inherits the auth
  manager's hasher (preserving the operator-configured bcrypt cost), and
  installs the user store. Column names are `velocity.WithAuth*Column`
  options for models that do not follow the defaults; a model that does
  needs none. `velocity.ORMUserStore[T]` builds a user store without
  installing it.

- **`auth.Manager.SetUserStore` re-points every registered scheme**, so the
  swap works from a module regardless of whether it runs before or after
  `velocity.New` installs the default. Without that fan-out the model would
  appear to change while schemes kept the old user store.

- **Zero-config default preserved.** `velocity.New` installs
  `ormauth.New[ormauth.User]` against `users`, reproducing the historical
  column set, so an application that configures nothing is unaffected. That
  model composes `orm.IDInt` without `orm.Timestamps` so token rotation does
  not stamp `users.updated_at` on every remember-me recall.

- **Model requirement:** because the remember token is persisted through the
  ORM's map-based `Update`, an auth model must declare a mass-assignment
  policy (`Fillable`, `Guarded`, or `AllowAllColumns`). `Store.Validate`
  reports a model with no policy rather than letting it fail on the first
  remember-me login.

**Breaking:**

- `auth.ORMUserProvider`, `auth.NewORMUserProvider`, and
  `auth.NewORMUserProviderForDialect` are removed with no deprecation
  window. Retaining them would preserve the fixed `users` schema and the
  misleading model parameter this change exists to remove. `normalizeID` is
  exported as `auth.NormalizeID` for out-of-package user stores.

- **`AUTH_MODEL` is gone**, along with `auth.ProviderConfig` and
  `auth.Config.Providers`. Velocity authenticates one identity store; the
  env var selected a model by name, which required a name-to-type registry
  that existed purely to serve it. Choosing the model in code removes the
  registry, the string, and every failure mode that came with them
  (unregistered name, stale registration, typo'd value). Remove `AUTH_MODEL`
  from `.env`.

- **The per-scheme user-store config field is gone.** `auth.GuardConfig`
  (now `auth.SchemeConfig`, see the rename entry above) no longer carries a
  `Provider` field; schemes use the single installed user store. Multiple
  schemes (`web`, `api`, `jwt`) over one identity store are unchanged - only
  the multiple-identity-stores config surface is removed.
  `auth.Manager.RegisterUserStore(name, …)` remains as a code-level escape
  hatch for an app that genuinely needs two, and is deliberately not
  reachable from configuration.


### UTC timestamp normalization (storage contract)

Instants are now stored UTC on every persistence path; zones are
presentation. Storage no longer depends on the writer's process timezone
(previously two hosts in different zones writing the same logical row
produced different wall clocks in naive timestamp columns).

- **Managed stamps are UTC.** `created_at`/`updated_at` (struct `Save`) now
  use `time.Now().UTC()`.
- **Bulk `Update` `updated_at` and soft-delete `deleted_at` moved from the
  DB clock (`NOW()`/`CURRENT_TIMESTAMP` sentinel) to the app clock in UTC**,
  collapsing the previous two-clock mix with the `Save` path. Values in
  these columns now come from the application host's clock.
- **All bound `time.Time` args are rebased to UTC** at the driver seam
  (`drivers.NormalizeTimeArgs`): `time.Time`, non-nil `*time.Time`, valid
  `sql.NullTime`, `sql.NamedArg` values (rebased recursively, name
  preserved), and custom `driver.Valuer` types whose `Value()` yields a
  `time.Time`, including raw `Manager.Exec`/`Raw` and the database queue
  and batch tables. Contract change: writers relying on local wall clocks
  in naive columns will observe UTC wall clocks instead. UTC hosts are
  unaffected (identical writes).
- **Scanned timestamps surface in `time.UTC`** across struct scans, `Value`,
  `Pluck`, and m2m pivot extras, with the instant preserved - lib/pq's
  `FixedZone("", 0)` and modernc sqlite's stored-offset locations no longer
  leak into application code.
- **`orm.NOW` / `orm.CurrentTimestamp` are UTC-pinned**: grammars emit
  `(NOW() AT TIME ZONE 'UTC')` (postgres), `UTC_TIMESTAMP()` (mysql),
  `CURRENT_TIMESTAMP` (sqlite; `orm.NOW` now maps there too instead of
  emitting invalid SQL). Contract: DB clock, UTC wall clock. Sentinels now
  also work in INSERT maps (`InsertGetId`/`insertExec` previously bound
  them as string parameters instead of emitting SQL).
- **MySQL DSN never emits the `loc=` codec parameter**;
  `drivers.ConnectionConfig.TimeZone` now uniformly means SESSION timezone
  (postgres `TimeZone=`, mysql `time_zone='...'`) and the sqlite `_loc=`
  param is gone. Codec stays pinned to UTC on all drivers.
- **`DB_TIMEZONE` is back, session-only**: it was removed in 0.32.0 as dead
  config (read but never forwarded). It now flows env ->
  `DBConfig.TimeZone` -> `orm.ManagerConfig.TimeZone` ->
  `drivers.ConnectionConfig.TimeZone` with one cross-driver meaning: the
  database session timezone for in-database functions and
  timestamptz/TIMESTAMP rendering. It never affects storage encoding.
  Ignored by SQLite.
- **New `APP_TIMEZONE` (default `UTC`)**: presentation-only knob applied at
  bootstrap to `time.Local` and scheduler cron evaluation. On non-UTC hosts
  this means cron expressions now evaluate in UTC unless `APP_TIMEZONE`
  says otherwise. Persistence never reads it. Invalid values fail `New()`.
  Empty `Timezone` via programmatic `WithConfig` leaves the process
  timezone untouched.
- **Existing rows written by non-UTC hosts are data, not code**: they are
  not rewritten; their wall clocks keep the original host offset. Storage
  contract details in `docs/configuration.md`; new schemas should prefer
  `TimestampsTz()`.

### 1.0 readiness

- **Sweep 3: configuration surface lock-in.** Inventoried every `os.Getenv` /
  `os.LookupEnv` read site and pinned the 1.0 environment-variable surface.
  Names below are final and will not change without a deprecation cycle.
  Full table in `docs/configuration.md`.
- **New `app/env.go` canonical reader.** `app.Env()`, `app.IsProduction()`,
  `app.IsTesting()`, and the parameterised `app.IsProductionEnv(env)` /
  `app.IsTestingEnv(env)` / `app.IsDevOrTestEnv(env)` helpers replace
  ad-hoc `os.Getenv("APP_ENV")` and `strings.ToLower(env) == "production"`
  checks. Subsystems that already accept `env` as an explicit string
  (auth, csrf, queue signing) keep doing so for cycle-break reasons and
  route through the parameterised helpers. Vocabulary is final: `production`,
  `prod`, `staging` all return `true` from `IsProduction`; `development`,
  `dev`, `test`, `testing`, `local`, and empty return `false`; anything
  unknown is treated as production (fail-secure).
- **`Config.Validate()` on the root `velocity.Config`** plus new
  `DBConfig.Validate()`, `CacheConfig.Validate()`, `QueueConfig.Validate()`
  (root), `StorageConfig.Validate()` (root). Called as the very first
  step of `velocity.New()` so unknown driver names (`CACHE_DRIVER=redus`),
  malformed ports (`APP_PORT=eighty`), and negative timeouts fail fast
  with a typed error wrapping `velocity.ErrInvalidConfig`. Session, CSRF,
  and Crypto validation are intentionally NOT folded into root Validate
  because they have env-aware dev-mode warning paths that would be
  short-circuited; those run later in `New()`.
- **New `Validate()` methods on subsystem Configs that previously had
  none:** `log.LogConfig.Validate()` (reserved for future use),
  `mail.MailConfig.Validate()` (rejects negative `MAX_ATTACHMENT_SIZE`),
  `view.Config.Validate()` (rejects negative SSR timeout), and a fail-fast
  `queue.QueueConfig.Validate()` (database driver requires `DB` +
  `DBDriver`). Wired into `mail.NewMailerWithContext`, `view.NewEngine`,
  and `queue.NewQueueWithContext` as a fail-fast guard before driver
  resolution.
- **Loud startup warning when scheduler runs the default in-memory
  Locker in production.** `velocity.New()` checks
  `app.IsProductionEnv(Config.Env)` after `installSchedulerLocker` and
  emits a `Log.Warn` when the configured Locker is still the
  process-local `scheduler.InMemoryLocker`. Does NOT panic: single-host
  production deployments are a legitimate use case the framework cannot
  distinguish from misconfigured HA.
- **Final 1.0 names marked in-code with `// final: do not rename`
  comments at the read site.** Names tagged: `GRPC_INSECURE` (gRPC TLS
  opt-out), `QUEUE_ACCEPT_UNSIGNED` (queue payload-signing opt-out).
  These read fine but look unusual; pinning them in comments prevents
  drive-by renames during 1.0 surface reviews.
- **Maintenance bypass-cookie `Secure` flag now reads through
  `app.IsDevOrTestEnv(app.Env())`** instead of duplicating a switch
  over `APP_ENV` literals. Same semantics, single source of truth.
- **Queue payload-signing dev/test detection routed through
  `app.IsDevOrTestEnv`** instead of the package-local
  `isDevOrTestEnvProfile`. Removed the duplicate helper.
- **Sweep 3 follow-up: closed the cycle-break excuses with a leaf
  `contract/env.go`.** Moved the env-classification logic from
  `app/env.go` to the `contract` package (stdlib-only leaf) so every
  subsystem can call `contract.IsProductionEnv`, `contract.IsTestingEnv`,
  `contract.IsDevelopmentEnv`, `contract.IsDevOrTestEnv` regardless of
  position in the import graph. `app/env.go` re-exports the helpers for
  callers that already depend on `app`. Vocabulary unchanged.
- **HIGH-1 fix - exceptions handler:** `exceptions.Handler` now routes
  `h.environment` through `contract.IsProductionEnv` instead of literal
  equality, so debug-mode is force-disabled and SetDebug refuses to
  enable when APP_ENV is `production`, `prod`, OR `staging` (and any
  unknown value). Previously only the literal `"production"` triggered
  the gate, leaving `APP_ENV=prod` and `APP_ENV=staging` exposed.
- **HIGH-2 fix - gRPC guards:** `grpc.Server.Build` (TLS + reflection)
  and `grpc.Gateway.Build` (TLS) now route through
  `contract.IsProductionEnv`. `APP_ENV=prod` and `APP_ENV=staging` are
  now refused alongside `production`. The previous
  `TestBuild_AllowsReflectionInStaging` (which asserted the opposite)
  was replaced with `TestBuild_RefusesReflectionInStaging` and a new
  `TestBuild_AllowsReflectionInDevelopment` to keep dev ergonomics
  covered.
- **MEDIUM-1 fix - app.go env relaxation:** the missing-APP_KEY and
  insecure-Session/CSRF branches in `velocity.New` previously matched
  only the literal strings `"testing"` and `"development"`, so
  `APP_ENV=dev` / `APP_ENV=local` failed identically to production.
  Now route through `app.IsTestingEnv` (silent) and `app.IsDevOrTestEnv`
  (warn), with everything else failing closed. The canonical vocabulary
  applies end-to-end.
- **MEDIUM-2 fix - CACHE_DRIVER=database is now rejected at
  `CacheConfig.Validate()`.** `factories.go:initCache` had no database
  branch (no driver implementation exists yet) and silently fell through
  to memory, defeating the fail-fast goal. Documented in
  `docs/configuration.md`; when a database-backed cache driver lands,
  add the wiring AND the case to the validator switch in the same commit.
- **csrf and auth.session env helpers consolidated through `contract`.**
  `csrf.isNonProdEnv` and `auth.isNonProdEnvSession` are deleted;
  `csrf.Config.Validate` and `auth.SessionConfig.Validate` now call
  `contract.IsDevOrTestEnv(env)`. The relaxation surface widens from
  `{testing, development}` to the canonical 5-name set
  `{development, dev, test, testing, local}`, matching every other
  env-gated relaxation in the framework.
- **orm/testing safety panics consolidated through `contract`.**
  `RefreshDatabase` and `TestCase.ensureSafeEnvironment` now panic when
  `contract.IsProductionEnv(APP_ENV)` reports true, so staging / prod
  / typo'd APP_ENV values all refuse to run destructive helpers (closes
  the test-fixture leak that the prior literal `"production"` check
  left open). `contract.IsTestingEnv` is the new "explicitly testing"
  gate.
- **Sweep 3 round-3 - LOW-1: `contract.GetEnv()` canonical reader.** New
  helper in `contract/env.go` returns the lowercased+trimmed APP_ENV
  value. Every `os.Getenv("APP_ENV")` / `envOrDefault("APP_ENV", ...)`
  call site in framework code now routes through it (or through the
  `app.Env()` re-export that delegates to `contract.GetEnv`).
  Touched: `grpc/server.go`, `grpc/gateway.go`,
  `orm/testing/{refresh,testcase}.go`, `cmd_ops.go`, `config.go`. The
  `console/serve.go` Setenv / subprocess-env-format sites now use the
  new `contract.EnvVar` constant so the literal "APP_ENV" lives in
  exactly one file. `app.EnvVar` re-exports `contract.EnvVar`.
- **Sweep 3 round-3 - LOW-2: `ErrNoAppKey` message drift fixed.** The
  error text now reads "APP_KEY is required outside
  development/dev/test/testing/local environments (run `vel
  key:generate`)" with the vocabulary built from
  `contract.NonProdEnvNames()` via `strings.Join` so future
  vocabulary changes flow through automatically. Previously it
  hard-coded "testing or development", which lied to operators who
  had widened to the canonical 5-name set in MEDIUM-1.
- **`contract.NonProdEnvNames()` exported.** Returns the canonical list
  recognised by `IsDevOrTestEnv` so error messages and validator hints
  quote the vocabulary from one source. Order matches the helper's
  switch declaration; do not depend on it beyond display.
- **Acceptance grep status.** After this commit, the audit grep
  `grep -rn 'os\.Getenv.*APP_ENV\|os\.LookupEnv.*APP_ENV\|os\.Setenv.*APP_ENV\|envOrDefault.*APP_ENV\|"APP_ENV='`
  (excluding `_test.go`, `app/env.go`, `contract/env.go`) is empty.
  The looser `grep -rn 'APP_ENV'` still returns matches in godoc
  comments, panic message text, and CHANGELOG itself; these are
  intentional human-readable references to the env-var name and not
  programmatic reads. Surfacing as a decision: the reviewer's
  "must be empty" target was interpreted as "zero programmatic
  reads/writes/format-strings of the env-var", not "zero textual
  occurrences of the string 'APP_ENV' anywhere in the source tree".
- **Sweep 3 round-4 - finding 1:** the signed-URL APP_KEY gate in
  `velocity.New` (formerly `app.go:577`) routed through the same
  literal `"testing"` / `"development"` switch that round-2 already
  fixed elsewhere. Replaced with the canonical `app.IsTestingEnv` /
  `app.IsDevOrTestEnv` helpers so `APP_ENV=dev` / `APP_ENV=test` /
  `APP_ENV=local` behave consistently with the earlier crypto-key
  gate. Same vocabulary across the two APP_KEY gates.
- **Sweep 3 round-4 - finding 2:** `ConfigFromEnv` (config.go) now
  reads `APP_ENV` through `app.Env()` so `Config.Env` is the
  normalised (lowercased + trimmed) value, matching what every
  classifier helper produces internally. Downstream consumers that
  do exact-string compares (the only one was
  `scheduler/job.go:185`'s environment-filter loop) were audited;
  `Scheduler.SetEnv` and `Job.Environments` were both updated to
  normalise their inputs the same way, so the runtime compare is
  case- and whitespace-insensitive on both sides. No consumers broke.
- **Sweep 3 round-4 - finding 3:** `view.Config.Validate` now rejects
  `SSRTimeout <= 0` when `SSREnabled=true`, matching the godoc
  ("must be positive"). The previous `< 0` check let zero through,
  which `net/http` interprets as "no deadline" and would leak the
  per-render call into an indefinite wait.
- **Sweep 3 round-4 - finding 4:** `docs/configuration.md` Notes
  columns for `APP_KEY`, `SESSION_SECURE`, and `CSRF_SECURE` updated
  to quote the canonical 5-name vocabulary from
  `contract.NonProdEnvNames()` instead of the obsolete
  "testing or development" pair.
- **Sweep 3 round-4 - bonus sweep:**
  - `bootstrap.go:validateSessionStoreForProduction` literal
    `case "testing", "development":` switch replaced with
    `contract.IsDevOrTestEnv`. Closes the same vocabulary gap (a
    production gate that didn't fire on `dev`/`test`/`local`).
  - `maintenance.go:mintMaintenanceBypassCookie` godoc comment
    updated to quote the canonical vocabulary (the implementation
    already routed through `app.IsDevOrTestEnv`).
### 1.0 readiness: BREAKING for implementers (Ctx-suffix interface lockdown)

The pre-1.0 surface-freeze sweep promoted ctx-aware `Ctx`-suffixed methods
into five plug-in interfaces. The non-`Ctx` methods stay on each interface
as `// Deprecated:` shims so existing **callers** keep compiling for the
v0.x line, but **external types that previously implemented only the
non-Ctx methods no longer satisfy the interfaces** and will not compile
against the v0.49+ framework. Breakage is intentional, it is what makes
the v1.0 surface freezable, and it surfaces here so operators know what
to expect during the upgrade.

The non-Ctx methods on each interface are deprecated and will be removed
in the v2.0 major. New implementations should write the Ctx variant as
the load-bearing one and keep the non-Ctx as a thin shim.

Recommended migration shape (inverse of the framework's own shim
direction, the implementer's Ctx body does the real work and the non-Ctx
shim delegates with `context.Background()` so existing callers keep
compiling):

```go
func (s *MyStore) GetCtx(ctx context.Context, key string) (any, bool) {
    // real work here, threading ctx into the backing store
}

func (s *MyStore) Get(key string) (any, bool) {
    return s.GetCtx(context.Background(), key)
}
```

The five affected interfaces and the methods that became required:

- **`auth.UserProvider`** (`auth/auth.go`). Added: `FindByIDCtx(ctx, id) (Authenticatable, error)`,
  `FindByCredentialsCtx(ctx, credentials) (Authenticatable, error)`,
  `UpdateRememberTokenCtx(ctx, user, token) error`. `ValidateCredentials`
  is unchanged (pure-CPU bcrypt compare, no ctx threading point).
- **`cache.Store`** (`cache/cache.go`, transitively `cache.Cache`). Added:
  `GetCtx`, `GetStringCtx`, `PutCtx`, `AddCtx`, `ForeverCtx`, `ForgetCtx`,
  `FlushCtx`, `IncrementCtx`, `DecrementCtx`, `ManyCtx`, `PutManyCtx`,
  `HasCtx`. `Remember` and `RememberForever` are unchanged on the interface
  (the callback boundary itself is the only ctx threading point and the
  Manager already exposes `RememberWithContext` at the consumer level).
  `cache.ContextStore` is now a deprecated type alias for `cache.Store`;
  existing type assertions against it keep compiling.
- **`broadcast.Driver`** (`broadcast/broadcast.go`). Added: `BroadcastCtx(ctx, channels, event, data) error`,
  `BroadcastExceptCtx(ctx, channels, event, data, socketID) error`. `GetClients`
  is unchanged (pure in-memory snapshot in the built-in drivers; future
  cluster-aware drivers may expose their own `GetClientsCtx` as an optional
  extension interface rather than promoting it into the core).
- **`storage.Driver`** (`storage/types.go`). Added 18 Ctx methods covering every
  I/O entry point: `PutCtx`, `PutStreamCtx`, `GetCtx`, `GetStreamCtx`,
  `ExistsCtx`, `DeleteCtx`, `CopyCtx`, `MoveCtx`, `SizeCtx`, `LastModifiedCtx`,
  `MimeTypeCtx`, `FilesCtx`, `AllFilesCtx`, `DirectoriesCtx`, `AllDirectoriesCtx`,
  `MakeDirectoryCtx`, `DeleteDirectoryCtx`, `TemporaryURLCtx`. `URL` stays
  non-Ctx (pure string transformation, no I/O).
- **`queue.Driver`** (`queue/types.go`). Added: `SizeCtx(ctx, queue) (int64, error)`,
  `ClearCtx(ctx, queue) error`, `FailedCtx(ctx, job, err, queue) error`. The
  `Push`/`PushDelayed`/`Pop` family was already Ctx-only since the queue
  sweep that predates this lockdown; `Shutdown` already takes ctx and is
  unchanged.

If you maintain a third-party implementation of any of these interfaces,
add the corresponding `*Ctx` method(s) before upgrading. The build will
not compile against v0.49+ otherwise, and the type-assertion error from
`go build` names the exact missing method, which is the canonical
checklist for the migration.

### 1.0 readiness: `router.Context.DB()` returns the stdlib-only contract

`router.Context.DB()` now returns `contract.Database` instead of
`orm.Database`. This is a deliberate narrowing that keeps heavy ORM driver
packages out of `./router`'s dependency graph (`go list -deps ./router`).
The returned method set omits the driver-facing methods `DefaultDriver`,
`Connection`, and `AddConnection`.

**Migration:** handlers that called `c.DB().Connection(...)`,
`c.DB().DefaultDriver()`, or `c.DB().AddConnection(...)` directly recover the
full surface with a single type assertion to the wider `orm.Database`
interface, which still carries those methods:

```go
db, ok := c.DB().(orm.Database) // ok == true; the stored value is *orm.Manager
```

There is no `orm.FromContext` helper for this: `orm` importing `router` would
create an import cycle (`router` → `validation`, whose tests import `orm`), so
the type assertion is the canonical, supported recovery path. No first-party
handler depends on the dropped methods, so most apps need no change.

### Breaking changes

- **Mass assignment is now deny-by-default for map-based writes.** A model that declares neither `Fillable()` (allowlist) nor `Guarded()` (denylist) previously accepted EVERY map key that resolved to a column - `Create(map)`, `FirstOrCreate`, and `UpdateOrCreate` would happily persist attacker-supplied keys like `role` or `is_admin`. Such models now resolve to an empty allowlist: any map-based write that targets an application column fails with a `*orm.MassAssignmentError` naming the model and the rejected keys (an error for developers and logs; the production error renderer already collapses it to a generic 500 for HTTP clients). What is unchanged: struct-based writes (`Create(*T)`, `Save`) are unaffected because the caller constructs the value in code; framework-managed embedded columns (`id`, `created_at`, `updated_at`, `deleted_at`) still bypass policy; and models with a declared policy keep the established silent-skip semantics for disallowed keys. **Migration:** declare `Fillable()` listing the user-writable fields (snake_case Go field names) - the secure choice - or `Guarded()` for a denylist; to genuinely restore the old allow-all behavior, implement the new escape hatch `AllowAllColumns() bool { return true }` on the model (declaring `Guarded()` with an empty slice is equivalent). Removed with the flip: the `orm.StrictMassAssignment` opt-in interface (deny-by-default is now the default; a leftover `StrictMassAssignment()` method on a model still compiles but is ignored) and `orm.SetMassAssignmentWarner` plus the boot-time warning that backed it (nothing to warn about anymore - the insecure default no longer exists).
- **New framework table `job_dedupe` for queue-layer at-most-once enqueue.** Backs the new `queue.DedupeAwarePusher` optional driver interface and its `PushIfNotExistsCtx(ctx, job, dedupeKey, queue...)` method. The `DatabaseDriver` implementation INSERTs into `job_dedupe` under a PRIMARY KEY (postgres `ON CONFLICT DO NOTHING`, mysql `INSERT IGNORE`, sqlite `INSERT OR IGNORE`) inside the same transaction as the `jobs` insert; a row already present in `job_dedupe` is treated as success without touching `jobs`. This is what makes the batch-callback reaper idempotent at the storage layer even when `MarkCallbackDispatched` (the bookkeeping write after a successful push) fails. Deployed apps must run an `ALTER TABLE`-equivalent migration to create the sidecar table before upgrading. Example for Postgres:
  ```sql
  CREATE TABLE IF NOT EXISTS job_dedupe (
    dedupe_key TEXT PRIMARY KEY,
    queue TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
  );
  CREATE INDEX IF NOT EXISTS idx_job_dedupe_created_at ON job_dedupe (created_at);
  ```
  Rows are NOT released on Pop: keeping the dedupe key past the consume boundary is what blocks a stale reaper retry from re-enqueueing the callback after the original execution completed. Long-horizon prune (default 7 days) reclaims orphaned rows. Drivers that do not implement `DedupeAwarePusher` fall back to plain `PushCtx`; callback delivery still works but is at-least-once at the queue layer (the application-level `*_dispatched` flag plus the reaper still serialise duplicate attempts).
- **`job_batches` table schema gained three new columns: `then_dispatched`, `catch_dispatched`, `finally_dispatched`.** All three are NOT NULL BOOLEAN (TINYINT(1) on MySQL, INTEGER on SQLite) defaulting to FALSE / 0. Deployed apps must run an `ALTER TABLE` (or the framework's migration once available) before upgrading. Example for Postgres:
  ```sql
  ALTER TABLE job_batches
    ADD COLUMN then_dispatched BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN catch_dispatched BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN finally_dispatched BOOLEAN NOT NULL DEFAULT FALSE;
  CREATE INDEX idx_job_batches_callback_pending
    ON job_batches (completed_at, then_dispatched, catch_dispatched, finally_dispatched);
  ```
  The columns track which named Then/Catch/Finally callbacks have been successfully PushCtx'd onto the queue. A background reaper goroutine on `queue.DatabaseBatchRepository` retries enqueue every 15s for any row where the corresponding `*_dispatched` flag is still false, which makes cross-process callback delivery durable across transient queue outages and dispatcher-process crashes that race the completion CAS. Existing rows produced by the previous schema take the DEFAULT 0 values and will be retried on the next reaper tick (idempotent: dispatched is monotonic).
- **`orm.ToSnakeCase` (and the auto-derived table/column names that flow from it) now splits acronym->word and digit->word boundaries.** Previously consecutive uppercase letters collapsed into a single token, so `SSHKey` mapped to table `sshkey` (and pluralized to `sshkeys`), `URLPath` mapped to column `urlpath`, `OAuthID` to `oauthid`, `Field1Name` to `field1name`. The new mapping is `ssh_key` / `url_path` / `o_auth_id` / `field1_name` respectively. Apps with acronym-named or digit-bearing model types that relied on the previous mapping must either override `TableName()` on the model to pin the legacy name, or run a migration to rename the table/column to the new convention. The `console` scaffolder (`vel make:model`, `vel make:migration`, etc.) now uses the same algorithm via `orm.ToSnakeCase`, so newly generated migrations match the runtime ORM.
- **`cache/drivers.Lock` interface gains `GetWithErr(ctx) (bool, error)`.** The existing `Get(ctx) bool` collapsed backend errors and contention into a single false return, so callers (notably the scheduler's distributed Locker) could not tell a Redis outage from healthy contention. `GetWithErr` returns the SETNX-equivalent outcome and the backend error separately. `Get` is preserved as a thin wrapper that discards the error, so existing callsites compile unchanged. **Migration for third-party cache drivers:** implement `GetWithErr` on every `Lock` implementation. Drivers that perform no I/O (memory-shaped) can return `(acquired, nil)`. Drivers that perform I/O (redis, database, memcached, ...) must return the underlying client error verbatim. A trivial in-tree migration sketch: `func (l *MyLock) Get(ctx context.Context) bool { acquired, _ := l.GetWithErr(ctx); return acquired }` plus a real implementation of `GetWithErr` that calls the backend and returns its error. The framework's built-in `MemoryLock` and `RedisLock` are migrated.
- **`scheduler.Logger` interface gains `Warn(msg string, kvs ...any)`.** Required so the scheduler can route Locker.Acquire backend errors (Redis outage, AUTH failure, ...) to Warn-level logs while leaving healthy contention at Debug. The framework's `log.Logger` already satisfies the new shape; third-party adapters must add a `Warn` method.
- **DB-backed validation implementation moved into the new `validation/dbrules` subpackage** so the core `validation` package no longer imports `orm`, `database/sql`, or any SQL driver. Adopters who only use the standard (orm-free) rule set no longer transitively pull those dependencies (and their CGO / cross-compile constraints) into their build. The old `validation.*` names below are **retained as deprecated, orm-free compatibility shims** (they reach the database structurally via reflection), so existing callers keep compiling and working unchanged; new code should prefer the `dbrules.*` variants:
  - `validation.CheckWithDB` -> `dbrules.CheckWithDB`
  - `validation.CheckWithDBW` -> `dbrules.CheckWithDBW`
  - `validation.CheckDataWithDB` -> `dbrules.CheckDataWithDB`
  - `validation.CheckDataWithDBCtx` -> `dbrules.CheckDataWithDBCtx`
  - `validation.UniqueRule` / `validation.UniqueRuleCtx` -> `dbrules.UniqueRule` / `dbrules.UniqueRuleCtx`
  - `validation.ExistsRule` / `validation.ExistsRuleCtx` -> `dbrules.ExistsRule` / `dbrules.ExistsRuleCtx`
  - `validation.AsValidationError` -> `dbrules.AsValidationError`

  **Migration (optional but recommended):** import `github.com/velocitykode/velocity/validation/dbrules` and replace the `validation.` qualifier with `dbrules.` on the calls above. No argument or return-type changes are required; the `dbrules.*` functions are thin wrappers over the new core seam `validation.CheckWithRulesW` / `validation.CheckDataWithRules`, which register DB-backed rule handlers on the orm-free engine without the core taking an orm dependency. The retained `validation.*` deprecated shims keep old call sites compiling and working; their `db` parameter is now typed `any` (an `orm.Database` satisfies it) and they reach `*sql.DB` via reflection. The one behavioral nuance: `validation.AsValidationError` matches UNIQUE violations by error-string only, whereas `dbrules.AsValidationError` adds typed `pq.Error` / `mysql.MySQLError` fast paths. Callers that used only orm-free entry points (`validation.Check`, `CheckW`, `CheckData`, `ExtractRequestData`, ...) are unaffected.

### Added

- **`auth.Config.TrustedProxies` (`AUTH_TRUSTED_PROXIES` env var):** comma-separated list of IPs/CIDRs whose forwarded headers (`Forwarded` / `X-Forwarded-For` / `X-Real-IP`) may be honoured when deriving the client IP for the login throttler and the session audit trail. Default is empty (no proxies trusted, secure default; XFF spoofing is fully ignored). Configure to match your load balancer / reverse proxy topology, e.g. `AUTH_TRUSTED_PROXIES=10.0.0.0/8,192.168.0.0/16`. Entries are parsed via the new `internal/clientip.ParseCIDRs` helper and propagated to every guard via the new `auth.TrustedProxiesReceiver` interface; guards registered after `Manager.SetTrustedProxies` inherit the list automatically.
- **`auth.TrustedProxiesReceiver`**: optional interface a `Guard` implements so `Manager.SetTrustedProxies` can plumb the parsed proxy network list through. `SessionGuard` and `JWTGuard` implement it; pure bearer-token guards can leave it unimplemented.
- **`auth.Manager.SetTrustedProxies([]*net.IPNet)` / `Manager.TrustedProxies() []*net.IPNet`**: install / read the parsed proxy network list. The setter takes a defensive copy so post-install caller mutation cannot affect the manager's view, and propagates the list to every registered guard implementing `TrustedProxiesReceiver`.
- **`internal/clientip` package**: framework-internal single source of truth for "who is the real client?" given an `*http.Request` and a trusted-proxy network list. Resolves `Forwarded` (RFC 7239) > `X-Forwarded-For` (right-most-of-trusted) > `X-Real-IP` (single-value only), strips ephemeral TCP ports, handles IPv4-in-IPv6 brackets, and refuses to trust headers from untrusted `RemoteAddr`. `auth/throttle.go` and `auth/drivers/guards/session.go` now both call it; the exceptions logger and other adopters should adopt it next so the framework has one IP-extraction policy, not three.
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

- **CBC ciphers now bind AAD; the flash-cookie CBC fallback is removed (security: OWASP V2-12).** `crypto.Encryptor.EncryptBytesWithAAD` / `DecryptBytesWithAAD` no longer return `ErrInvalidCipher` on CBC modes: the AAD is mixed into the encrypt-then-MAC HMAC input under a dedicated domain prefix with an explicit length frame (`HMAC(hmacKey, "velocity-aad\x00" || be64(len(aad)) || aad || iv || ciphertext)`), giving CBC the same AAD semantics GCM gets from its AEAD tag (wrong aad, missing aad, and plain-vs-AAD payload mixing all collapse to `ErrAADMismatch`; `PreviousKeys` rotation is honoured; nil/empty aad stays equivalent to no aad). Existing CBC ciphertexts are unaffected: the plain v1 and legacy v0 MAC framings are unchanged and pinned by fixture tests. Consequently `router.SealFlash` / `OpenFlash` drop their CBC fallback to the non-AAD `EncryptBytes` / `DecryptBytes` path, which had silently discarded the per-cookie AAD label on CBC-pinned apps and let a `_velocity_old` ciphertext verify as `_velocity_errors` (and vice versa). Operational note for CBC-pinned apps: flash cookies sealed by the old fallback (5-minute MaxAge) stop decoding across the upgrade; the only effect is one lost flash render in that window. to the router-level trusted-proxy list (`Router.TrustedProxies`), not a replacement.** Previously, a per-middleware list silently overrode the router-level list, so `Router.Use(router.Throttle(...))` without `WithTrustedProxies` fell back to "no proxies trusted" and bucketed every client behind the load balancer into the same limit. Now, `RateLimitByIP` resolves the client IP through `internal/clientip.Extract` over the UNION of the router-level set (`Context.TrustedProxyNets()`) and the per-middleware extras. Configuring trust at the router level is the recommended pattern; the per-middleware option remains as an escape hatch for middleware-specific extras. New `(*router.TrustedProxies).IPNets()` returns the parsed nets for callers that need the `[]*net.IPNet` shape (e.g. for `clientip.Extract`). New `router.Context.TrustedProxyNets()` returns the parsed router-level proxy list installed for the current request, suitable for passing to `internal/clientip.Extract` from custom middleware.
- **`auth.Manager.RevokeAllSessions` return semantics.** Previously this method returned `nil` whenever the underlying `ServerSessionStore.DeleteAllForUser` succeeded, regardless of any subsequent work. It now also runs every registered guard's `RememberTokenClearer` and, if any clearer fails, returns an error wrapping `auth.ErrRememberClearPartial` joined with the underlying causes (via `errors.Join`). The store delete is still the load-bearing security action and runs first; clearers run only after it succeeds. Adopters with `if err := mgr.RevokeAllSessions(ctx, uid); err != nil { return err }` will now see clearer failures they did not see before. To preserve the prior best-effort behavior, gate the return on the sentinel: `if err := mgr.RevokeAllSessions(ctx, uid); err != nil && !errors.Is(err, auth.ErrRememberClearPartial) { return err }`.

### Fixed

- **Remember-cookie revival now writes the fresh XSRF-TOKEN cookie after rotating the CSRF token.** `SessionGuard`'s silent-revival path rotated the per-session CSRF token (deleting the token bound to the pre-revival session id) but, unlike `Login`, never emitted the replacement cookie, so the SPA kept echoing a stale value and its next state-changing request 419'd until a later safe-method response happened to re-sync it. The revival path now calls `WriteXSRFCookie` with the post-rotation session id, mirroring `Login`.
- **`auth.ORMUserProvider` SQL is now placeholder-dialect aware.** All four provider statements (`FindByID`, `FindByCredentials`, `UpdateRememberToken`, `CompareAndSwapRememberToken`) hardcoded PostgreSQL `$N` placeholders, which are a syntax error on MySQL; combined with the fail-closed rotate-on-use recall, every remember-cookie recall on MySQL was silently rejected. New `auth.NewORMUserProviderForDialect(db, model, hasher, dialect)` selects `$N` for `"postgres"` and `?` otherwise; `velocity.New` wires `DB_CONNECTION` through automatically. The plain `NewORMUserProvider` constructor keeps the historical PostgreSQL syntax.
- **The per-identifier login-throttle dimension is now verify-first, closing a remote account-lockout DoS.** The identifier bucket aggregates one account's failures across ALL source IPs, and it was checked before the password, so an attacker spraying a victim's email from throwaway IPs (20 wrong passwords / 60s by default) locked the real user out from every IP. Guards (`SessionGuard.Attempt`, `JWTGuard.Attempt`) now deny pre-check only on the pair and per-IP dimensions; an over-cap identifier bucket runs the (timeboxed) credential check and denies only wrong-credential attempts, so the account holder with the correct password is never locked out, while distributed guessers keep receiving the uniform `auth.ErrLoginThrottled`. Pre-check denials are padded to the attempt floor so the tripped dimension is not distinguishable by timing. Previously `drivers.MemoryStore` kept entries in an unbounded map: the periodic sweep removed only expired items, `Forever` entries lived forever, and any attacker-influenceable key shape (per-user, per-IP, per-request-derived) could grow the map until the process OOMed. The store now holds at most `DefaultMaxEntries` (1,000,000) entries; inserting past the cap evicts an approximately least-recently-used entry (sampled LRU: hits stamp an atomic access sequence, eviction samples up to 16 map entries and removes the stalest, preferring already-expired ones; stores at or below the sample size get exact LRU). Reads stay on the shared read lock on bounded and unbounded stores alike, so cache hits run concurrently. Configure via `CACHE_MEMORY_MAX_ENTRIES` (root `CacheConfig.MemoryMaxEntries` -> `cache.StoreConfig.MaxEntries` -> new `drivers.WithMaxEntries` option): `0` = default cap, positive = explicit cap, negative = unlimited (documented escape hatch restoring the old behaviour). `Forever` entries never expire but ARE evictable at cap; replacing an existing key never evicts; `Add`/`Increment` atomicity is unchanged (eviction runs under the same mutex). `NewMemoryStore` gained variadic `MemoryOption`s, so existing call sites compile unchanged but are now bounded by default.

- **`exceptions.Handler` no longer honours `X-Forwarded-For` / `X-Real-IP` unconditionally (security: C-05 follow-up).** Previously `exceptions/middleware.go:getClientIP` took the left-most XFF entry from every request, so any direct-internet client could spoof the IP recorded on `ExceptionContext.IP` (CWE-345 log poisoning / forensics evasion). It also took the LEFT-most where the rate-limit path takes RIGHT-most-of-trusted, so the same request was attributed to two different IPs across subsystems. `Handler` now carries a `trustedProxies []*net.IPNet` field set via the new `exceptions.WithTrustedProxies(...)` option or the new `Handler.SetTrustedProxies([]*net.IPNet)` runtime setter; `ErrorHandler` routes through `internal/clientip.ExtractString` so the audit log uses the same secure resolution as the throttle / rate-limit / session-audit layers. `velocity.New` wires the deployment list from `Config.Auth.TrustedProxies` automatically.
- **`router.RateLimitByIP` now consumes the router-level trusted-proxy list (security: C-05 follow-up).** Previously it had its own per-middleware `WithTrustedProxies` parsed independently of `Router.TrustedProxies`; operators who configured trust once at the router level lost it at the rate-limit layer, silently fell back to "no proxies trusted", and every client behind the LB shared one bucket. `RateLimitByIP` now reads `Context.TrustedProxyNets()` (the parsed router-level list) on every request and unions it with the per-middleware extras. `extractIP` and the throttle key both route through `internal/clientip.Extract`, so all three subsystems (router rate limit, auth login throttle, exceptions audit log) agree on "who is the real client?".
- **`auth.ThrottleKey` no longer keys by ephemeral TCP port and now resolves the real client IP behind trusted proxies (security: C-05).** The previous implementation used `r.RemoteAddr` verbatim, which is `host:port`. The port rotates per TCP connection, so every login attempt produced a fresh throttle key, effectively disabling the limiter. Behind any load balancer, every client also shared one bucket (the LB IP), so legitimate users got DoS-throttled while attackers spread across forwarded clients. The key is now a length-bounded SHA-256 hex digest over `(normalised_identifier, client_ip)`: the identifier is `strings.TrimSpace` + NFKC + `strings.ToLower` (so `Victim@example.com` and `VICTIM@example.com` hit the same bucket), capped at 254 bytes; the IP is resolved via the new `internal/clientip.Extract` honouring `auth.Config.TrustedProxies`. The `"|"` separator footgun (`alice|10.0.0.5` colliding with `alice` from `10.0.0.5`) is closed both by `\x00` separation and by hashing. The function signature gained a `trustedProxies []*net.IPNet` parameter; the only in-tree callers are the session and JWT guards, which now plumb their per-guard list automatically. The session guard's `clientIP(r)` helper (used for the audit-trail IP recorded on `Login`) now delegates to the same `clientip.Extract` so the throttle, audit log, and per-IP limiter all agree. Default trusted-proxy list is empty (forwarded headers untrusted); set `AUTH_TRUSTED_PROXIES` to match your topology.
- **`auth.SessionGuard` now consults `ServerSessionStore` on every request when one is installed.** Previously `Manager.RevokeSession` / `RevokeAllSessions` deleted the store row but `SessionGuard.Check` / `User` only validated the encrypted cookie, so a "logged out" browser stayed authenticated until the cookie's TTL elapsed. The guard now performs `store.Get(sessionID)` on every authenticated request; a missing or expired record returns the new `auth.ErrSessionRevoked` sentinel and `Check` returns `false`. New `SessionGuard.CheckWithError(r) (bool, error)` lets middleware distinguish revoked from expired from no-cookie for UX. `Login` writes the session record (id, user id, created/last-seen/expires, IP, User-Agent) to the store; `Logout` deletes it. `LastSeenAt` write-back is debounced to one `Put` per 60s to avoid amplifying read RTTs into double round-trips. Cookie-only behavior is preserved when no store is installed; `Manager.SetServerSessionStore` propagates to every guard implementing the new `auth.ServerSessionStoreReceiver` interface (and to guards registered after the store is installed). IP is captured from `r.RemoteAddr` with the `:port` suffix stripped via `net.SplitHostPort`; `X-Forwarded-For` / trusted-proxy support is deferred.
- **`auth.Manager.RevokeAllSessions` also clears the user's remember-me token.** Without this, a "sign out everywhere" admin action would still let the revoked browser resurrect a fresh session on the next request via its remember cookie. Manager now walks every registered guard and calls `ClearRememberTokensForUser` on guards implementing the new `auth.RememberTokenClearer` interface; `SessionGuard` implements it by calling `UserProvider.UpdateRememberToken(user, "")`. `Manager.RevokeSession` (single-session) intentionally does NOT clear the remember token, since remember tokens are per-user and wiping one would log the user out across every device; if you need that, call `RevokeAllSessions`. (See **Changed** above for the new partial-failure return-error contract.)
- **`Pluck()` now honors `Distinct()`**. Previously the `SelectQuery{}` literal in `Pluck` omitted `Distinct`, so `Model[User]{}.Distinct().Pluck("role")` silently returned duplicates. Asserted across all three driver grammars.
- **WHERE compilation no longer mis-binds nested AND/OR**. The flat-slice condition compiler is replaced by a recursive walk that emits parens around grouped predicates.
- **Side fix in postgres `CompileUpdate` / `CompileDelete`**: `WhereIn(...).Update(...)` and `WhereIn(...).Delete()` previously emitted `col IN $1` and bound the slice as a single param, producing a runtime driver error or silent corruption. The shared `compileConditions` helper now expands `IN` / `NOT IN` / `BETWEEN` / `NOT BETWEEN` correctly in UPDATE and DELETE WHERE clauses on every driver. Adopters who routed those queries through `NewRawQuery` to work around the breakage can drop the workaround.
- **`Model[T]` no longer mandates an `updated_at` column for read paths**. The `query.Update` injection of `updated_at` is now gated on the model actually having that field (cached reflection lookup), so embedding `Model[T]` in a struct without a `UpdatedAt` field no longer breaks `Update`. Tables that genuinely need append-only semantics should still use `ImmutableModel[T]` for the API guarantees.
- **Scheduler now installs the cache-backed `Locker` only when the underlying cache store actually implements the cache `Lock` primitive.** Previously `velocity.New` installed `cacheLocker` for any non-memory `CACHE_DRIVER` value. But the `file` cache driver does not implement `Lock`, and the `database` driver is not yet implemented at all, so `cacheLocker.Acquire` would return a misconfiguration error, the scheduler would treat that as `ErrLockHeld`, and every `WithoutOverlapping` / `OnOneServer` job would be silently skipped forever on a fresh `CACHE_DRIVER=file` deployment. `installSchedulerLocker` now probes the default store via a structural type assertion and falls back to the process-local `scheduler.InMemoryLocker` with a `WARN` log when locks are unsupported. **Capability matrix:** `redis` for full cross-host distributed locking; `memory` for in-process locking (matches the cache scope); `file` / `database` for in-process locking only, with a WARN at boot. Multi-host operators relying on `WithoutOverlapping` / `OnOneServer` must use the `redis` driver (or supply a custom `scheduler.SetLocker(...)`).
- **Scheduler's cache-backed `Locker` no longer collapses Redis backend errors into "lock held".** `RedisLock.Get(ctx) bool` returned false for both contention (SETNX returned false) and backend failure (network reset, AUTH/NOAUTH, OOM, READONLY during failover). `cacheLocker.Acquire` wrapped every false as `scheduler.ErrLockHeld`, and the scheduler treated that as healthy contention, silently skipping every guarded job for the duration of a Redis outage with no operator-visible signal. The new `cache.Lock.GetWithErr(ctx) (bool, error)` returns the SETNX outcome and the backend error separately; `cacheLocker.Acquire` maps `(false, nil) -> ErrLockHeld` (Debug log, quiet) and `(any, err != nil) -> wrapped backend error` (Warn log naming the underlying cause). `cacheLocker` fencing-token docstring is also corrected: tokens are process-local only (each process starts the counter at zero), not cross-process monotonic; the field is informational, and real distributed write-side fencing requires a separate primitive (out of scope here).

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
- **`APP_KEY` is mandatory outside the canonical dev/test profiles.** `velocity.New` returns `velocity.ErrNoAppKey` when the key is missing in any other environment (including unset `APP_ENV`, which most production deployments have). Test profiles (`test`, `testing`) bypass the check silently; the wider dev profiles (`development`, `dev`, `local`) bypass it with a boot-time `a.Log.Warn(...)` so developers see that crypto is disabled without being blocked. Generate a key with `vel key:generate`. **Update (Unreleased / sweep 3):** this 0.32.0 entry originally said only "`APP_ENV=testing` and `APP_ENV=development`"; sweep 3 widened the relaxation surface to the canonical 5-name set `{development, dev, test, testing, local}` per `contract.NonProdEnvNames()`.
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
- **Session and CSRF cookie configs are validated at boot.** `auth.SessionConfig.Validate(env)` and `csrf.Config.Validate(env)` enforce: `HttpOnly=true` (unless the new `AllowJSAccess=true` opt-in), `Secure=true` outside the canonical dev/test profiles, non-zero `SameSite`, and `SameSite=None` requires `Secure=true`. `velocity.New()` fails boot on violation in production, logs `a.Log.Warn` in development, stays silent in testing. **Migration:** apps that shipped with `SESSION_SECURE=false` / `CSRF_SECURE=false` / `SESSION_HTTP_ONLY=false` / `CSRF_HTTP_ONLY=false` in production will fail to boot until the config is fixed. Recommended fix is to set those to their secure defaults; the `AllowJSAccess=true` escape hatch exists for rare cases that genuinely need client-side access. **Update (Unreleased / sweep 3):** the dev/test relaxation surface widened from `{testing, development}` to the canonical 5-name set `{development, dev, test, testing, local}` per `contract.NonProdEnvNames()`; this entry originally said only "testing or development".
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
- **`velocity.ErrNoAppKey`**: sentinel returned by `New` when `APP_KEY`/`CRYPTO_KEY` is unset outside the canonical dev/test profiles. Test profiles (`test`, `testing`) bypass the check silently; the wider dev profiles (`development`, `dev`, `local`) bypass it with a boot-time WARN so local workflows aren't blocked. **Update (Unreleased / sweep 3 round 3):** this 0.32.0 entry originally said only "`APP_ENV=testing` / `APP_ENV=development`"; the canonical set per `contract.NonProdEnvNames()` is `{development, dev, test, testing, local}`.
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
  - `view.DefaultViewConfig`: unused. **Update (Unreleased / sweep 3 round 4+5):** this 0.32.0 entry also listed `view.Config.Validate` as unused, but sweep 3 reintroduced it. `view.Config.Validate` now rejects `SSRTimeout <= 0` when `SSREnabled=true` and is chained from `velocity.Config.Validate` unconditionally (see `config.go`), so the SSR fast-fail fires even when no view engine is constructed.
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

**Mail (`mail/message.go`, `mail/init.go`, `notification/mail/mail.go`)**
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
- **Cookie config**: `auth.SessionConfig` and `csrf.Config` now carry `Validate(env string) error` methods enforcing `HttpOnly=true` (unless `AllowJSAccess=true` opt-in), `Secure=true` outside the canonical dev/test profiles, non-zero `SameSite`, and `SameSite=None => Secure=true`. Wired into `velocity.New()`: production boot fails on violation, development logs a warning, testing is silent. See Migration. **Update (Unreleased / sweep 3):** the relaxation set widened from `{testing, development}` to the canonical 5-name set `{development, dev, test, testing, local}` per `contract.NonProdEnvNames()`.

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
