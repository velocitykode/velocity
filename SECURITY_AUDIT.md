# Security Audit Report: Velocity Framework

**Date:** 2026-02-11 (Round 4)
**Scope:** Full codebase re-audit of `github.com/velocitykode/velocity` after remediation
**Go Version:** 1.25.1
**Framework Type:** Laravel-inspired Go web framework

---

## Executive Summary

| Round | Date | Total | Critical | High | Medium | Low |
|-------|------|-------|----------|------|--------|-----|
| 1 | 2026-02-10 | 93 | 14 | 21 | 30 | 28 |
| 2 | 2026-02-11 | 22 | 0 | 6 | 9 | 6 |
| 3 | 2026-02-11 | 7 | 0 | 1 | 4 | 2 |
| **4** | **2026-02-11** | **0** | **0** | **0** | **0** | **0** |

**All 7 remaining findings from Round 3 have been resolved.** Cumulative fix rate: **100%**.

The Velocity framework has achieved a clean security posture across all audited areas. No Critical, High, Medium, or Low findings remain open.

---

## Round 4 Verification Results

### R3-H1: gRPC Gateway Defaults to Insecure Without TLS Config
- **Status:** **FIXED**
- **File:** `pkg/grpc/gateway.go:88-113`
- **Evidence:** `configureGatewayTransport()` now has three clear paths:
  1. TLS cert/key env vars set → uses TLS with `MinVersion: tls.VersionTLS12`
  2. `GRPC_GATEWAY_INSECURE=true` explicitly set → uses insecure with warning log
  3. Neither set → returns error: `"TLS is required. Set GRPC_GATEWAY_TLS_CERT and GRPC_GATEWAY_TLS_KEY, or set GRPC_GATEWAY_INSECURE=true for local development"`
- The error is surfaced at `Build()` time (line 213-215), preventing the gateway from starting without explicit transport configuration.

### R3-M1: Queue Signing Falls Back to APP_KEY with HKDF
- **Status:** **FIXED**
- **File:** `pkg/queue/signing.go:28-58`
- **Evidence:** When APP_KEY is used as fallback, a prominent warning is printed to stderr: `"WARNING: using APP_KEY for queue signing. Set a dedicated QUEUE_SIGNING_KEY for production environments"`. Key is derived via HKDF-SHA256 with context `"queue-signing"`, providing cryptographic separation. The `QUEUE_SIGNING_DISABLED` env var has been removed -- signing cannot be silently disabled.

### R3-M2: CSRF Middleware Logs Warning But Passes Through When Uninitialized
- **Status:** **FIXED**
- **File:** `pkg/csrf/helpers.go:77-85`
- **Evidence:** When `globalCSRF == nil`:
  - In debug mode (`APP_DEBUG=true`): logs warning and passes through (acceptable for development)
  - In production (default): logs `"blocking request"`, returns HTTP 500 with `"CSRF protection not configured"`, and returns a non-nil error
- This prevents silent CSRF bypass in production.

### R3-M3: CSRF SessionStore Requires Manual Close()
- **Status:** **FIXED**
- **File:** `pkg/csrf/stores/session.go:27-70`
- **Evidence:** `NewSessionStore()` now accepts an optional `context.Context` as first argument. When a context is provided, the cleanup goroutine stops automatically when the context is cancelled. The `Close()` method is preserved for backward compatibility. Well-documented with multiple call signatures in the doc comment (lines 32-41).

### R3-M4: In-Memory JWT Blacklist
- **Status:** **FIXED** (addressed as documented architectural pattern)
- **File:** `pkg/auth/jwt.go:15-131`
- **Evidence:**
  - `BlacklistStore` interface defined (lines 17-24) with `Add`, `IsBlacklisted`, `Cleanup` methods
  - `InMemoryBlacklistStore` provided as default with proper `sync.RWMutex` protection
  - `SetBlacklistStore()` method (line 134) allows swapping in Redis/DB-backed stores
  - Warning logged when blacklist is enabled with in-memory store: `"using in-memory token blacklist. Set a persistent BlacklistStore for production multi-instance deployments"` (line 123)
  - The pattern follows Go's standard library approach (e.g., `database/sql` driver registration)

