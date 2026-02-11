# Security Guidelines for Velocity Framework

> These guidelines are derived from a 3-round security audit (2026-02-10 to 2026-02-11) that
> identified 93 vulnerabilities across the codebase. Each rule exists because we found a real
> bug that violated it. Follow these when adding or modifying code.

---

## 1. SQL & Database

### 1.1 Never Concatenate User Input into SQL

Every SQL injection we found came from string concatenation -- column names, operators, table
names, and JOIN clauses were all interpolated directly into queries.

**Rule:** All dynamic values in SQL must be parameterized (`?` placeholders) or validated
against an allowlist before use.

```go
// WRONG -- found in query.go (ORM-C4, ORM-C5)
query := fmt.Sprintf("SELECT * FROM users WHERE %s %s ?", column, operator)

// CORRECT -- validate then quote
if err := validateIdentifier(column); err != nil {
    return err
}
if !isAllowedOperator(operator) {
    return fmt.Errorf("invalid operator: %s", operator)
}
query := fmt.Sprintf("SELECT * FROM users WHERE %s %s ?", grammar.QuoteIdentifier(column), operator)
```

### 1.2 Validate Identifiers with the Established Regex

We use `^[a-zA-Z_][a-zA-Z0-9_.]*$` to validate column/table names. Call `validateIdentifier()`
from `pkg/orm/query.go` for any identifier that enters a SQL statement.

**Applies to:** Column names in WHERE, ORDER BY, GROUP BY, HAVING, JOIN ON clauses. Table
names in DDL. Index names in CREATE/DROP INDEX.

### 1.3 Always Escape Inside QuoteIdentifier

We found that all three drivers wrapped identifiers in quotes but didn't escape the quote
character within the name (`ORM-C3`). The fix is to double the quote character:

- MySQL/SQLite: `` "`" + strings.ReplaceAll(name, "`", "``") + "`" ``
- PostgreSQL: `"\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""`

Any new database driver must implement this same pattern.

### 1.4 Validate Operators Against an Allowlist

We found arbitrary SQL injection through the operator position (`ORM-C4`). The allowed
operators are:

```
=  !=  <>  <  >  <=  >=  LIKE  NOT LIKE  ILIKE  IN  NOT IN  IS  IS NOT  BETWEEN  SIMILAR TO  ~
```

Reject anything else. Do not try to sanitize operators -- allowlist only.

### 1.5 Escape String Defaults in DDL

When generating DDL with default values, escape single quotes by doubling them (`ORM-H5`):

```go
// WRONG
fmt.Sprintf("DEFAULT '%s'", defaultValue)

// CORRECT
fmt.Sprintf("DEFAULT '%s'", strings.ReplaceAll(defaultValue, "'", "''"))
```

### 1.6 Implement Mass Assignment Protection

Any ORM method that maps external data to struct fields must check `Fillable()` (whitelist)
or `Guarded()` (blacklist) interfaces. We found unrestricted field setting in `mapToStruct`
(`ORM-H2`) that allowed overwriting `ID`, `is_admin`, and `role` fields.

```go
type User struct { ... }

// Whitelist approach (preferred)
func (u User) Fillable() []string {
    return []string{"name", "email", "password"}
}

// Blacklist approach
func (u User) Guarded() []string {
    return []string{"id", "is_admin", "role", "created_at"}
}
```

### 1.7 Default PostgreSQL to SSL

We found `sslmode=disable` as the default (`ORM-H3`). Default must be `sslmode=prefer` at
minimum. Never set `sslmode=disable` in production code.

### 1.8 Pass Transactions to Callbacks

We found a broken transaction implementation where the `tx` object was never passed to the
callback (`ORM-H4`). Always ensure the transactional connection is what the callback uses:

```go
func Transaction(fn func(tx *sql.Tx) error) error {
    tx, err := db.Begin()
    // ...
    err = fn(tx)  // tx must be used inside fn, not the global db
}
```

---

## 2. Cryptography

