# Changelog

All notable changes to Velocity will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security
- **auth/jwt**: enforce algorithm allowlist before signature verification; reject `none` unconditionally; add RS256/RS384/RS512 support (mismatched alg families now fail fast, closing the classic HMAC-verify-with-public-key confusion). `NewJWTManager` now returns `(*JWTManager, error)`.
- **auth/jwt**: add `JWTConfig.Validate()` (secret length ≥32 for HMAC, TTL > 0, allowlisted algorithm, RSA key pair for RSxxx).
- **auth/session**: replace `crypto/rand` panics in session ID and remember-token generation with propagated errors; mirror of jti fix.
- **auth/session**: remember-me cookies now store a SHA-256 hash server-side (raw token only lives in the encrypted cookie); cookie TTL is clamped to `min(session lifetime, 30d)`; refuses creation when `Lifetime == 0`.
- **csrf**: replace silent session-ID generation with a `csrf.session_fallback` event; token comparison stays on `subtle.ConstantTimeCompare`; rename `Config.HTTPOnly` → `Config.HttpOnly` to match `net/http.Cookie.HttpOnly`.
- **crypto/aes**: replace `fmt.Sprintf("base64:%s.%s", ...)` MAC concatenation with domain-separated HMAC writes (`"velocity\x00" || iv || ct`). **Wire-format break**: existing CBC payloads produced before this change cannot be decrypted. GCM is unaffected.
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
- GORM-based ORM with migrations
- Background job processing
- Real-time features with WebSockets