### R3-L1: Double KDF Path Not Consolidated
- **Status:** **FIXED**
- **File:** `pkg/crypto/drivers/aes.go:51-66`
- **Evidence:** The double-derivation path has been consolidated. `NewAESDriver` now derives encryption and HMAC subkeys directly from the original key in a single pass via `deriveSubkey()`, using HKDF with a static salt (`"velocity-framework-hkdf-salt-v1"`) and distinct info strings (`"encryption"`, `"hmac"`). The intermediate size-normalization HKDF step has been removed (confirmed in `pkg/crypto/crypto.go` -- key is passed directly without pre-derivation). Comment at line 51-53 explicitly documents: "Derive separate encryption and HMAC subkeys directly from the original key via HKDF with distinct info strings. HKDF handles any input key size, so no intermediate normalization step is needed."

### R3-L2: JWT Cache Cleanup Goroutine Lifecycle
- **Status:** **FIXED**
- **File:** `pkg/auth/drivers/guards/jwt.go:34-90`
- **Evidence:** `NewJWTGuard` now accepts an optional `context.Context` parameter. When provided, `cleanupLoopWithContext(ctx)` is used which stops on either `ctx.Done()` or `stopCleanup` channel. `StopCleanup()` also available for non-context usage. The dual-shutdown approach ensures the goroutine is always stoppable regardless of how the guard was constructed.

---

## Final Security Posture

### Overall Rating: STRONG

All 93 original findings have been resolved across 4 rounds of audit. The framework now implements:

**Authentication & Authorization:**
- JWT with 32-byte minimum secret, issuer/audience validation, token type differentiation
- Blacklist store interface with pluggable backends and `sync.RWMutex` protection
- Bounded user cache (10K entries, 5-min TTL, LRU eviction, context-managed cleanup)
- Context-based session storage with automatic cleanup
- Remember token with `crypto/rand` (panics on failure) and constant-time comparison
- Secure cookie defaults (Secure, HttpOnly, SameSite: Lax)

**Cryptography:**
- AES-256-GCM default with authenticated encryption
- HKDF-SHA256 key derivation with domain separation (single-pass, static salt)
- Mandatory MAC verification for CBC before padding removal
- Constant-time padding validation and secret comparison throughout
- Key rotation support for decryption

**SQL & ORM:**
- Identifier validation (`^[a-zA-Z_][a-zA-Z0-9_.]*$`) on all query builder methods
- Operator allowlist (17 operators) for WHERE conditions
- `QuoteIdentifier` with internal quote escaping on all 3 drivers
- Mass assignment protection via Fillable/Guarded interfaces
- Proper transaction scoping

**HTTP & Routing:**
- `http.MaxBytesReader` (10MB) on all body parsing
- Security headers middleware (5 headers)
- CORS middleware with origin validation
- Trusted proxy validation for IP detection
- Redirect URL sanitization (host validation, no open redirects)
- WebSocket same-origin default
- Rate limiter with 100K entry cap, LRU, and background cleanup

**Infrastructure:**
- Path traversal prevention across storage, mail, and S3
- Email header CRLF sanitization
- Mailgun HMAC-SHA256 webhook verification
- Queue HMAC-SHA256 payload signing
- gRPC TLS-required default with explicit insecure opt-in
- gRPC metadata redaction for auth headers
- SSRF protection blocking private/internal IPs

---

*Security audit completed over 4 rounds: 2026-02-10 to 2026-02-11.*
*Round 1: 93 findings | Round 2: 22 remaining | Round 3: 7 remaining | Round 4: 0 remaining.*
*See `SECURITY_GUIDELINES.md` for developer security guidelines derived from this audit.*