### 2.1 Default to AES-256-GCM

We found that AES-256-CBC was the default cipher, and the MAC verification for CBC was
bypassable (`CRYPTO-C1`). GCM provides authenticated encryption natively.

**Rule:** Always use AES-256-GCM for new encryption. CBC is supported for backward
compatibility only.

### 2.2 Never Make MAC Verification Optional

The padding oracle attack we found (`CRYPTO-C1` + `CRYPTO-C2`) was possible because:
1. MAC was checked only if the MAC field was non-empty (attacker strips it)
2. PKCS#7 padding only checked the last byte, not all padding bytes

**Rules:**
- Always require MAC for CBC-encrypted payloads. Reject decryption if MAC is missing.
- Verify MAC **before** removing padding (authenticate-then-decrypt).
- Validate **all** padding bytes using constant-time comparison.

### 2.3 Use HKDF for Key Derivation

We found raw ASCII keys used directly as AES keys (`CRYPTO-M1`) and the same key used for
both encryption and HMAC (`CRYPTO-M2`).

**Rules:**
- Derive keys using HKDF-SHA256 with a domain-specific info parameter.
- Always derive separate subkeys for encryption and HMAC.

```go
encKey, _ := deriveSubkey(master, 32, []byte("encryption"))
macKey, _ := deriveSubkey(master, 32, []byte("hmac"))
```

### 2.4 Use crypto/rand for All Security-Sensitive Randomness

We found `time.Now().String()` used as a fallback for session IDs (`AUTH-H7`) and remember
tokens (`R-H1`).

**Rules:**
- Always use `crypto/rand.Read()` for tokens, keys, IVs, nonces, session IDs, and CSRF tokens.
- If `crypto/rand.Read()` fails, **panic**. Never fall back to `time.Now()` or `math/rand`.
- Generate at least 16 bytes (128 bits) of entropy for tokens and identifiers.

```go
// CORRECT
token := make([]byte, 32)
if _, err := rand.Read(token); err != nil {
    panic("crypto/rand failure: " + err.Error())
}
```

### 2.5 Use Constant-Time Comparison for Secrets

We verified correct use of `crypto/subtle.ConstantTimeCompare` for CSRF tokens and HMAC
signatures. This must be used everywhere secrets are compared.

**Applies to:** CSRF tokens, HMAC signatures, remember tokens, webhook signatures, API keys,
password reset tokens.

```go
if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
    return ErrInvalidToken
}
```

---

## 3. Authentication & Sessions

### 3.1 Never Hardcode Secrets or Keys

We found a hardcoded session encryption key (`AUTH-C1`: `"default-session-key-32-bytes-long!!"`)
and an empty JWT secret accepted without validation (`AUTH-C2`).

**Rules:**
- Encryption keys, JWT secrets, and signing keys must come from environment configuration.
- Validate minimum key length at initialization (32 bytes for JWT, AES key size for encryption).
- **Panic** on missing or undersized keys. Silent fallbacks are security holes.

### 3.2 Protect Concurrent Map Access

We found three race conditions (`AUTH-H1`, `AUTH-H3`, `AUTH-M9`) where maps were accessed
from multiple goroutines without synchronization. In Go, this causes a fatal runtime panic.

**Rule:** Every map accessed from multiple goroutines must be protected by `sync.RWMutex` or
use `sync.Map`. Use `RLock` for reads and `Lock` for writes.

```go
type SafeCache struct {
    mu    sync.RWMutex
    items map[string]interface{}
}

func (c *SafeCache) Get(key string) (interface{}, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    v, ok := c.items[key]
    return v, ok
}
```

### 3.3 Bound All In-Memory Caches

We found unbounded maps causing memory leaks in the JWT user cache (`R-H2`) and session
guard (`AUTH-H4`).

**Rules:**
- Every in-memory cache must have a maximum size (e.g., 10,000 entries).
- Implement TTL-based expiration (e.g., 5 minutes).
- Add LRU or oldest-first eviction when at capacity.
- Prefer context-based per-request storage over global maps keyed by `*http.Request`.

