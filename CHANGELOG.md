# Changelog

All notable changes to Velocity will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- GORM-based ORM with migrations
- Background job processing
- Real-time features with WebSockets
