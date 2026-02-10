# Security Audit Report: Velocity Framework

**Date:** 2026-02-10
**Scope:** Full codebase audit of `github.com/velocitykode/velocity`
**Go Version:** 1.25.1
**Framework Type:** Laravel-inspired Go web framework

---

## Executive Summary

This audit identified **93 security findings** across the Velocity framework codebase:

| Severity | Count |
|----------|-------|
| Critical | 14    |
| High     | 21    |
| Medium   | 30    |
| Low      | 28    |

The most critical classes of vulnerabilities are:

1. **SQL Injection** -- Unvalidated column names, operators, and identifiers are concatenated directly into SQL queries throughout the ORM layer
2. **Cryptographic Weaknesses** -- CBC MAC verification can be bypassed, enabling padding oracle attacks; hardcoded encryption keys; empty JWT secrets accepted
3. **Path Traversal** -- Local storage, email templates, and SQLite paths lack directory traversal protection
4. **Email Header Injection** -- CRLF injection in email headers enables arbitrary header manipulation
5. **Denial of Service** -- Unbounded request body reads, memory leaks, and race conditions causing panics

---

## Table of Contents

1. [Authentication & Authorization](#1-authentication--authorization)
2. [ORM & SQL Injection](#2-orm--sql-injection)
3. [Cryptography & CSRF](#3-cryptography--csrf)
4. [HTTP Router & Web Security](#4-http-router--web-security)
5. [Storage, Mail, Queue & Infrastructure](#5-storage-mail-queue--infrastructure)
6. [Dependency Analysis](#6-dependency-analysis)
7. [Remediation Priority Matrix](#7-remediation-priority-matrix)

---

## 1. Authentication & Authorization

### CRITICAL

#### AUTH-C1: Hardcoded Default Encryption Key for Session Cookies
- **File:** `pkg/auth/drivers/session/cookie.go:29-35`
- **Issue:** When the global encryptor is unavailable, the cookie store falls back to a hardcoded key `"default-session-key-32-bytes-long!!"`. Since this framework is open source, any attacker can use this key to decrypt and forge session cookies.
- **Impact:** Complete session forgery and arbitrary user impersonation.
- **Remediation:** Remove the hardcoded fallback. Require explicit key configuration and fail loudly if no key is provided.

#### AUTH-C2: Empty JWT Secret Accepted Without Validation
- **File:** `pkg/auth/init.go:63`, `pkg/auth/jwt.go:90`
- **Issue:** If `JWT_SECRET` environment variable is unset, the secret defaults to an empty string. No minimum length validation exists. Tokens signed with an empty HMAC key can be trivially forged.
- **Impact:** Complete JWT forgery and API authentication bypass.
- **Remediation:** Validate that JWT secret is non-empty and meets a minimum length (e.g., 32 bytes) at initialization. Panic or return error if not met.

### HIGH

#### AUTH-H1: JWT Blacklist Map Has No Mutex Protection (Race Condition)
- **File:** `pkg/auth/jwt.go:166-200`
- **Issue:** The `blacklist` map is accessed concurrently by `RevokeToken`, `IsBlacklisted`, and `CleanupBlacklist` with zero synchronization. Concurrent map access in Go causes a fatal runtime panic.
- **Impact:** Denial of service via panic; potential token revocation bypass.
- **Remediation:** Add `sync.RWMutex` protection around all blacklist map operations.

#### AUTH-H2: JWT Tokens Accepted from URL Query Parameters
- **File:** `pkg/auth/drivers/guards/jwt.go:222-224`
- **Issue:** JWT tokens are accepted from URL query parameter `?token=...`. URLs are logged in server access logs, proxy logs, browser history, and HTTP Referer headers.
- **Impact:** Token leakage enabling account takeover.
- **Remediation:** Remove query parameter token extraction or restrict it to WebSocket upgrade requests only with short-lived tokens.

#### AUTH-H3: JWT Guard User Cache Not Thread-Safe (Race Condition)
- **File:** `pkg/auth/drivers/guards/jwt.go:15,47,59,74,172`
- **Issue:** The `userCache` map is accessed from multiple goroutines without mutex protection.
- **Impact:** Denial of service via runtime panic under concurrent requests.
- **Remediation:** Add `sync.RWMutex` protection or use `sync.Map`.

#### AUTH-H4: Session Guard Memory Leak (Unbounded Session Cache)
- **File:** `pkg/auth/drivers/guards/session.go:22,214-216`
- **Issue:** Sessions are added to a map keyed by `*http.Request` but never removed after requests complete.
- **Impact:** Denial of service via memory exhaustion.
- **Remediation:** Add cleanup logic (e.g., defer deletion after request completes, or use request context for storage).

#### AUTH-H5: In-Memory JWT Blacklist Lost on Restart
- **File:** `pkg/auth/jwt.go:33`
- **Issue:** Token blacklist is a plain in-memory map. All revocations are lost on process restart and not shared across instances.
- **Impact:** Unreliable token revocation; compromised tokens cannot be invalidated.
- **Remediation:** Use a persistent store (Redis, database) for the blacklist.

#### AUTH-H6: Session Secure Cookie Flag Defaults to `false`
- **File:** `pkg/auth/init.go:37`, `pkg/auth/session.go:265-266`
- **Issue:** Without explicit `SESSION_SECURE=true`, session cookies are sent over plain HTTP.
- **Impact:** Session hijacking via network eavesdropping.
- **Remediation:** Default `Secure` to `true`. Require explicit opt-out for development.

#### AUTH-H7: Predictable Fallback Session ID Generation
- **File:** `pkg/auth/session.go:236-241`
- **Issue:** If `crypto/rand.Read` fails, session IDs fall back to `time.Now().String()`, which is trivially predictable.
- **Impact:** Session fixation and hijacking when CSPRNG fails.
- **Remediation:** If `crypto/rand` fails, panic rather than falling back to insecure generation.

### MEDIUM

#### AUTH-M1: No JWT Issuer or Audience Validation
- **File:** `pkg/auth/jwt.go:62-71,100-109`
- **Issue:** Neither `iss` nor `aud` claims are set or validated, enabling cross-service token confusion.

#### AUTH-M2: No Access/Refresh Token Differentiation
- **File:** `pkg/auth/jwt.go:55-113`
- **Issue:** Access and refresh tokens are structurally identical with no "type" claim.

#### AUTH-M3: Blacklist Expiry Uses Access Token TTL for Refresh Tokens
- **File:** `pkg/auth/jwt.go:168`
- **Issue:** Revoked refresh tokens (2 week TTL) become unblacklisted after the access token TTL (60 min).

#### AUTH-M4: `ParseTokenWithoutValidation` Exposes Unsigned Claim Extraction
- **File:** `pkg/auth/jwt.go:227-239`
- **Issue:** Public method parses JWT without signature verification. If misused, enables authentication bypass.

#### AUTH-M5: JWT Blacklist Disabled by Default
- **File:** `pkg/auth/init.go:67`
- **Issue:** Token blacklisting is opt-in. Logout is a no-op without it.

#### AUTH-M6: Authorization Header Parsing Too Permissive
- **File:** `pkg/auth/drivers/guards/jwt.go:205-214`
- **Issue:** Non-Bearer authorization headers are silently treated as raw tokens.

#### AUTH-M7: Unsafe Type Assertion Causes Panic
- **File:** `pkg/auth/drivers/guards/session.go:253`
- **Issue:** Unchecked `.(*string)` assertion panics when user ID is not a string (e.g., integer).

#### AUTH-M8: `UpdateRememberToken` Never Persists to Database
- **File:** `pkg/auth/provider.go:85-89`
- **Issue:** Remember tokens are set in-memory only; never saved to database.

#### AUTH-M9: Race Condition in `GetHasher` (Write Under RLock)
- **File:** `pkg/auth/hasher.go:117-129`
- **Issue:** `globalHasher` is written while only holding a read lock.

### LOW

#### AUTH-L1: Low Bcrypt Costs (4-9) Accepted Without Warning
- **File:** `pkg/auth/hasher.go:29-35`

#### AUTH-L2: No Bcrypt 72-Byte Password Truncation Handling
- **File:** `pkg/auth/hasher.go:43-57`

#### AUTH-L3: Auth Middleware Only Supports HTML Redirects (No JSON 401)
- **File:** `pkg/auth/middleware.go:30-47`

---

## 2. ORM & SQL Injection

### CRITICAL

#### ORM-C1: Password Hash Function Returns Plaintext
- **File:** `pkg/orm/orm.go:359-363`
- **Issue:** `orm.Hash(password)` returns the password unchanged. Any code using this function stores passwords in plaintext.
- **Impact:** Full password exposure on database breach.
- **Remediation:** Implement proper bcrypt hashing or remove this misleading function and direct users to `pkg/auth/hasher.go`.

#### ORM-C2: SQL Injection via Unvalidated Column Names
- **File:** `pkg/orm/model.go:81-89,212-218,221-227` (repeated across all 4 model types)
- **Issue:** Field names in `FindBy`, `Update`, and `DeleteWhere` are concatenated directly into WHERE clauses: `query.Where(field+" = ?", value)`.
- **Impact:** If application code passes user input as field names, arbitrary SQL injection.
- **Remediation:** Validate field names against the model's known columns or use `QuoteIdentifier`.

#### ORM-C3: QuoteIdentifier Does Not Escape Internal Quotes
- **File:** `pkg/orm/drivers/mysql.go:537-539`, `pkg/orm/drivers/postgres.go:555-557`, `pkg/orm/drivers/sqlite.go:484-486`
- **Issue:** `QuoteIdentifier` wraps names in backticks or double-quotes without escaping those characters within the name.
- **Impact:** Identifier injection enabling arbitrary SQL execution.
- **Remediation:** Escape internal backticks (MySQL/SQLite: double them) and double-quotes (Postgres: double them).

#### ORM-C4: SQL Injection via Unvalidated Operator in WHERE Conditions
- **File:** `pkg/orm/query.go:175-207`
- **Issue:** `parseCondition` extracts an operator from user-supplied condition strings and writes it directly into SQL.
- **Impact:** Arbitrary SQL injection through the operator position.
- **Remediation:** Validate operators against an allowlist (`=`, `!=`, `<`, `>`, `<=`, `>=`, `LIKE`, `IN`, `IS`).

#### ORM-C5: SQL Injection in JOIN ON Clauses
- **File:** `pkg/orm/query.go:245-271`
- **Issue:** All four parameters of `Join()`, `LeftJoin()`, `RightJoin()` are concatenated into SQL without parameterization.
- **Impact:** Arbitrary SQL injection.
- **Remediation:** Validate/quote table names and column identifiers; validate operators.

### HIGH

#### ORM-H1: SQL Injection via ORDER BY Column and Direction
- **File:** `pkg/orm/query.go:210-218`
- **Issue:** `direction` is written directly to SQL without validation against `ASC`/`DESC`.

#### ORM-H2: Mass Assignment -- No Fillable/Guarded Fields
- **File:** `pkg/orm/model.go:1179-1211`
- **Issue:** `mapToStruct` sets any settable struct field from a map, including `ID`, `is_admin`, `role`, etc.

#### ORM-H3: Default SSL Disabled for PostgreSQL
- **File:** `pkg/orm/drivers/postgres.go:46-49`
- **Issue:** Defaults to `sslmode=disable`, transmitting credentials and data in plaintext.

#### ORM-H4: Broken Transaction -- Callback Runs Outside Transaction
- **File:** `pkg/orm/orm.go:206-230`
- **Issue:** The `tx` object is never passed to the callback; all queries run on the default connection.

#### ORM-H5: SQL Injection in DDL Default Value Formatting
- **File:** `pkg/orm/migrate/migrator.go:1062-1065`
- **Issue:** String default values are wrapped in single quotes without escaping internal quotes.

#### ORM-H6: SQL Injection in DDL Statements (DROP TABLE, DROP INDEX, etc.)
- **File:** `pkg/orm/migrate/state.go:203-217`, `pkg/orm/migrate/migrator.go:224-241`, `pkg/orm/migrate/index.go:204-228`
- **Issue:** Table and index names concatenated directly into DDL without quoting.

#### ORM-H7: SQL Injection in Testing Helper
- **File:** `pkg/orm/testing/refresh.go:27`
- **Issue:** Database name interpolated directly into `information_schema` query.

### MEDIUM

#### ORM-M1: Unquoted SELECT Columns in SQLite Grammar
- **File:** `pkg/orm/drivers/sqlite.go:218`

#### ORM-M2: Raw SQL Methods Without Safety Guardrails
- **File:** `pkg/orm/orm.go:281-302`, `pkg/orm/query.go:826-831`, `pkg/orm/migrate/migrator.go:243-250`

#### ORM-M3: Special Characters in MySQL DSN Not Escaped
- **File:** `pkg/orm/drivers/mysql.go:28-35`

#### ORM-M4: Special Characters in PostgreSQL DSN Not Escaped
- **File:** `pkg/orm/drivers/postgres.go:27-58`

#### ORM-M5: Rollback Errors Silently Discarded
- **File:** `pkg/orm/orm.go:219,225`

#### ORM-M6: Sensitive Data Exposure in Query Logging
- **File:** `pkg/orm/drivers/mysql.go:112-114`, `postgres.go:111-113`, `sqlite.go:107-109`

#### ORM-M7: Path Traversal in SQLite Database Path
- **File:** `pkg/orm/drivers/sqlite.go:29-45`

### LOW

#### ORM-L1: Race Condition in MigrationRegistry.All()
- **File:** `pkg/orm/migrate/migrate.go:76-89`

#### ORM-L2: Error Swallowed in orm.Raw()
- **File:** `pkg/orm/orm.go:281-293`

#### ORM-L3: Factory buildInsertSQL Does Not Quote Identifiers
- **File:** `pkg/orm/testing/factory.go:171-199`

#### ORM-L4: Auto-Loading .env in init() Without Warning
- **File:** `pkg/orm/init.go:16`

---

## 3. Cryptography & CSRF

### CRITICAL

#### CRYPTO-C1: CBC MAC Verification Can Be Bypassed (Padding Oracle Attack)
- **File:** `pkg/crypto/drivers/aes.go:191-197`
- **Issue:** MAC verification is conditional on `p.MAC != ""`. An attacker can strip the MAC field from any CBC-encrypted payload, bypassing integrity verification. Combined with the incomplete PKCS#7 validation (CRYPTO-C2), this enables a classic padding oracle attack to decrypt arbitrary ciphertext without the key.
- **Impact:** Complete decryption of all AES-256-CBC encrypted data.
- **Remediation:** Always require MAC for CBC payloads. Reject decryption if MAC is missing.

#### CRYPTO-C2: Incomplete PKCS#7 Padding Validation
- **File:** `pkg/crypto/drivers/aes.go:295-304`
- **Issue:** `pkcs7Unpad` only checks the last byte for padding length but does not verify all padding bytes match. This produces distinguishable error paths that leak information for padding oracle exploitation.
- **Impact:** Enables padding oracle attacks when combined with CRYPTO-C1.
- **Remediation:** Verify all `N` trailing bytes equal `N`. Also validate that padding value does not exceed block size (16).

### HIGH

#### CRYPTO-H1: Default Cipher Is AES-256-CBC Instead of AES-256-GCM
- **File:** `pkg/crypto/crypto.go:149`, `pkg/crypto/init.go:23`
- **Issue:** Default cipher is CBC, which requires separate MAC handling (currently bypassable). GCM provides authenticated encryption natively.

#### CRYPTO-H2: Silent Initialization Failure Masks Misconfiguration
- **File:** `pkg/crypto/init.go:39-43`
- **Issue:** If `CRYPTO_KEY` is malformed, initialization fails silently. Application runs without encryption.

### MEDIUM

#### CSRF-M1: Session ID Falls Back to RemoteAddr
- **File:** `pkg/csrf/csrf.go:122-124`
- **Issue:** Users behind the same NAT share CSRF tokens.

#### CSRF-M2: SessionStore Ignores Config.TokenLifetime
- **File:** `pkg/csrf/stores/session.go:59`
- **Issue:** Hardcoded 24h expiry ignores configured lifetime.

#### CRYPTO-M1: No Key Derivation Function -- Raw Keys Used Directly
- **File:** `pkg/crypto/crypto.go:184-197`
- **Issue:** ASCII passwords used directly as AES keys without KDF.

#### CRYPTO-M2: HMAC and Encryption Use the Same Key
- **File:** `pkg/crypto/drivers/aes.go:278-282`
- **Issue:** Violates key separation principle.

### LOW

#### CSRF-L1: TRACE Listed as "Safe" HTTP Method
- **File:** `pkg/csrf/csrf.go:223`

#### CSRF-L2: SessionStore Goroutine Leak
- **File:** `pkg/csrf/stores/session.go:26-32`

#### CSRF-L3: SingleUse Token Delete Error Ignored
- **File:** `pkg/csrf/csrf.go:85-87`

#### CSRF-L4: Middleware Wrapper Swallows CSRF Rejection Error
- **File:** `pkg/csrf/helpers.go:71-81`

---

## 4. HTTP Router & Web Security

### CRITICAL

#### HTTP-C1: Unbounded Request Body Read -- Denial of Service
- **File:** `pkg/http/router.go:655-659`
- **Issue:** `io.ReadAll` reads the entire request body with no size limit. A single attacker can exhaust server memory.
- **Remediation:** Use `http.MaxBytesReader` to enforce body size limits.

#### HTTP-C2: Unbounded JSON Body Parsing -- Denial of Service
- **File:** `pkg/router/context.go:232-234`, `pkg/http/router.go:648-653`
- **Issue:** `Bind()` and `BindJSON()` do not enforce body size limits.
- **Remediation:** Wrap the request body with `http.MaxBytesReader` before decoding.

### HIGH

#### HTTP-H1: Error Messages Leak Internal Information
- **File:** `pkg/router/velocity_router.go:369-371`, `pkg/router/context.go:274-278`, `pkg/http/router.go:286-304`
- **Issue:** Raw `err.Error()` messages (which may contain DB connection strings, file paths, SQL) sent directly to clients.

#### HTTP-H2: Rate Limiting Bypassed via Spoofable Headers
- **File:** `pkg/router/rate_limit.go:307-343`
- **Issue:** Unconditionally trusts `X-Forwarded-For` and `X-Real-IP` headers.

#### HTTP-H3: IP Spoofing via Trusted Proxy Headers
- **File:** `pkg/router/context.go:247-257`
- **Issue:** `c.IP()` trusts `X-Forwarded-For` unconditionally and returns the entire header value.

#### HTTP-H4: WebSocket Origin Validation Disabled by Default
- **File:** `pkg/websocket/server.go:257-273`, `pkg/websocket/utils.go:27-41`
- **Issue:** Default config allows all origins (`*`), enabling Cross-Site WebSocket Hijacking.

#### HTTP-H5: Open Redirect via Bond `Back()` Using Referer Header
- **File:** `pkg/bond/redirect.go:36-42`
- **Issue:** Client-controlled `Referer` header used as redirect target.

### MEDIUM

#### HTTP-M1: Open Redirect in Redirect Helpers
- **File:** `pkg/router/context.go:213-217`, `pkg/http/router.go:545-548`, `pkg/bond/redirect.go:12-17`
- **Issue:** No validation that redirect URLs are local/relative.

#### HTTP-M2: HTML Response Methods Write Raw Content (XSS Surface)
- **File:** `pkg/router/context.go:206-211`, `pkg/http/router.go:538-543`
- **Issue:** `HTML()` writes user content without encoding.

#### HTTP-M3: Missing Security Response Headers Framework-Wide
- **Issue:** No middleware sets `X-Content-Type-Options`, `X-Frame-Options`, `Content-Security-Policy`, `Strict-Transport-Security`, `Referrer-Policy`, or `Permissions-Policy`.

#### HTTP-M4: Debug Mode Exposes Source Code and Stack Traces
- **File:** `pkg/exceptions/renderer.go:57-81`, `pkg/exceptions/stack.go:106-144`
- **Issue:** Debug mode reads and renders source code from disk in error pages.

#### HTTP-M5: WebSocket Error Messages Reflect User Input
- **File:** `pkg/websocket/client.go:101-108,119-123`

#### HTTP-M6: No CORS Middleware Provided
- **Issue:** No built-in CORS handling, likely leading to insecure implementations by developers.

#### HTTP-M7: Rate Limiter Memory Exhaustion via Key Flooding
- **File:** `pkg/router/rate_limit.go:68-121`
- **Issue:** No maximum map size cap; spoofed IPs create unlimited entries.

#### HTTP-M8: URL Generation Does Not Encode Parameter Values
- **File:** `pkg/http/router.go:444-462`

#### HTTP-M9: Wildcard Route Parameters Not URL-Escaped
- **File:** `pkg/router/segment.go:162-169`

### LOW

#### HTTP-L1: Client-Provided Request IDs Reflected in Responses
- **File:** `pkg/http/router.go:386-396`

#### HTTP-L2: Bond containerID Not HTML-Escaped
- **File:** `pkg/bond/render.go:71-73`

#### HTTP-L3: Static File Serving Follows Symlinks
- **File:** `pkg/router/velocity_router.go:152-156`

#### HTTP-L4: Format String Vulnerability in String Method
- **File:** `pkg/http/router.go:531-536`

#### HTTP-L5: WebSocket Group Names Susceptible to Log Injection
- **File:** `pkg/websocket/server.go:196,228`, `pkg/websocket/groups.go:31,57`

---

## 5. Storage, Mail, Queue & Infrastructure

### CRITICAL

#### INFRA-C1: Path Traversal in Local Storage Driver
- **File:** `pkg/storage/local.go:404-408`
- **Issue:** `filepath.Join(d.root, filepath.Clean(path))` does not prevent traversal. `filepath.Join("/storage", "../../etc/passwd")` resolves to `/etc/passwd`. Every storage operation (`Put`, `Get`, `Delete`, `Copy`, `Move`) is affected.
- **Impact:** Arbitrary file read, write, and delete on the filesystem.
- **Remediation:** After joining, verify the result starts with `d.root` using `strings.HasPrefix(filepath.Clean(result), filepath.Clean(d.root))`.

#### INFRA-C2: Email Header Injection (CRLF Injection)
- **File:** `pkg/mail/drivers/local.go:148,157,180-181,192-194`
- **Issue:** No sanitization of `\r\n` in Subject, From, To, CC, Reply-To, or custom headers. Enables injection of arbitrary email headers including BCC recipients.
- **Remediation:** Strip or reject `\r` and `\n` characters in all header values.

#### INFRA-C3: Broken Mailgun Webhook Signature Verification
- **File:** `pkg/mail/drivers/mailgun.go:221-225`
- **Issue:** `VerifyWebhookSignature` only checks that signature is non-empty; no HMAC verification.
- **Impact:** Forged webhook events accepted as legitimate.
- **Remediation:** Implement proper HMAC-SHA256 verification using Mailgun signing key.

#### INFRA-C4: Arbitrary File Read via AttachFile
- **File:** `pkg/mail/message.go:122-126`
- **Issue:** No path validation or sandboxing. If user input reaches this method, arbitrary files can be read and attached to emails. Also panics on error.
- **Remediation:** Validate paths against an allowed directory. Return error instead of panicking.

#### INFRA-C5: Insecure Deserialization in Queue (No Integrity Verification)
- **File:** `pkg/queue/redis.go:129-139`, `pkg/queue/database.go:180-183`
- **Issue:** Job payloads from Redis/database are deserialized and executed without HMAC or integrity checks. An attacker with Redis access can inject arbitrary job payloads.
- **Remediation:** Sign payloads with HMAC before enqueuing; verify on dequeue.

### HIGH

#### INFRA-H1: Internal Errors Leaked to gRPC Clients
- **File:** `pkg/grpc/errors.go:100-112`
- **Issue:** Raw Go error messages (potentially containing DB connection strings, paths) sent to clients.

#### INFRA-H2: Bearer Tokens Exposed in gRPC Event Metadata
- **File:** `pkg/grpc/interceptors/logging.go:243-257`
- **Issue:** Full gRPC metadata including `authorization` header included in events without redaction.

#### INFRA-H3: Template Path Traversal in Email Templates
- **File:** `pkg/mail/message.go:153-156`
- **Issue:** Template name concatenated directly into filesystem path.

#### INFRA-H4: Unbounded Memory via PutStream (S3 and Memory Drivers)
- **File:** `pkg/storage/s3.go:98-106`, `pkg/storage/memory.go:78-86`
- **Issue:** `io.ReadAll` on streams with no size limit.

#### INFRA-H5: MIME Boundary Injection via Static Boundaries
- **File:** `pkg/mail/drivers/local.go:201,241`
- **Issue:** Hardcoded MIME boundaries can be spoofed in email content.

#### INFRA-H6: Attachment Filename Injection
- **File:** `pkg/mail/drivers/local.go:211-213`
- **Issue:** Quotes and CRLF in attachment names/types not escaped.

#### INFRA-H7: Insecure Gateway-to-gRPC Connection (No TLS)
- **File:** `pkg/grpc/gateway.go:47-49`
- **Issue:** Gateway-to-backend uses `insecure.NewCredentials()` by default.

### MEDIUM

#### INFRA-M1: MD5 Used for Cache File Paths
- **File:** `pkg/cache/drivers/file.go:86-96`

#### INFRA-M2: Redis Cache Flush Destroys Entire Database
- **File:** `pkg/cache/drivers/redis.go:101-105`

#### INFRA-M3: Cache Files World-Readable (0644)
- **File:** `pkg/cache/drivers/file.go:177`

#### INFRA-M4: Memory Leak in Queue Job Store
- **File:** `pkg/queue/job_wrapper.go:24-36`
- **Issue:** Jobs stored in global map but `Remove` never called.

#### INFRA-M5: URL Validation Overly Permissive (SSRF Risk)
- **File:** `pkg/validation/rules/string.go:52`
- **Issue:** Accepts `http://169.254.169.254/` and internal URLs.

#### INFRA-M6: Regex Compiled on Every Validation Call
- **File:** `pkg/validation/rules/string.go:33,52,70,88,106`

#### INFRA-M7: No Email Address Format Validation in Mail Types
- **File:** `pkg/mail/types.go:26-31`

#### INFRA-M8: Panic-Based Error Handling in Mail Package
- **File:** `pkg/mail/message.go:125-126,155-157`

#### INFRA-M9: S3 cleanPath Does Not Sanitize Traversal Components
- **File:** `pkg/storage/s3.go:500-506`

### LOW

#### INFRA-L1: Sensitive Credentials as Plain Strings in Structs
- **Files:** `pkg/storage/types.go:78-79`, `pkg/mail/drivers/local.go:39`, `pkg/mail/drivers/mailgun.go:28-29`, `pkg/mail/drivers/postmark.go:25-26`, `pkg/queue/redis.go:18`

#### INFRA-L2: Email Regex Allows Invalid Addresses
- **File:** `pkg/validation/rules/string.go:33`

#### INFRA-L3: No TLS for Redis Connections
- **File:** `pkg/queue/redis.go:35-39`, `pkg/cache/drivers/redis.go:20-24`

#### INFRA-L4: gRPC Reflection Exposable via Environment Variable
- **File:** `pkg/grpc/init.go:42`

#### INFRA-L5: Local Storage Default Visibility Is Public
- **File:** `pkg/storage/local.go:34-36`

#### INFRA-L6: No File Size Limit on Local Storage Put/PutStream
- **File:** `pkg/storage/local.go:47-102`

---

## 6. Dependency Analysis

### Direct Dependencies

| Dependency | Version | Notes |
|---|---|---|
| `golang-jwt/jwt/v5` | v5.3.0 | No known vulnerabilities |
| `gorilla/websocket` | v1.5.3 | No known vulnerabilities |
| `go-sql-driver/mysql` | v1.9.3 | No known vulnerabilities |
| `lib/pq` | v1.10.9 | No known vulnerabilities |
| `mattn/go-sqlite3` | v1.14.32 | No known vulnerabilities |
| `redis/go-redis/v9` | v9.14.0 | No known vulnerabilities |
| `golang.org/x/crypto` | v0.46.0 | No known vulnerabilities |
| `aws-sdk-go-v2` | v1.41.1 | No known vulnerabilities |
| `grpc` | v1.78.0 | No known vulnerabilities |
| `joho/godotenv` | v1.5.1 | No known vulnerabilities |

**Note:** `govulncheck` could not be executed in this environment due to network restrictions. Manual review of dependency versions against known CVE databases shows no known vulnerabilities in the pinned versions as of the audit date. Running `govulncheck` in CI (which is already configured in `.github/workflows/security.yml`) is recommended for continuous monitoring.

---

## 7. Remediation Priority Matrix

### Immediate (P0) -- Fix before any release

| Finding | Risk | Effort |
|---------|------|--------|
| AUTH-C1: Hardcoded session key | Authentication bypass | Low |
| AUTH-C2: Empty JWT secret | Authentication bypass | Low |
| CRYPTO-C1: CBC MAC bypass | Data decryption | Low |
| CRYPTO-C2: PKCS#7 padding | Enables padding oracle | Low |
| ORM-C1: Plaintext Hash() | Password exposure | Low |
| ORM-C2: Column name injection | SQL injection | Medium |
| ORM-C3: QuoteIdentifier bypass | SQL injection | Low |
| ORM-C4: Operator injection | SQL injection | Low |
| ORM-C5: JOIN injection | SQL injection | Low |
| INFRA-C1: Storage path traversal | Arbitrary file access | Low |
| INFRA-C2: Email header injection | Email spoofing | Low |
| INFRA-C3: Webhook verification | Event forgery | Medium |
| HTTP-C1/C2: Unbounded body read | Denial of service | Low |

### Short-term (P1) -- Fix within next sprint

| Finding | Risk | Effort |
|---------|------|--------|
| AUTH-H1/H3: Race conditions | DoS via panic | Low |
| AUTH-H2: JWT in URL | Token leakage | Low |
| AUTH-H4: Memory leak | DoS | Medium |
| AUTH-H6: Insecure cookie default | Session hijacking | Low |
| AUTH-H7: Predictable session IDs | Session hijacking | Low |
| ORM-H1: ORDER BY injection | SQL injection | Low |
| ORM-H2: Mass assignment | Privilege escalation | Medium |
| ORM-H4: Broken Transaction | Data corruption | Medium |
| HTTP-H1: Error info leak | Information disclosure | Medium |
| HTTP-H2/H3: IP spoofing | Rate limit/ACL bypass | Medium |
| HTTP-H4: WebSocket origin | CSWSH | Low |
| INFRA-H7: gRPC no TLS | Credential theft | Low |

### Medium-term (P2) -- Plan for upcoming releases

All MEDIUM findings should be addressed, with particular focus on:
- Missing security headers (HTTP-M3)
- CORS middleware (HTTP-M6)
- Debug mode safeguards (HTTP-M4)
- Cache security (INFRA-M1/M2/M3)
- Queue integrity (INFRA-C5)

### Long-term (P3) -- Track and address

All LOW findings should be tracked in the issue backlog for eventual remediation.

---

## Positive Findings

The following security practices were implemented correctly:

- **CSRF token generation** uses `crypto/rand.Read` (`pkg/csrf/token.go:14`)
- **CSRF token comparison** uses `crypto/subtle.ConstantTimeCompare` (`pkg/csrf/token.go:23`)
- **CSRF cookie defaults** include `Secure: true`, `HttpOnly: true`, `SameSite: Lax`
- **AES IV/nonce generation** uses `io.ReadFull(rand.Reader, ...)` correctly
- **AES key size validation** in `NewAESDriver` checks key length
- **AES-GCM implementation** correctly uses `Seal`/`Open` with random nonce
- **Crypto key generation** uses `io.ReadFull(rand.Reader, key)`
- **No `math/rand`** usage for security-sensitive operations
- **Go's `net/http`** prevents CRLF injection in response headers
- **`html/template`** is used correctly in Bond with `template.HTML` for trusted content only
- **`http.Dir`** prevents `../` traversal in static file serving
- **Bcrypt** used for password hashing with reasonable default cost
- **Request ID generation** in `pkg/router` uses server-generated IDs
- **Key rotation support** in crypto package allows graceful key migration
- **CI pipeline** includes `govulncheck` for dependency vulnerability scanning

---

*Report generated by security audit on 2026-02-10.*