### 3.4 Restrict JWT Extraction to Safe Sources

We found JWT tokens accepted from URL query parameters (`AUTH-H2`), which leaks tokens
through server logs, proxy logs, browser history, and Referer headers.

**Rule:** Extract JWT tokens from the `Authorization: Bearer <token>` header only. The sole
exception is WebSocket upgrade requests, where query parameters may be necessary.

### 3.5 Set Secure Cookie Defaults

We found session cookies defaulting to insecure settings (`AUTH-H6`).

**Required cookie defaults:**
```go
Secure:   true       // HTTPS only
HttpOnly: true       // No JavaScript access
SameSite: Lax        // CSRF protection
Path:     "/"
```

`Secure: false` should only be available as an explicit development-mode opt-out.

### 3.6 Differentiate Token Types

We found access and refresh tokens were structurally identical (`AUTH-M2`), enabling token
confusion attacks.

**Rule:** Include a `"type"` claim in all JWTs (e.g., `"access"`, `"refresh"`). Validate the
type claim matches the expected use. Use the correct TTL when blacklisting each type.

---

## 4. HTTP & Routing

### 4.1 Limit Request Body Size

We found `io.ReadAll` used without size limits on request bodies (`HTTP-C1`, `HTTP-C2`),
allowing a single request to exhaust server memory.

**Rule:** Always wrap `r.Body` with `http.MaxBytesReader` before reading:

```go
const DefaultMaxBodySize int64 = 10 * 1024 * 1024 // 10MB
r.Body = http.MaxBytesReader(w, r.Body, DefaultMaxBodySize)
```

This applies to JSON decoding, form parsing, file uploads, and any `io.ReadAll` call.

### 4.2 Never Expose Internal Errors to Clients

We found raw `err.Error()` messages sent to HTTP clients (`HTTP-H1`) and gRPC clients
(`INFRA-H1`), potentially leaking database connection strings, file paths, and SQL.

**Rules:**
- Return generic error messages to clients: `"Internal Server Error"`, `"Bad Request"`.
- Log the full error server-side with context (request ID, endpoint).
- Use a debug mode flag (default `false`) to control stack trace exposure.
- Never include `err.Error()` in response bodies in production.

### 4.3 Validate Proxy Headers Against Trusted Sources

We found rate limiting and IP detection bypassed via spoofable `X-Forwarded-For` and
`X-Real-IP` headers (`HTTP-H2`, `HTTP-H3`).

**Rule:** Only trust forwarded headers when the direct connection IP is in a configured list
of trusted proxies (validated by CIDR range):

```go
func extractIP(r *http.Request, trustedProxies []*net.IPNet) string {
    remoteIP := parseRemoteAddr(r.RemoteAddr)
    if isTrustedProxy(remoteIP, trustedProxies) {
        if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
            return strings.TrimSpace(strings.Split(xff, ",")[0])
        }
    }
    return remoteIP
}
```

### 4.4 Validate Redirect URLs

We found open redirects in redirect helpers (`HTTP-M1`, `HTTP-H5`) and the Bond `Back()`
method which trusted the `Referer` header.

**Rule:** Use `sanitizeRedirectURL()` for all redirects. Only allow:
- Relative paths starting with `/` (but not `//`)
- Absolute URLs where the host matches the request host

```go
func sanitizeRedirectURL(target, host string) string {
    if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//") {
        return target // Relative path -- safe
    }
    u, err := url.Parse(target)
    if err != nil || u.Host != host {
        return "/" // External domain -- reject
    }
    return target
}
```

### 4.5 Include Security Response Headers

We found no security headers set framework-wide (`HTTP-M3`). The `SecurityHeaders()`
middleware now sets these on every response:

