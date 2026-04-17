# Changelog

All notable changes to Velocity will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
