# Security Audit Report: Velocity Framework

**Date:** 2026-02-11 (Round 2)
**Scope:** Full codebase re-audit of `github.com/velocitykode/velocity` after remediation
**Go Version:** 1.25.1
**Framework Type:** Laravel-inspired Go web framework

---

## Executive Summary

**Round 1** (2026-02-10) identified **93 findings** (14 Critical, 21 High, 30 Medium, 28 Low).

**Round 2** (2026-02-11) re-audited all fixes. Results:

| Category | Round 1 | Fixed | Partially Fixed | Not Fixed | New Issues | Remaining |
|----------|---------|-------|-----------------|-----------|------------|-----------|
| Critical | 14 | 13 | 0 | 0 | 0 | 1 |
| High | 21 | 16 | 3 | 1 | 2 | 6 |
| Medium | 30 | 23 | 2 | 0 | 4 | 9 |
| Low | 28 | 24 | 2 | 0 | 2 | 6 |
| **Total** | **93** | **76** | **7** | **1** | **8** | **22** |

**Fix rate: 82%** (76/93 fully resolved). All 14 original Critical findings now resolved except one partially fixed ORM finding. The codebase security posture has improved dramatically.

---

## Table of Contents

1. [Remediation Verification Summary](#1-remediation-verification-summary)
2. [Remaining Issues](#2-remaining-issues)
3. [New Issues Discovered](#3-new-issues-discovered)
4. [New Security Features Added](#4-new-security-features-added)
5. [Detailed Verification by Category](#5-detailed-verification-by-category)
6. [Remediation Priority Matrix](#6-remediation-priority-matrix)

---

## 1. Remediation Verification Summary

### All Original Critical Findings -- Status

| ID | Finding | Status |
|----|---------|--------|
| AUTH-C1 | Hardcoded session encryption key | **FIXED** -- Requires `CRYPTO_KEY`/`APP_KEY`, panics if not set |
| AUTH-C2 | Empty JWT secret accepted | **FIXED** -- 32-byte minimum enforced, panics if too short |
| CRYPTO-C1 | CBC MAC bypass (padding oracle) | **FIXED** -- MAC verified before padding removal, constant-time comparison |
| CRYPTO-C2 | Incomplete PKCS#7 validation | **FIXED** -- Full constant-time padding byte verification |
| ORM-C1 | `Hash()` returns plaintext | **FIXED** -- Uses `bcrypt.GenerateFromPassword` correctly |
| ORM-C2 | Column name SQL injection | **PARTIALLY FIXED** -- `FindBy`/`Update` validate; 7 other methods don't (see below) |
| ORM-C3 | `QuoteIdentifier` escape bypass | **FIXED** -- All 3 drivers escape internal quotes |
| ORM-C4 | Operator SQL injection | **FIXED** -- 17-operator allowlist with validation |
| ORM-C5 | JOIN clause injection | **FIXED** -- Operator validated + identifiers quoted |
| INFRA-C1 | Storage path traversal | **FIXED** -- `safePath()` validates paths stay within root |
| INFRA-C2 | Email header CRLF injection | **FIXED** -- `sanitizeHeader()` strips `\r` and `\n` |
| INFRA-C3 | Mailgun webhook verification | **FIXED** -- HMAC-SHA256 with `subtle.ConstantTimeCompare` |
| INFRA-C4 | Arbitrary file read via AttachFile | **FIXED** -- Rejects `..` paths, uses `filepath.Clean` |
| INFRA-C5 | Queue insecure deserialization | **FIXED** -- New `signing.go` with HMAC-SHA256 verification |
| HTTP-C1 | Unbounded body read | **FIXED** -- `http.MaxBytesReader` (10MB default) |
| HTTP-C2 | Unbounded JSON parsing | **FIXED** -- `http.MaxBytesReader` wraps body before decode |

### All Original High Findings -- Status

| ID | Finding | Status |
|----|---------|--------|
| AUTH-H1 | JWT blacklist race condition | **FIXED** -- `sync.RWMutex` on all operations |
| AUTH-H2 | JWT in URL query params | **FIXED** -- Restricted to WebSocket upgrade only |
| AUTH-H3 | JWT user cache race condition | **FIXED** -- `sync.RWMutex` protection |
| AUTH-H4 | Session guard memory leak | **PARTIALLY FIXED** -- `CleanupRequest()` exists but requires manual call |
| AUTH-H5 | In-memory blacklist lost on restart | Not addressed (architectural; documented as limitation) |
| AUTH-H6 | Session secure cookie defaults false | **FIXED** -- Defaults to `true` |
| AUTH-H7 | Predictable session ID fallback | **NOT FIXED** -- Still falls back to `time.Now().String()` |
| ORM-H1 | ORDER BY injection | **PARTIALLY FIXED** -- Direction validated; column quoted but not pre-validated |
| ORM-H2 | Mass assignment | **FIXED** -- Fillable/Guarded interfaces enforced |
| ORM-H3 | PostgreSQL SSL disabled | **FIXED** -- Defaults to `sslmode=prefer` |
| ORM-H4 | Broken transaction | **FIXED** -- Proper tx scope with defer/recover |
| ORM-H5 | DDL default value injection | **FIXED** -- String quoting with `'` escaping |
| ORM-H6 | DDL statement injection | **FIXED** -- `quoteIdentifier()` used throughout |
| ORM-H7 | Testing helper injection | **PARTIALLY FIXED** -- Metadata-sourced names but still unquoted |
| INFRA-H1 | gRPC error leak | **FIXED** -- Debug mode controls detail exposure |
| INFRA-H2 | Bearer tokens in gRPC logs | **PARTIALLY FIXED** -- `authorization` redacted; other auth headers not |
| INFRA-H3 | Email template traversal | **FIXED** -- Validates no separators, checks `..`, verifies prefix |
| INFRA-H4 | Unbounded PutStream | **FIXED** -- `io.LimitReader` with size checks in all drivers |
| INFRA-H5 | Static MIME boundary | **FIXED** -- `crypto/rand` + base64 dynamic generation |
| INFRA-H6 | Attachment filename injection | **FIXED** -- `sanitizeFilename()` strips CRLF/quotes |
| INFRA-H7 | gRPC gateway insecure default | **PARTIALLY FIXED** -- TLS available but insecure is default |
| HTTP-H1 | Error info leaks | **FIXED** -- Debug mode (default false) controls stack traces |
| HTTP-H2 | Rate limit header spoofing | **FIXED** -- `WithTrustedProxies()` validates source IP |
| HTTP-H3 | IP spoofing via proxy headers | **FIXED** -- Only trusts headers from verified proxies |
| HTTP-H4 | WebSocket origin bypass | **FIXED** -- Same-origin default; explicit allowlist required |
| HTTP-H5 | Open redirect via Referer | **FIXED** -- `sanitizeRedirectURL()` validates host |

---

## 2. Remaining Issues

### HIGH (6 remaining)

#### R-H1: Predictable Remember Token Fallback (was AUTH-H7)
- **File:** `pkg/auth/drivers/guards/session.go:305-312`
- **Status:** NOT FIXED
- **Issue:** If `crypto/rand.Read` fails, `generateRememberToken()` falls back to `base64(time.Now().String())`. Timestamps have microsecond resolution (~1M guesses/sec to brute force).
- **Remediation:** Panic on `rand.Read` failure instead of using insecure fallback.

#### R-H2: JWT Guard User Cache Memory Leak (NEW)
- **File:** `pkg/auth/drivers/guards/jwt.go:12-28`
- **Issue:** `userCache` map stores users by token indefinitely. No eviction, no size limit, no TTL.
- **Impact:** Memory exhaustion over time with many unique tokens.
- **Remediation:** Add LRU eviction, TTL-based cache, or per-request scoping.

#### R-H3: Remember Cookie Validation Always Returns False (NEW)
- **File:** `pkg/auth/drivers/guards/session.go:231-247`
- **Issue:** `checkRememberCookie()` unconditionally returns `false`. Remember-me feature is non-functional.
- **Impact:** Feature broken; users cannot stay authenticated across sessions.
- **Remediation:** Complete the implementation to verify decrypted token against database.

#### R-H4: gRPC Gateway Defaults to Insecure Credentials
- **File:** `pkg/grpc/gateway.go:51-53`
- **Status:** PARTIALLY FIXED (was INFRA-H7)
- **Issue:** Gateway defaults to `insecure.NewCredentials()`. TLS is available via `GatewayWithTLS()` but not default.
- **Remediation:** Default to TLS; make insecure an explicit opt-in for development.

#### R-H5: Session Guard Requires Manual Cleanup
- **File:** `pkg/auth/drivers/guards/session.go:23,195-199`
- **Status:** PARTIALLY FIXED (was AUTH-H4)
- **Issue:** `CleanupRequest()` must be called manually. `*http.Request` pointers as map keys prevent GC if not cleaned.
- **Remediation:** Add middleware wrapper or use request context for automatic cleanup.

#### R-H6: In-Memory JWT Blacklist Not Persistent (was AUTH-H5)
- **File:** `pkg/auth/jwt.go`
- **Status:** Architectural limitation (documented)
- **Issue:** Token revocations lost on restart, not shared across instances.
- **Note:** `BlacklistStore` interface allows custom implementations. Framework users should implement Redis/DB-backed store for production.

### MEDIUM (9 remaining)

#### R-M1: Inconsistent Column Name Validation in ORM (was ORM-C2)
- **File:** `pkg/orm/query.go`, `pkg/orm/model.go`
- **Issue:** `FindBy` and `Update` validate column names, but 7 methods don't: `WhereIn`, `WhereNotIn`, `WhereNull`, `WhereNotNull`, `WhereBetween`, `GroupBy`, `Having`.
- **Mitigation:** `QuoteIdentifier()` provides escaping, but defense-in-depth requires pre-validation.

#### R-M2: Testing Helper Unquoted Table Names (was ORM-H7)
- **File:** `pkg/orm/testing/refresh.go:79,86,129,141,154`
- **Issue:** Table names from `GetAllTables()` used in DDL without quoting. `factory.go` has `quoteIdent()` but `refresh.go` doesn't use it.
- **Risk:** Low (names from DB metadata), but inconsistent with rest of codebase.

#### R-M3: OrderBy Column Not Pre-Validated (was ORM-H1)
- **File:** `pkg/orm/query.go:230-241`
- **Issue:** Direction validated against ASC/DESC. Column is quoted by driver but not pre-validated.

#### R-M4: Queue Signing Can Be Disabled via Environment Variable (NEW)
- **File:** `pkg/queue/signing.go:24-40`
- **Issue:** `QUEUE_SIGNING_DISABLED=true` bypasses all payload integrity checks. No code-level override.
- **Remediation:** Remove the disable toggle, or require explicit code-level opt-out.

#### R-M5: Queue Signing Falls Back to APP_KEY (NEW)
- **File:** `pkg/queue/signing.go:24-40`
- **Issue:** If `QUEUE_SIGNING_KEY` is unset, falls back to `APP_KEY`. Key reuse across subsystems weakens isolation.

#### R-M6: Silent CSRF Middleware Bypass When Uninitialized (NEW)
- **File:** `pkg/csrf/helpers.go:75-77`
- **Issue:** If `SetGlobalCSRF()` is never called, CSRF middleware silently passes all requests through.
- **Remediation:** Panic or log warning when middleware invoked without initialization.

#### R-M7: S3 Path Traversal Returns Empty String (NEW)
- **File:** `pkg/storage/s3.go:517-518`
- **Issue:** `cleanPath()` returns empty string when `..` detected instead of an error. Silent failure.
- **Remediation:** Return explicit error so callers can distinguish invalid paths.

#### R-M8: gRPC Gateway Endpoint Not Validated
- **File:** `pkg/grpc/gateway.go:173-175`
- **Issue:** Only checks endpoint is non-empty. No URL validation.

#### R-M9: CSRF SessionStore Goroutine Cleanup (was CSRF-L2)
- **File:** `pkg/csrf/stores/session.go`
- **Issue:** `Close()` method exists but no mechanism ensures it's called automatically.

### LOW (6 remaining)

| ID | Finding | File |
|----|---------|------|
| R-L1 | gRPC metadata redaction incomplete (only `authorization`) | `pkg/grpc/interceptors/logging.go:245-258` |
| R-L2 | CSRF middleware return value misleading (returns nil on rejection) | `pkg/csrf/helpers.go:88` |
| R-L3 | CSRF session ID generation silent failure on rand error | `pkg/csrf/csrf.go:129-131` |
| R-L4 | KDF uses nil salt in subkey derivation | `pkg/crypto/drivers/aes.go:68-74` |
| R-L5 | Double KDF path (inconsistent derivation based on input size) | `pkg/crypto/crypto.go:191-200`, `pkg/crypto/drivers/aes.go:52-62` |
| R-L6 | Cache directory permissions 0755 (world-readable listing) | `pkg/cache/drivers/file.go:35` |

---

## 3. New Issues Discovered

Eight new issues were found during Round 2 that were not present in the original audit:

| ID | Severity | Finding | Source |
|----|----------|---------|--------|
| R-H2 | HIGH | JWT Guard user cache memory leak (unbounded) | `pkg/auth/drivers/guards/jwt.go` |
| R-H3 | HIGH | Remember cookie validation always returns false | `pkg/auth/drivers/guards/session.go` |
| R-M4 | MEDIUM | Queue signing disable via env var | `pkg/queue/signing.go` |
| R-M5 | MEDIUM | Queue signing falls back to APP_KEY | `pkg/queue/signing.go` |
| R-M6 | MEDIUM | Silent CSRF bypass when uninitialized | `pkg/csrf/helpers.go` |
| R-M7 | MEDIUM | S3 path traversal returns empty string (silent) | `pkg/storage/s3.go` |
| R-L4 | LOW | KDF nil salt in subkey derivation | `pkg/crypto/drivers/aes.go` |
| R-L5 | LOW | Double KDF path complexity | `pkg/crypto/crypto.go` + `aes.go` |

---

## 4. New Security Features Added

The following security features were added as part of the remediation:

### Security Headers Middleware (`pkg/router/security_headers.go`)
Sets `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: strict-origin-when-cross-origin`, `X-XSS-Protection: 0`, `Permissions-Policy: camera=(), microphone=(), geolocation=()`.

### CORS Middleware (`pkg/router/cors.go`)
Complete CORS implementation with origin allowlist validation, preflight handling, `Vary: Origin` header, and wildcard-with-credentials rejection per spec.

### Queue Payload Signing (`pkg/queue/signing.go`)
HMAC-SHA256 payload signing for Redis and database queue drivers. Prevents deserialization of tampered payloads.

### URL Public Validation Rule (`pkg/validation/rules/string.go`)
New `URLPublicRule()` resolves hostnames and blocks private/internal IPs (127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, ::1/128, fc00::/7, fe80::/10).

### Rate Limiter Hardening (`pkg/router/rate_limit.go`)
100K entry cap with LRU eviction, trusted proxy validation, and background cleanup goroutine.

### Mass Assignment Protection (`pkg/orm/model.go`)
`Fillable` and `Guarded` interfaces for ORM models with enforcement in `mapToStruct`.

---

## 5. Detailed Verification by Category

### Authentication & Authorization

| ID | Status | Evidence |
|----|--------|----------|
| AUTH-C1 | FIXED | No hardcoded key; requires `CRYPTO_KEY`/`APP_KEY` |
| AUTH-C2 | FIXED | 32-byte minimum enforced; panics on violation |
| AUTH-H1 | FIXED | `sync.RWMutex` on `InMemoryBlacklistStore` |
| AUTH-H2 | FIXED | Query params restricted to WebSocket upgrades |
| AUTH-H3 | FIXED | `sync.RWMutex` on user cache |
| AUTH-H4 | PARTIAL | `CleanupRequest()` available but manual |
| AUTH-H6 | FIXED | `Secure` defaults to `true` |
| AUTH-H7 | NOT FIXED | `time.Now().String()` fallback remains |
| AUTH-M1 | FIXED | Issuer/audience set and validated |
| AUTH-M2 | FIXED | `TokenType` claim distinguishes access/refresh |
| AUTH-M3 | FIXED | Each token type uses correct TTL for blacklist |
| AUTH-M5 | FIXED | Blacklisting enabled by default |
| AUTH-M6 | FIXED | Strict "Bearer " prefix required |
| AUTH-M8 | FIXED | Database UPDATE query persists token |
| AUTH-M9 | FIXED | Proper double-check locking pattern |

### ORM & SQL Injection

| ID | Status | Evidence |
|----|--------|----------|
| ORM-C1 | FIXED | `bcrypt.GenerateFromPassword` used correctly |
| ORM-C2 | PARTIAL | `FindBy`/`Update` validate; 7 other methods don't |
| ORM-C3 | FIXED | All 3 drivers escape internal quotes |
| ORM-C4 | FIXED | 17-operator allowlist enforced |
| ORM-C5 | FIXED | Operator validated + identifiers quoted |
| ORM-H1 | PARTIAL | Direction validated; column quoted but not pre-validated |
| ORM-H2 | FIXED | Fillable/Guarded interface enforced in `mapToStruct` |
| ORM-H3 | FIXED | `sslmode=prefer` default |
| ORM-H4 | FIXED | Transaction scope correct with recover/rollback |
| ORM-H5 | FIXED | String quoting with `'` escaping |
| ORM-H6 | FIXED | `quoteIdentifier()` on all DDL |
| ORM-H7 | PARTIAL | DB metadata-sourced but unquoted |

### Cryptography & CSRF

| ID | Status | Evidence |
|----|--------|----------|
| CRYPTO-C1 | FIXED | MAC verified before padding removal, constant-time |
| CRYPTO-C2 | FIXED | All padding bytes verified, constant-time |
| CRYPTO-H1 | FIXED | Default cipher now AES-256-GCM |
| CRYPTO-H2 | FIXED | Panics on initialization failure |
| CSRF-M1 | FIXED | `crypto/rand` for session IDs, not RemoteAddr |
| CSRF-M2 | FIXED | `TokenLifetime` properly passed through |
| CRYPTO-M1 | FIXED | HKDF-SHA256 key derivation |
| CRYPTO-M2 | FIXED | Separate encryption and HMAC subkeys |
| CSRF-L1 | FIXED | TRACE not in safe methods list |
| CSRF-L3 | FIXED | Error now logged |
| CSRF-L4 | PARTIAL | Response correct but return value misleading |

### HTTP Router & Web Security

| ID | Status | Evidence |
|----|--------|----------|
| HTTP-C1 | FIXED | `MaxBytesReader` on body reads |
| HTTP-C2 | FIXED | `MaxBytesReader` before JSON decode |
| HTTP-H1 | FIXED | Debug mode (default false) controls exposure |
| HTTP-H2 | FIXED | `WithTrustedProxies()` validates source |
| HTTP-H3 | FIXED | Headers trusted only from verified proxies |
| HTTP-H4 | FIXED | Same-origin default for WebSocket |
| HTTP-H5 | FIXED | `sanitizeRedirectURL()` validates host |
| HTTP-M1 | FIXED | All redirect paths validated |
| HTTP-M2 | FIXED | Documented as caller responsibility |
| HTTP-M3 | FIXED | New `security_headers.go` middleware |
| HTTP-M4 | FIXED | Debug defaults false with warnings |
| HTTP-M5 | FIXED | Generic error messages, no input reflection |
| HTTP-M6 | FIXED | New `cors.go` middleware |
| HTTP-M7 | FIXED | 100K cap + LRU eviction + cleanup goroutine |
| HTTP-M8 | FIXED | `url.PathEscape` on all parameters |
| HTTP-M9 | FIXED | `url.PathUnescape` with error handling |

### Storage, Mail, Queue & Infrastructure

| ID | Status | Evidence |
|----|--------|----------|
| INFRA-C1 | FIXED | `safePath()` with `HasPrefix` verification |
| INFRA-C2 | FIXED | `sanitizeHeader()` strips CRLF |
| INFRA-C3 | FIXED | HMAC-SHA256 with constant-time compare |
| INFRA-C4 | FIXED | Path traversal blocked, `filepath.Clean` |
| INFRA-C5 | FIXED | HMAC signing via `signing.go` |
| INFRA-H1 | FIXED | Debug mode controls error detail |
| INFRA-H2 | PARTIAL | `authorization` redacted; other auth headers not |
| INFRA-H3 | FIXED | Path separator check + prefix verification |
| INFRA-H4 | FIXED | `io.LimitReader` in all drivers |
| INFRA-H5 | FIXED | `crypto/rand` boundary generation |
| INFRA-H6 | FIXED | CRLF/quote stripping |
| INFRA-H7 | PARTIAL | TLS available but insecure default |
| INFRA-M5 | FIXED | `URLPublicRule` blocks private IPs |
| INFRA-M6 | FIXED | Regex pre-compiled at module level |

---

## 6. Remediation Priority Matrix

### Immediate (P0) -- Fix before release

| Finding | Risk | Effort |
|---------|------|--------|
| R-H1: Predictable remember token fallback | Token forging | Low |
| R-H3: Remember cookie validation broken | Feature non-functional | Medium |
| R-M1: Inconsistent column validation (7 methods) | SQL injection surface | Medium |

### Short-term (P1) -- Fix within next sprint

| Finding | Risk | Effort |
|---------|------|--------|
| R-H2: JWT user cache memory leak | Memory exhaustion | Medium |
| R-H5: Session guard manual cleanup | Memory leak | Medium |
| R-M4: Queue signing disable toggle | Integrity bypass | Low |
| R-M6: Silent CSRF middleware bypass | CSRF bypass | Low |

### Medium-term (P2) -- Plan for upcoming releases

| Finding | Risk | Effort |
|---------|------|--------|
| R-H4: gRPC gateway insecure default | Credential exposure | Low |
| R-M5: Queue signing key reuse | Key isolation | Low |
| R-M7: S3 path traversal silent failure | Silent errors | Low |
| R-M8: gRPC endpoint not validated | Input validation | Low |
| R-M9: CSRF cleanup goroutine lifecycle | Resource leak | Low |

### Long-term (P3) -- Track in backlog

All remaining LOW findings (R-L1 through R-L6).

---

## Positive Findings (Maintained + New)

**Carried from Round 1 (still correct):**
- CSRF token generation uses `crypto/rand.Read`
- CSRF comparison uses `crypto/subtle.ConstantTimeCompare`
- AES IV/nonce generation uses `io.ReadFull(rand.Reader, ...)`
- No `math/rand` for security-sensitive operations
- Bcrypt used for password hashing
- CI pipeline includes `govulncheck`

**New in Round 2:**
- **HKDF-SHA256** key derivation with separate encryption/HMAC subkeys
- **AES-256-GCM** as default cipher (authenticated encryption)
- **Mass assignment protection** via Fillable/Guarded interfaces
- **CORS middleware** with proper spec compliance
- **Security headers middleware** with 5 protective headers
- **Rate limiter hardening** with trusted proxy validation and memory bounds
- **URL SSRF protection** blocking private/internal IP ranges
- **Queue payload signing** with HMAC-SHA256 integrity verification
- **Mailgun webhook verification** with proper HMAC-SHA256
- **Redirect sanitization** across all redirect helpers
- **WebSocket origin validation** enabled by default

---

*Report generated by security re-audit on 2026-02-11.*
*Round 1: 2026-02-10 | Round 2: 2026-02-11*