| Header | Value | Purpose |
|--------|-------|---------|
| `X-Content-Type-Options` | `nosniff` | Prevent MIME type sniffing |
| `X-Frame-Options` | `DENY` | Prevent clickjacking |
| `Referrer-Policy` | `strict-origin-when-cross-origin` | Control referer leakage |
| `X-XSS-Protection` | `0` | Disable broken legacy XSS filter |
| `Permissions-Policy` | `camera=(), microphone=(), geolocation=()` | Restrict browser features |

**Rule:** Apply the `SecurityHeaders()` middleware to all routes. Add `Strict-Transport-Security`
when TLS is enabled.

### 4.6 Validate WebSocket Origins

We found the default WebSocket config allowed all origins (`HTTP-H4`), enabling Cross-Site
WebSocket Hijacking.

**Rule:** Default to same-origin validation. Require an explicit allowlist for cross-origin
WebSocket connections. Never use `*` with credentials.

### 4.7 Bound Rate Limiter Memory

We found rate limiter maps could grow unboundedly via IP flooding (`HTTP-M7`).

**Rules:**
- Cap the maximum number of entries (e.g., 100,000).
- Implement LRU eviction when at capacity.
- Run a background cleanup goroutine to remove expired entries.
- Pair with trusted proxy validation (4.3) to prevent key flooding via spoofed IPs.

---

## 5. File System & Storage

### 5.1 Prevent Path Traversal

We found path traversal in local storage (`INFRA-C1`), email templates (`INFRA-H3`), and
file attachments (`INFRA-C4`). `filepath.Join` and `filepath.Clean` alone are NOT sufficient.

**Rule:** After cleaning and joining, verify the resolved path is within the intended root:

```go
func safePath(root, userPath string) (string, error) {
    cleanRoot := filepath.Clean(root) + string(filepath.Separator)
    full := filepath.Join(root, filepath.Clean(userPath))
    cleanFull := filepath.Clean(full)
    if !strings.HasPrefix(cleanFull, cleanRoot) {
        return "", fmt.Errorf("path traversal detected: %s", userPath)
    }
    return cleanFull, nil
}
```

**Applies to:** Local storage operations (Put, Get, Delete, Copy, Move), template loading,
file attachments, SQLite database paths, and any path built from user input.

### 5.2 Reject Path Traversal Components Explicitly

For S3 and non-filesystem paths, reject `..` components and return an explicit error -- not
an empty string or silent fallback:

```go
if strings.Contains(path, "..") {
    return "", fmt.Errorf("path traversal detected in: %s", path)
}
```

### 5.3 Limit Stream Sizes

We found `io.ReadAll` on unbounded streams in S3 and memory storage drivers (`INFRA-H4`).

**Rule:** Always wrap streams with `io.LimitReader` before reading into memory:

```go
limited := io.LimitReader(stream, maxSize)
data, err := io.ReadAll(limited)
```

### 5.4 Use Restrictive File Permissions

We found cache files created as world-readable (`INFRA-M3`).

**Required permissions:**
- Directories: `0700` (owner only)
- Files: `0600` (owner only)
- Never use `0644` or `0755` for files containing sensitive data (cache, sessions, logs).

---

## 6. Email

### 6.1 Sanitize All Email Headers

We found CRLF injection in every email header field (`INFRA-C2`), allowing attackers to inject
arbitrary headers including BCC recipients.

**Rule:** Strip `\r` and `\n` from all header values before use:

```go
func sanitizeHeader(value string) string {
    value = strings.ReplaceAll(value, "\r", "")
    value = strings.ReplaceAll(value, "\n", "")
    return value
}
```

**Applies to:** From, To, CC, BCC, Reply-To, Subject, and all custom headers.

### 6.2 Validate Attachment Paths

We found arbitrary file read via `AttachFile` (`INFRA-C4`).

**Rules:**
- Reject paths containing `..`
- Use `filepath.Clean` and verify the resolved path is within an allowed directory
- Sanitize filenames: strip CRLF, quotes, and path separators

### 6.3 Generate Random MIME Boundaries

We found hardcoded MIME boundaries (`INFRA-H5`) that could be spoofed in email content.

**Rule:** Generate unique boundaries using `crypto/rand`:

```go
b := make([]byte, 16)
rand.Read(b)
boundary := base64.URLEncoding.EncodeToString(b)
```

### 6.4 Verify Webhook Signatures Properly

We found Mailgun webhook verification only checked that the signature was non-empty
(`INFRA-C3`).

**Rule:** Implement full HMAC-SHA256 verification with constant-time comparison:

```go
mac := hmac.New(sha256.New, []byte(signingKey))
mac.Write([]byte(timestamp + token))
expected := mac.Sum(nil)
if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(expected)), []byte(signature)) != 1 {
    return ErrInvalidSignature
}
```

---

## 7. Queue & Background Jobs

### 7.1 Sign All Queue Payloads

We found queue payloads deserialized and executed without integrity verification (`INFRA-C5`).
An attacker with Redis/DB access could inject arbitrary job payloads.

**Rule:** Sign payloads with HMAC-SHA256 before enqueuing and verify on dequeue:

```go
// Enqueue
signature := signPayload(payload, signingKey)
store(signature + "." + payload)

// Dequeue
parts := strings.SplitN(data, ".", 2)
if !verifyPayload(parts[1], parts[0], signingKey) {
    return ErrTamperedPayload
}
```

### 7.2 Use Dedicated Signing Keys

Queue signing should use a dedicated `QUEUE_SIGNING_KEY`. If falling back to `APP_KEY`,
derive a separate subkey via HKDF to ensure cryptographic key separation.

### 7.3 Clean Up Job Store Entries

We found jobs stored in a global map that were never removed (`INFRA-M4`).

**Rule:** Call `Remove()` after job completion (success or failure). Implement a cleanup
mechanism for orphaned entries.

---

## 8. gRPC

### 8.1 Redact Sensitive Metadata in Logs

We found bearer tokens logged in gRPC event metadata (`INFRA-H2`).

**Rule:** Redact these metadata keys before logging:

```
authorization, cookie, set-cookie, x-api-key, *token*, *secret*
```

### 8.2 Control Error Detail with Debug Mode

We found internal Go error messages (with DB connection strings) sent to gRPC clients
(`INFRA-H1`).

**Rule:** In production, return only the gRPC status code and a generic message. Include
error details only when debug mode is explicitly enabled.

### 8.3 Default to TLS for gRPC Connections

We found insecure credentials used by default for gateway-to-backend connections (`INFRA-H7`).

**Rule:** Require TLS configuration. If insecure mode is needed for development, require
explicit opt-in and log a prominent warning.

---

## 9. Initialization & Configuration

### 9.1 Fail Loudly on Misconfiguration

We found multiple silent initialization failures: crypto init (`CRYPTO-H2`), CSRF middleware
(`R-M6`), and JWT secret (`AUTH-C2`) all silently fell back to insecure defaults.

**Rule:** Security-critical initialization failures must **panic** with a descriptive message.
Never silently fall back to insecure defaults.

```go
// WRONG -- found in crypto/init.go
if err := Init(config); err != nil {
    log.Println("crypto init failed:", err)  // App runs without encryption
}

// CORRECT
if err := Init(config); err != nil {
    panic(fmt.Sprintf("crypto: failed to initialize: %v", err))
}
```

### 9.2 Validate Configuration at Startup

Check these at application startup, before serving any requests:

| Config | Validation |
|--------|-----------|
| `APP_KEY` / `CRYPTO_KEY` | Non-empty, valid base64, correct length for cipher |
| `JWT_SECRET` | Minimum 32 bytes |
| `DB_SSLMODE` (Postgres) | Not `disable` in production |
| `SESSION_SECURE` | `true` in production |
| Queue signing key | Present or derivable from APP_KEY |

### 9.3 Never Commit Secrets

The `.gitignore` must include:
```
.env
*.pem
*.key
credentials.json
```

Store credentials as plain strings only when necessary in struct fields. Consider using
`[]byte` with explicit zeroing after use for high-sensitivity values.

---

