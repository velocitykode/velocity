# Configuration Surface

Every environment variable read by the framework. Names below are the 1.0
surface and will not change without a deprecation cycle.

Read sites are inventoried via:

```sh
grep -rn 'os\.Getenv\|os\.LookupEnv' --include='*.go' | grep -v _test.go
```

Sentinel reader for `APP_ENV`: `app.Env()` / `app.IsProduction()` /
`app.IsTesting()` / `app.IsDevOrTestEnv()` in `app/env.go`. Every
"is this production?" check inside the framework routes through these
helpers (or accepts an explicit env string at sub-package boundaries
that cannot import `app`).

## Notation

- "Required in prod?" yes means the framework refuses to boot (or fails
  closed at first use) when the variable is unset in a production env.
- "Security impact" highlights what an attacker gains when the value is
  missing, weak, or attacker-controlled.

## App

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `APP_ENV` | `app/env.go`, `config.go`, `maintenance.go`, `grpc/server.go`, `grpc/gateway.go`, `orm/testing/*` | `development` | recommended | drives every "is prod?" gate (cookies Secure, queue signing, gRPC TLS) | Canonical: route reads through `app.Env()` / `app.IsProduction()` |
| `APP_DEBUG` | `config.go` | `false` | no (force-disabled in prod by `exceptions.Handler`) | leaks stack traces and source if enabled in prod | |
| `APP_PORT` | `config.go`, `cmd_ops.go` | `4000` | no | none | |
| `APP_KEY` | `config.go`, `maintenance.go` | empty | YES (errors out unless `APP_ENV` is one of `development`, `dev`, `test`, `testing`, `local` per `contract.NonProdEnvNames()`) | crypto/session/CSRF/queue-signing all key off this; weak key compromises every secret | Generate via `vel key:generate` |
| `APP_TIMEZONE` | `config.go`, `app.go` | `UTC` | no | none | IANA name. PRESENTATION only: applied to `time.Local` and scheduler cron evaluation at bootstrap. Persistence never reads it (see "Timestamp storage contract" below). Invalid value fails `New()`. Programmatic `WithConfig` with an empty `Timezone` leaves the process timezone untouched. |

## Database

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `DB_CONNECTION` | `config.go` | empty | only if DB used | none | one of `sqlite`, `postgres`, `mysql` |
| `DB_HOST` | `config.go` | `127.0.0.1` | only if DB used | binding 0.0.0.0 inadvertently | |
| `DB_PORT` | `config.go` | per-driver | only if DB used | none | |
| `DB_DATABASE` | `config.go` | empty | only if DB used | none | |
| `DB_USERNAME` | `config.go` | empty | only if DB used | SENSITIVE | |
| `DB_PASSWORD` | `config.go` | empty | only if DB used | SENSITIVE | |
| `DB_CHARSET` | `config.go` | empty | no | none | |
| `DB_SSL_MODE` | `config.go` | empty | recommended | unencrypted DB traffic in prod | postgres only; defaults to `prefer` at driver layer |
| `DB_MYSQL_TLS` | `config.go` | empty | recommended | unencrypted DB traffic in prod | mysql only |
| `DB_TIMEZONE` | `config.go` | empty | no | none | database SESSION timezone only (postgres `TimeZone=`, mysql `time_zone='...'`); never affects storage encoding, which is unconditionally UTC (see below); ignored by sqlite |
| `DB_MAX_IDLE_CONNS` | `config.go` | `10` | no | none | |
| `DB_MAX_OPEN_CONNS` | `config.go` | `100` | no | none | |
| `DB_CONN_MAX_LIFETIME` | `config.go` | `3600s` | no | none | seconds |
| `DB_LOG_QUERIES` | `config.go` | `false` | no | logs SQL with bound args if enabled | |
| `DB_SLOW_QUERY_THRESHOLD` | `config.go` | `0` | no | none | duration syntax |

### Timestamp storage contract

**Instants are stored UTC; zones are presentation.** Concretely:

- **Managed lifecycle columns** (`created_at`, `updated_at`, `deleted_at`) are stamped app-side with `time.Now().UTC()` - one clock (the app's), one zone (UTC), on every write path (struct `Save`, map `Update`, soft `Delete`).
- **Every bound `time.Time` argument** (including `*time.Time`, valid `sql.NullTime`, `sql.Named(...)` values, custom `driver.Valuer` types whose `Value()` yields a `time.Time`, raw `Manager.Exec`/`Raw` args, and queue tables) is rebased to UTC at the driver seam (`drivers.NormalizeTimeArgs`) before encoding, so user-supplied times also land as UTC wall clocks in naive `timestamp`/`DATETIME` columns. `timestamptz` columns are instant-preserving, so the rebase is a no-op for them.
- **Scanned timestamps surface located in `time.UTC`** (struct scans, `Value`, `Pluck`, pivot extras), so round-trips are stable across hosts regardless of what location the underlying driver returns.
- **SQL sentinels** `orm.NOW` / `orm.CurrentTimestamp` mean "DB clock, UTC wall clock" in both Update and Insert maps: grammars emit `(NOW() AT TIME ZONE 'UTC')` (postgres), `UTC_TIMESTAMP()` (mysql), `CURRENT_TIMESTAMP` (sqlite). Caveat: into a `timestamptz` column under a hand-set non-UTC session timezone the naive UTC value is misread - use an app-side stamp or raw `NOW()` there.
- **Session timezone** (`DB_TIMEZONE` -> `drivers.ConnectionConfig.TimeZone`; postgres `TimeZone=`, mysql `time_zone='...'`) affects in-database functions and `timestamptz`/`TIMESTAMP` rendering only - never the encoding of bound time values. The MySQL `loc=` codec parameter is never emitted; the go-sql-driver default `Loc=UTC` is part of the contract.
- **DB-side column defaults** (`DEFAULT CURRENT_TIMESTAMP` from migrations) follow the database session/server timezone, not this contract - prefer managed stamps. For new schemas prefer `TimestampsTz()` (timezone-aware columns) over `Timestamps()`.
- **Writers that hold their own `*sql.DB`** (bypassing the ORM driver seam) must stamp `time.Now().UTC()` themselves; the framework's queue and outbox tables do.
- Rows written by non-UTC hosts **before** this contract are data, not code: they are not rewritten, and their wall clocks remain skewed by the original host offset.

`APP_TIMEZONE` is deliberately outside this contract: it configures presentation (formatting, scheduler cron evaluation), never storage.

## Session

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `SESSION_NAME` | `config.go` | `velocity_session` | no | none | |
| `SESSION_LIFETIME` | `config.go` | `120` | no | longer-lived stolen cookies | minutes |
| `SESSION_PATH` | `config.go` | `/` | no | scope of cookie | |
| `SESSION_DOMAIN` | `config.go` | empty | no | scope of cookie | |
| `SESSION_SECURE` | `config.go` | `true` | YES | cookie sent over HTTP | reject unless `APP_ENV` names a dev/test profile (`development`, `dev`, `test`, `testing`, `local` per `contract.NonProdEnvNames()`) |
| `SESSION_HTTP_ONLY` | `config.go` | `true` | YES (unless opt-in) | XSS can steal session | |
| `SESSION_SAME_SITE` | `config.go` | `lax` | YES (must be set) | CSRF | one of `strict`, `lax`, `none` |

## Auth

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `AUTH_GUARD` | `config.go` | empty | optional | none | enables `web`/`api` guards |
| `AUTH_MODEL` | `config.go` | `User` | no | none | |
| `AUTH_TRUSTED_PROXIES` | `config.go` | empty | YES if behind proxy | XFF spoofing -> throttle bypass + bogus audit IPs | comma-separated IP/CIDR; empty = trust nothing (secure default) |
| `AUTH_ATTEMPT_FLOOR` | `config.go` | `200ms` | no | login timing side-channel | raise when `HASH_BCRYPT_COST` >= 12 |
| `HASH_BCRYPT_COST` | `config.go` | `10` | recommended >=12 | weak password hashes | |
| `AUTH_JWT_ALGO` | `config.go` | `HS256` | only if JWT used | weak signature alg | |
| `AUTH_JWT_SECRET` | `config.go` | empty | YES (JWT) | forge JWTs | SENSITIVE |
| `AUTH_JWT_TTL` | `config.go` | `60` | no | longer-lived stolen tokens | minutes |
| `AUTH_JWT_REFRESH_TTL` | `config.go` | `20160` | no | longer-lived stolen refresh tokens | minutes (14 days) |
| `AUTH_JWT_BLACKLIST_ENABLED` | `config.go` | `true` | YES | revocation does nothing if false | |

## CSRF

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `CSRF_TOKEN_LIFETIME` | `config.go` | from `csrf.DefaultConfig()` | no | longer-lived stolen tokens | duration |
| `CSRF_HEADER` | `config.go` | from default | no | none | |
| `CSRF_FORM_FIELD` | `config.go` | from default | no | none | |
| `CSRF_COOKIE_NAME` | `config.go` | from default | no | none | |
| `CSRF_SESSION_COOKIE` | `config.go` | matches `SESSION_NAME` | recommended | CSRF token keyed off wrong cookie -> always 419 or always-pass | |
| `CSRF_SAME_SITE` | `config.go` | `lax` | YES (must be set) | CSRF | |
| `CSRF_SECURE` | `config.go` | `true` | YES | token cookie over HTTP | reject unless `APP_ENV` names a dev/test profile (`development`, `dev`, `test`, `testing`, `local` per `contract.NonProdEnvNames()`) |
| `CSRF_HTTP_ONLY` | `config.go` | `true` | YES (unless opt-in) | XSS can steal CSRF token | |
| `CSRF_SINGLE_USE` | `config.go` | `false` | no | none | |
| `CSRF_ERROR_MESSAGE` | `config.go` | default | no | none | |
| `CSRF_WRITE_XSRF_COOKIE` | `config.go` | `true` | no | none | |
| `CSRF_XSRF_COOKIE_NAME` | `config.go` | from default | no | none | |

## Cache

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `CACHE_DRIVER` | `config.go` | `memory` | recommended (redis on HA) | per-process state on multi-host | one of `memory`, `file`, `redis`. `database` is reserved but not implemented; setting it is rejected at boot |
| `CACHE_PREFIX` | `config.go` | `velocity_cache` | no | none | |
| `CACHE_PATH` | `config.go` | empty | only if file driver | path traversal if attacker-controlled | |
| `CACHE_MEMORY_MAX_ENTRIES` | `config.go` | `0` (= 1,000,000) | no | unbounded cache map is OOM-able via attacker-influenceable keys | memory driver entry cap with LRU eviction. `0` = default cap, negative = unlimited (escape hatch) |
| `REDIS_HOST` | `config.go` | `127.0.0.1` | only if redis | binding 0.0.0.0 inadvertently | |
| `REDIS_PORT` | `config.go` | `6379` | only if redis | none | |
| `REDIS_PASSWORD` | `config.go` | empty | YES (redis in prod) | unauthenticated redis | SENSITIVE |
| `REDIS_DATABASE` | `config.go` | `0` | no | none | |
| `REDIS_TLS` | `config.go` | `false` | YES (redis in prod) | unencrypted redis traffic | |

## Queue

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `QUEUE_DRIVER` | `config.go` | `memory` | YES (redis or database on HA) | jobs lost / per-process state on multi-host | |
| `QUEUE_REDIS_HOST` | `config.go` | `localhost` | only if redis | binding 0.0.0.0 inadvertently | |
| `QUEUE_REDIS_PORT` | `config.go` | `6379` | only if redis | none | |
| `QUEUE_REDIS_PASSWORD` | `config.go` | empty | YES (redis in prod) | unauthenticated redis | SENSITIVE |
| `QUEUE_REDIS_DB` | `config.go` | `0` | no | none | |
| `QUEUE_SIGNING_KEY` | `config.go` | empty (falls back to APP_KEY) | YES | attacker who writes to queue can run arbitrary jobs in worker | SENSITIVE; HMAC key for payload signing |
| `QUEUE_ACCEPT_UNSIGNED` | `factories.go` | `false` | NO (loud opt-in only) | disables signing fail-closed; warned in logs | final: do not rename |

## Storage

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `STORAGE_DRIVER` | `config.go` | `local` | no | none | |
| `FILESYSTEM_LOCAL_ROOT` | `config.go` | `./storage/app` | no | path traversal if attacker-controlled | |
| `AWS_BUCKET` | `config.go` | empty | only if s3 used | none | |
| `AWS_DEFAULT_REGION` | `config.go` | empty | only if s3 used | none | |
| `AWS_ACCESS_KEY_ID` | `config.go` | empty | only if s3 used | SENSITIVE | |
| `AWS_SECRET_ACCESS_KEY` | `config.go` | empty | only if s3 used | SENSITIVE | |
| `AWS_URL` | `config.go` | empty | no | endpoint override | |
| `FILE_ROOT` | `config.go` | process CWD | recommended | Context.File / Context.SaveFile escape if attacker can set | absolute path enforced by router |

## Mail

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `MAIL_DRIVER` | `config.go` | `log` | YES (real driver) | mail goes to logs only | one of `log`, `postmark`, `mailgun`, `local` |
| `MAIL_FROM_ADDRESS` | `config.go` | empty | YES | mail rejected by upstream | |
| `MAIL_FROM_NAME` | `config.go` | empty | no | none | |
| `MAIL_MAX_ATTACHMENT_SIZE` | `config.go` | 25 MiB | no | none | bytes |
| `MAIL_MAILGUN_DOMAIN` | `config.go` | empty | YES (mailgun) | none | |
| `MAIL_MAILGUN_SECRET` | `config.go` | empty | YES (mailgun) | SENSITIVE | |
| `MAIL_MAILGUN_ENDPOINT` | `config.go` | empty | no | none | |
| `MAIL_MAILGUN_WEBHOOK_SIGNING_KEY` | `config.go` | empty | YES (if webhooks) | webhook forgery | SENSITIVE |
| `MAIL_POSTMARK_TOKEN` | `config.go` | empty | YES (postmark) | SENSITIVE | |
| `MAIL_POSTMARK_MESSAGE_STREAM` | `config.go` | empty | no | none | |
| `MAIL_HOST`, `MAIL_PORT` | `config.go` | empty | YES (local) | none | |
| `MAIL_USERNAME`, `MAIL_PASSWORD` | `config.go` | empty | YES (local SMTP) | SENSITIVE | |
| `MAIL_ENCRYPTION` | `config.go` | empty | recommended (local) | unencrypted SMTP | |
| `MAIL_SENDMAIL_PATH` | `config.go` | empty | only if sendmail | none | |

## Logging

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `LOG_DRIVER` | `config.go` | `console` | no | none | one of `console`, `file`, `daily`, `stack`, `null` |
| `LOG_PATH` | `config.go` | empty | only if file driver | path traversal if attacker-controlled | |
| `LOG_DAYS` | `config.go` | `14` | no | none | |
| `LOG_LEVEL` | `config.go` | `debug` | recommended `info`/`warn` in prod | debug logs may leak request bodies | |
| `LOG_STACK` | `config.go` | empty | no | none | comma-separated channels |
| `LOG_REDACT` | `log/init.go` | `false` | recommended | PII in logs | process-wide default-on toggle |
| `LOG_REDACT_EMAILS` | `log/redact.go` | `false` | recommended | emails in logs | |

## Crypto

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `CRYPTO_KEY` | `config.go` | falls back to `APP_KEY` | YES | encryption broken; cookies forgeable | rotate every 6 months |
| `CRYPTO_CIPHER` | `config.go` | `AES-256-GCM` | no | weaker cipher if changed | |
| `CRYPTO_OLD_KEYS` | `config.go` | empty | only during rotation | none | comma-separated previous keys |
| `CRYPTO_DEBUG` | `crypto/drivers/aes.go` | `false` | NO | logs key material if enabled | tests only |
| `CRYPTO_DISABLE_V0` | `crypto/drivers/aes.go` | `false` | no | rejects v0 ciphertexts on read | |

## View / Bond

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `VIEW_SSR_ENABLED` | `config.go` | `false` | no | none | |
| `VIEW_SSR_URL` | `config.go` | `http://127.0.0.1:13714` | only if SSR | SSRF if attacker-controlled | should always be loopback |
| `VIEW_SSR_TIMEOUT` | `config.go` | `3s` | no | unbounded render wait | |
| `VIEW_SSR_EXCEPT` | `config.go` | empty | no | none | URL prefixes |

## Server timeouts

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `SERVER_READ_TIMEOUT` | `config.go` | `30s` | YES | slowloris | |
| `SERVER_WRITE_TIMEOUT` | `config.go` | `30s` | YES | slow-read attacks | |
| `SERVER_IDLE_TIMEOUT` | `config.go` | `120s` | YES | connection exhaustion | |
| `SERVER_READ_HEADER_TIMEOUT` | `config.go` | `10s` | YES | slowloris | |

## gRPC

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `GRPC_PORT` | `grpc/init.go` | `50051` | no | none | |
| `GRPC_REFLECTION` | `grpc/init.go` | `false` | NO (refused in prod) | reflection exposes service surface | |
| `GRPC_MAX_RECV_SIZE` | `grpc/init.go` | 4 MiB | no | none | |
| `GRPC_MAX_SEND_SIZE` | `grpc/init.go` | 4 MiB | no | none | |
| `GATEWAY_PORT` | `grpc/init.go` | `8080` | no | none | |
| `GRPC_ENDPOINT` | `grpc/init.go` | `localhost:50051` | no | none | |
| `GRPC_INSECURE` | `grpc/server.go` | `false` | NO (loud opt-in only) | disables prod TLS guard | final: do not rename |

## Maintenance

| Name | Package | Default | Required in prod? | Security impact | Notes |
|------|---------|---------|-------------------|-----------------|-------|
| `VELOCITY_MAINTENANCE_EXCLUDE_PATHS` | `maintenance.go`, `internal/maintpath/maintpath.go` | empty | no | none | comma-separated path prefixes |

## Hard-required surface in production

A boot in production (`APP_ENV` set to anything other than `dev`, `development`,
`test`, `testing`, `local`, or empty) requires:

1. `APP_KEY` set (or `CRYPTO_KEY` explicitly). Missing -> `ErrNoAppKey`.
2. `QUEUE_SIGNING_KEY` (or `APP_KEY`) set, OR `QUEUE_ACCEPT_UNSIGNED=true`.
3. `SESSION_SECURE=true` (default), `SESSION_HTTP_ONLY=true` (default).
4. `CSRF_SECURE=true` (default), `CSRF_HTTP_ONLY=true` (default).
5. `CSRF_SAME_SITE` and `SESSION_SAME_SITE` set to a non-default value
   (`lax`, `strict`, or `none`).
6. A `ServerSessionStore` wired by a provider OR
   `SessionConfig.AllowCookieStoreInProduction=true`.
7. `AUTH_TRUSTED_PROXIES` set when the deployment is behind a load
   balancer / reverse proxy. Empty means "trust nothing"; the framework
   defaults to the secure choice and ignores X-Forwarded-* headers.

## Soft-required (loud warning, not fatal)

- Scheduler distributed `Locker` installed in prod when running >1
  worker. Default `InMemoryLocker` only enforces single-process
  semantics; `OnOneServer` / `WithoutOverlapping` quietly degrade
  without it. Logged at boot via `installSchedulerLocker` and via the
  production-locker check in `velocity.New`.
- `broadcast.SetAuthSecret` must be called before any private/presence
  channel is used. The broadcast manager refuses to sign or verify
  tokens without it (`ErrUnauthorized`), so the failure surfaces at
  first use rather than at boot.

## Read-site map

```text
APP_*              config.go, cmd_ops.go, maintenance.go
DB_*               config.go
SESSION_*          config.go
AUTH_*             config.go
HASH_*             config.go
CSRF_*             config.go
CACHE_*, REDIS_*   config.go
QUEUE_*            config.go, factories.go
STORAGE_*, AWS_*   config.go
FILE_ROOT          config.go
MAIL_*             config.go
LOG_*              config.go, log/init.go, log/redact.go
CRYPTO_*           config.go, crypto/crypto.go, crypto/drivers/aes.go
VIEW_SSR_*         config.go
SERVER_*           config.go
GRPC_*, GATEWAY_*  grpc/init.go, grpc/server.go, grpc/gateway.go
VELOCITY_*         maintenance.go, internal/maintpath/maintpath.go
```
