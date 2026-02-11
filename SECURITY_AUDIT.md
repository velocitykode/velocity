# Security Audit Report: Velocity Framework

**Date:** 2026-02-11 (Round 3)
**Scope:** Full codebase re-audit of `github.com/velocitykode/velocity` after remediation
**Go Version:** 1.25.1
**Framework Type:** Laravel-inspired Go web framework

---

## Executive Summary

| Round | Date | Total | Critical | High | Medium | Low |
|-------|------|-------|----------|------|--------|-----|
| 1 | 2026-02-10 | 93 | 14 | 21 | 30 | 28 |
| 2 | 2026-02-11 | 22 | 1 | 6 | 9 | 6 |
| **3** | **2026-02-11** | **7** | **0** | **1** | **4** | **2** |

**Round 3 fix rate: 68%** (15/22 remaining issues resolved). **Cumulative fix rate: 92%** (86/93 original findings resolved).

**No Critical findings remain.** The codebase has progressed from 14 Critical vulnerabilities to zero across three rounds. The 7 remaining items are architectural limitations or minor hardening opportunities -- none represent exploitable vulnerabilities with default configuration.

---

## Table of Contents

1. [Round 3 Verification Results](#1-round-3-verification-results)
2. [Remaining Issues (7)](#2-remaining-issues)
3. [Full Audit History](#3-full-audit-history)
4. [Security Posture Assessment](#4-security-posture-assessment)

---

## 1. Round 3 Verification Results

### Previously HIGH (6 items) -- 5 Fixed, 1 Remaining

| ID | Finding | R2 Status | R3 Status |
|----|---------|-----------|-----------|
| R-H1 | Predictable remember token fallback | NOT FIXED | **FIXED** -- Panics on `rand.Read` failure |
| R-H2 | JWT guard user cache memory leak | NEW | **FIXED** -- TTL (5min) + size cap (10K) + LRU eviction + background cleanup |
| R-H3 | Remember cookie validation always false | NEW | **FIXED** -- Full validation: decrypt, parse, DB lookup, constant-time compare |
| R-H4 | gRPC gateway insecure default | PARTIAL | **NOT FIXED** -- Warning logged but insecure still default without TLS config |
| R-H5 | Session guard manual cleanup | PARTIAL | **FIXED** -- Refactored to context-based storage; automatic GC on request end |
| R-H6 | In-memory JWT blacklist | Architectural | **IMPROVED** -- `SetBlacklistStore()` + documented interface; still defaults to in-memory |

### Previously MEDIUM (9 items) -- 7 Fixed, 2 Remaining

| ID | Finding | R2 Status | R3 Status |
|----|---------|-----------|-----------|
| R-M1 | Inconsistent column validation (7 methods) | PARTIAL | **FIXED** -- All 7 methods now call `validateIdentifier()` |
| R-M2 | Testing helper unquoted table names | PARTIAL | **FIXED** -- `quoteIdentifier()` used on all DDL in refresh.go |
| R-M3 | OrderBy column not pre-validated | PARTIAL | **FIXED** -- `validateIdentifier(column)` called before use |
| R-M4 | Queue signing disable via env var | NEW | **FIXED** -- `QUEUE_SIGNING_DISABLED` env var removed |
| R-M5 | Queue signing falls back to APP_KEY | NEW | **PARTIALLY FIXED** -- HKDF derivation prevents key reuse, but fallback remains |
| R-M6 | Silent CSRF bypass when uninitialized | NEW | **PARTIALLY FIXED** -- Warning logged, but still passes requests through |
| R-M7 | S3 path traversal silent failure | NEW | **FIXED** -- Returns explicit `fmt.Errorf("path traversal detected")` |
| R-M8 | gRPC gateway endpoint not validated | Unresolved | **FIXED** -- `net.SplitHostPort()` validates host:port format |
| R-M9 | CSRF SessionStore goroutine cleanup | PARTIAL | **PARTIALLY FIXED** -- Properly documented; `Close()` handles cancellation but requires manual call |

### Previously LOW (6 items) -- 4 Fixed, 2 Remaining

| ID | Finding | R2 Status | R3 Status |
|----|---------|-----------|-----------|
| R-L1 | gRPC metadata redaction incomplete | Unresolved | **FIXED** -- Redacts `authorization`, `cookie`, `set-cookie`, `x-api-key`, `*token*`, `*secret*` |
| R-L2 | CSRF middleware return value misleading | Unresolved | **FIXED** -- Returns `fmt.Errorf(...)` on CSRF rejection |
| R-L3 | CSRF session ID generation silent failure | Unresolved | **FIXED** -- Returns wrapped error on `rand.Read` failure |
| R-L4 | KDF nil salt in subkey derivation | Unresolved | **FIXED** -- Uses deterministic salt `"velocity-framework-hkdf-salt-v1"` |
| R-L5 | Double KDF path | Unresolved | **PARTIALLY FIXED** -- Documented; consistent salt; not consolidated |
| R-L6 | Cache directory permissions 0755 | Unresolved | **FIXED** -- Directories 0700, files 0600 |

---

## 2. Remaining Issues

### HIGH (1)

#### R3-H1: gRPC Gateway Defaults to Insecure Without TLS Config (was R-H4)
- **File:** `pkg/grpc/gateway.go:79-110`
- **Issue:** When `GRPC_GATEWAY_TLS_CERT` and `GRPC_GATEWAY_TLS_KEY` are not set, the gateway falls back to `insecure.NewCredentials()` with a warning log. TLS is not the default.
- **Risk:** LOW in practice -- this is an internal gateway-to-backend connection, typically on localhost or a private network. Warning log makes the state visible.
- **Recommendation:** Default to refusing startup without TLS. Add explicit `GatewayWithInsecure()` for development/testing opt-in.

### MEDIUM (4)

#### R3-M1: Queue Signing Falls Back to APP_KEY with HKDF (was R-M5)
- **File:** `pkg/queue/signing.go:28-58`
- **Issue:** If `QUEUE_SIGNING_KEY` is unset, derives signing key from `APP_KEY` via HKDF-SHA256 with context `"queue-signing"`.
- **Risk:** LOW -- HKDF derivation provides cryptographic key separation. Not a vulnerability, but a dedicated key is best practice.
- **Recommendation:** Log a notice recommending dedicated `QUEUE_SIGNING_KEY` for production.

#### R3-M2: CSRF Middleware Logs Warning But Passes Through When Uninitialized (was R-M6)
- **File:** `pkg/csrf/helpers.go:72-78`
- **Issue:** If `SetGlobalCSRF()` is never called, middleware logs a warning and passes all requests through.
- **Risk:** LOW -- requires developer to both register the middleware AND forget to initialize it. Warning makes it visible.
- **Recommendation:** Consider panicking in production mode or returning 500 error.

#### R3-M3: CSRF SessionStore Requires Manual Close() (was R-M9)
- **File:** `pkg/csrf/stores/session.go:27-49`
- **Issue:** Background cleanup goroutine requires manual `Close()` call. Well-documented with `IMPORTANT` comment.
- **Risk:** LOW -- goroutine leak on shutdown, not a security vulnerability.
- **Recommendation:** Provide a constructor that accepts `context.Context` for automatic shutdown.

#### R3-M4: In-Memory JWT Blacklist (was R-H6)
- **File:** `pkg/auth/jwt.go:14-132`
- **Issue:** Defaults to in-memory blacklist. `BlacklistStore` interface and `SetBlacklistStore()` allow custom persistent backends.
- **Risk:** Documented architectural limitation. Framework users must implement Redis/DB store for production multi-instance deployments.
- **Recommendation:** Consider providing a built-in Redis-backed `BlacklistStore` implementation.

### LOW (2)

#### R3-L1: Double KDF Path Not Consolidated (was R-L5)
- **File:** `pkg/crypto/drivers/aes.go:52-68`
- **Issue:** When input key size doesn't match required size, key goes through two HKDF derivations (size normalization + subkey derivation). Consistent salt and info strings used throughout.
- **Risk:** Negligible -- cryptographically sound, just adds complexity.

#### R3-L2: New -- JWT Cache Cleanup Goroutine Lifecycle
- **File:** `pkg/auth/drivers/guards/jwt.go:34-67`
- **Issue:** `NewJWTGuard` spawns a background cleanup goroutine. `StopCleanup()` must be called manually. Similar pattern to R3-M3.
- **Risk:** Goroutine leak on shutdown, not a security vulnerability.
- **Recommendation:** Accept `context.Context` parameter for automatic lifecycle management.

---

## 3. Full Audit History

### Cumulative Resolution by Category

| Category | R1 Total | After R2 | After R3 | Resolution |
|----------|----------|----------|----------|------------|
| **Critical** | 14 | 1 | **0** | All resolved |
| **High** | 21 | 6 | **1** | 95% resolved |
| **Medium** | 30 | 9 | **4** | 87% resolved |
| **Low** | 28 | 6 | **2** | 93% resolved |
| **Total** | **93** | **22** | **7** | **92% resolved** |

### All Original Critical Findings -- Final Status

| ID | Finding | Final Status |
|----|---------|-------------|
| AUTH-C1 | Hardcoded session encryption key | FIXED (R2) |
| AUTH-C2 | Empty JWT secret accepted | FIXED (R2) |
| CRYPTO-C1 | CBC MAC bypass (padding oracle) | FIXED (R2) |
| CRYPTO-C2 | Incomplete PKCS#7 validation | FIXED (R2) |
| ORM-C1 | `Hash()` returns plaintext | FIXED (R2) |
| ORM-C2 | Column name SQL injection | FIXED (R3) -- all methods now validate |
| ORM-C3 | `QuoteIdentifier` escape bypass | FIXED (R2) |
| ORM-C4 | Operator SQL injection | FIXED (R2) |
| ORM-C5 | JOIN clause injection | FIXED (R2) |
| INFRA-C1 | Storage path traversal | FIXED (R2) |
| INFRA-C2 | Email header CRLF injection | FIXED (R2) |
| INFRA-C3 | Mailgun webhook verification | FIXED (R2) |
| INFRA-C4 | Arbitrary file read via AttachFile | FIXED (R2) |
| INFRA-C5 | Queue insecure deserialization | FIXED (R2) |
| HTTP-C1 | Unbounded body read | FIXED (R2) |
| HTTP-C2 | Unbounded JSON parsing | FIXED (R2) |

---

## 4. Security Posture Assessment

### Overall Rating: GOOD

The Velocity framework has achieved a strong security posture after three rounds of audit and remediation:

**Strengths:**
- All Critical and nearly all High vulnerabilities resolved
- Proper use of `crypto/rand`, `crypto/subtle`, bcrypt, HKDF-SHA256, and AES-256-GCM
- CSRF with constant-time comparison and secure cookie defaults
- SQL injection mitigated via operator allowlists, identifier validation, and proper quoting
- Path traversal blocked across storage, mail, and S3 drivers
- Request body size limits enforced on all input parsing
- Security headers and CORS middleware provided out-of-the-box
- Rate limiting with trusted proxy validation and memory bounds
- Queue payload integrity via HMAC-SHA256 signing
- Mass assignment protection via Fillable/Guarded interfaces

**Remaining Items (non-exploitable with correct usage):**
- 1 HIGH: gRPC gateway insecure default (internal connection, warned)
- 4 MEDIUM: architectural patterns requiring manual lifecycle management or best-practice key configuration
- 2 LOW: code complexity and goroutine lifecycle patterns

**Recommendation:** The 7 remaining items are suitable for tracking in the project's issue backlog. None block a release from a security standpoint.

---

*Security audit completed over 3 rounds: 2026-02-10 to 2026-02-11.*
*Round 1: 93 findings | Round 2: 22 remaining | Round 3: 7 remaining (0 Critical).*
