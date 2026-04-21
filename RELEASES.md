# Release Policy

This document describes how Velocity is versioned, released, and supported.

## Versioning Scheme

Velocity follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html), with one deliberate constraint:

**Velocity commits to staying on `v1` indefinitely.** There is no planned `v2` module path. All evolution happens within the `v1.x.y` line, using additive changes and deprecation — never breaking changes.

| Version part | Meaning | Example |
|---|---|---|
| **Patch** (`v1.4.1 → v1.4.2`) | Bug fixes, documentation, performance. No API changes. | Fix a panic in `router.Context`. |
| **Minor** (`v1.4 → v1.5`) | New features, additive API. No breaking changes. | Add `ctx.JSONPretty()`. Deprecate `ctx.Old()`. |
| **Major** (`v1 → v2`) | **Not planned.** Would require a new import path (`.../v2`). | — |

Consumers can always run `go get github.com/velocitykode/velocity@latest` without code changes.

## Backwards Compatibility Surface

The following are **covered** by the compatibility promise — breaking changes require a major version bump (which we don't plan to do):

- Exported types, functions, methods, and their signatures.
- Exported struct fields.
- Exported interface methods (we never add to interfaces that consumers are expected to implement).
- Documented runtime behavior.
- Environment variable names and their semantics.

The following are **not covered** — they may change in any minor release:

- Anything under `internal/`.
- Unexported identifiers.
- Exact error message strings (error *types* are covered; message text is not).
- Log output format.
- Test helper behavior marked `// Experimental:` in godoc.
- Benchmark numbers.
- Zero-value behavior of struct literals where the type documents "use the constructor."
- The exact shape of generic type inference in edge cases.

## Deprecation Policy

APIs are deprecated, never removed. A deprecated API:

- Carries a `// Deprecated:` godoc comment explaining the replacement.
- Is flagged by `staticcheck` and IDE tooling.
- Keeps working indefinitely — no removal within v1.

Example:

```go
// Deprecated: use ctx.JSON instead. Will not be removed.
func (c *Context) Json(code int, v any) error { ... }
```

If an API must be disabled for security reasons, that's a security release (see below), not a deprecation.

## Release Cadence

There is no fixed schedule.

- **Patch releases** ship when bug fixes accumulate (typically days to weeks).
- **Minor releases** ship when a coherent feature set is ready (typically weeks to months).
- **Emergency releases** ship immediately for critical fixes (see Emergency Releases).

## Branches

**`main` is the only long-lived branch.** All development happens on `main` via pull requests. Releases are git tags; there are no release branches under normal operation.

Short-lived branches are used for:

- **Pull requests** — feature and fix branches, deleted on merge.
- **Security advisory fixes** — private temporary forks via GitHub Security Advisories (see `SECURITY.md`).
- **Retroactive backport branches** — `1.N.x` branches cut on demand from an old tag when a high-severity CVE requires backporting to an older minor.

## Release Process

Releases are automated by `.github/workflows/ci.yml` on push to `main`:

1. Commits are analyzed for conventional-commit prefixes.
2. A new version is calculated:
   - `feat:` or `add` → minor bump
   - anything else → patch bump
3. A tag `vX.Y.Z` is created and pushed.
4. A GitHub Release is published with auto-generated notes.
5. `velocity-template`, `velocity-template-api`, and `velocity-installer` are updated to the new version.

**Manual release steps:**

```bash
# Update CHANGELOG.md with the release entry.
git commit -m "docs(changelog): prepare v1.5.0"
git push origin main
# CI tags and publishes automatically.
```

## Pre-Releases

Pre-release tags are supported for early testing without affecting stable consumers:

```bash
git tag v1.5.0-alpha.1    # early integration testing
git tag v1.5.0-beta.1     # feature-complete, seeking feedback
git tag v1.5.0-rc.1       # release candidate
git tag v1.5.0            # stable
```

Go's resolver skips pre-releases when a user runs `go get ...@latest`. Pre-releases are only selected when explicitly requested: `go get github.com/velocitykode/velocity@v1.5.0-rc.1`.

## Support Policy

- **Latest minor line** — all bug fixes and security fixes land here. Users are expected to track the latest minor.
- **Previous minor line** — receives security fixes for high-severity CVEs (CVSS ≥ 7.0) on a best-effort basis, via retroactive backport branches. No feature or bug-fix backports.
- **Older minor lines** — not supported. Users must upgrade to a supported line.

## Emergency Releases

### Critical bugs

Data corruption, crashes, or severe regressions in the current minor:

1. Fix is developed on `main` in a fast-tracked PR.
2. A patch release is tagged immediately on merge (e.g. `v1.5.0` → `v1.5.1`).
3. The fix is announced via the GitHub Release notes and CHANGELOG.

No backport to older minors unless explicitly requested by a user and agreed to by maintainers.

### Security vulnerabilities

See `SECURITY.md` for the full disclosure process. In summary:

1. Vulnerability is reported via GitHub's private vulnerability reporting.
2. Fix is developed in a temporary private fork created from the advisory.
3. Embargo is set (typically 7–14 days).
4. On disclosure day: merge to `main`, tag a patch release, publish the advisory.
5. For CVSS ≥ 7.0, a backport branch is cut from the previous minor's last tag, the fix is cherry-picked, and a patch release is tagged on that branch.

## Commit and PR Hygiene

Because security fixes may need to be cherry-picked months after they land, every commit on `main` must be **atomic**:

- One logical change per commit.
- Builds and tests pass at every commit.
- No mixing of fixes with refactors or formatting churn.

Pull requests are **squash-merged** by default. The squashed commit on `main` becomes one atomic, cherry-pickable unit regardless of how messy the PR branch was. PR authors don't need to rebase-clean their branches; the squash handles it.

## Upgrade Guidance for Consumers

```bash
# Safe upgrade — never crosses a major boundary.
go get github.com/velocitykode/velocity@latest
go mod tidy

# Upgrade dependencies transitively.
go get -u github.com/velocitykode/velocity
```

Breaking changes will never arrive via `go get` because there is no `v2` module path. `go.sum` is updated automatically; run `go mod tidy` to prune stale hashes.

## Related Documents

- [`CHANGELOG.md`](CHANGELOG.md) — per-release change log.
- [`SECURITY.md`](SECURITY.md) — vulnerability reporting and disclosure.
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — how to contribute code.