## 10. Concurrency & Resource Management

### 10.1 Manage Goroutine Lifecycles

We found goroutine leaks in the CSRF session store (`CSRF-L2`), JWT cache cleanup (`R3-L2`),
and rate limiter.

**Rules:**
- Every goroutine spawned for background work must have a shutdown mechanism.
- Accept `context.Context` for lifecycle management, or provide an explicit `Stop()`/`Close()` method.
- Document shutdown requirements prominently.

```go
func NewStore(ctx context.Context) *Store {
    s := &Store{...}
    go s.cleanupLoop(ctx)  // Stops when ctx is cancelled
    return s
}
```

### 10.2 Use Context-Based Storage Over Global Maps

We refactored the session guard from a global `map[*http.Request]Session` to context-based
storage (`R-H5`). This eliminated both the memory leak and the need for manual cleanup.

**Rule:** Prefer `context.WithValue` for per-request data over global maps keyed by request
pointers:

```go
type ctxKey struct{}

func WithValue(r *http.Request, val interface{}) *http.Request {
    return r.WithContext(context.WithValue(r.Context(), ctxKey{}, val))
}
```

---

## 11. Input Validation

### 11.1 Block SSRF via URL Validation

We found URL validation accepted internal IPs (`INFRA-M5`) like `169.254.169.254` (AWS
metadata endpoint).

**Rule:** When validating user-supplied URLs that the server will fetch, use `URLPublicRule()`
which resolves the hostname and blocks:

- `127.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (private IPv4)
- `169.254.0.0/16` (link-local / cloud metadata)
- `::1/128`, `fc00::/7`, `fe80::/10` (private IPv6)

### 11.2 Pre-Compile Regular Expressions

We found regex compiled on every validation call (`INFRA-M6`), causing unnecessary CPU usage.

**Rule:** Compile regex patterns at package init time, not per-call:

```go
// CORRECT -- compile once
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

func ValidateEmail(email string) bool {
    return emailRegex.MatchString(email)
}
```

---

## 12. Code Review Checklist

Use this checklist when reviewing PRs that touch security-sensitive code:

### SQL / ORM
- [ ] No string concatenation of user input into SQL
- [ ] All identifiers validated with `validateIdentifier()` or quoted with `QuoteIdentifier()`
- [ ] Operators checked against allowlist
- [ ] String defaults in DDL have quotes escaped
- [ ] Mass assignment protection via Fillable/Guarded
- [ ] Transactions properly scoped

### Crypto / Auth
- [ ] No hardcoded keys or secrets
- [ ] `crypto/rand` used for all randomness (no `math/rand`, no `time.Now()` fallback)
- [ ] `subtle.ConstantTimeCompare` for all secret comparisons
- [ ] HKDF for key derivation with domain separation
- [ ] JWT tokens extracted from Authorization header only
- [ ] Session cookies have Secure, HttpOnly, SameSite set

### HTTP
- [ ] Request bodies wrapped with `http.MaxBytesReader`
- [ ] No `err.Error()` in client responses
- [ ] Redirect URLs validated (no open redirects)
- [ ] Proxy headers only trusted from configured sources
- [ ] Security headers middleware applied

### File System
- [ ] Path traversal prevented with `safePath()` pattern
- [ ] File permissions 0600/0700
- [ ] Streams size-limited with `io.LimitReader`
- [ ] Attachment filenames sanitized

### Email
- [ ] All headers sanitized for CRLF
- [ ] MIME boundaries randomly generated
- [ ] Webhook signatures verified with HMAC + constant-time compare

### Concurrency
- [ ] Maps accessed from multiple goroutines have mutex protection
- [ ] In-memory caches have size limits and TTL
- [ ] Background goroutines have shutdown mechanisms
- [ ] No `*http.Request` used as map keys (use context instead)

---

*Derived from security audit rounds 1-3 (2026-02-10 to 2026-02-11). See `SECURITY_AUDIT.md`
for the full vulnerability history and remediation tracking.*
