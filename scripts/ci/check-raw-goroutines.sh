#!/usr/bin/env bash
# check-raw-goroutines.sh - reports `go` statements in framework code
# that are not gated by an `async.Go` call or a per-callsite
# `//safe-goroutine: <rationale>` marker.
#
# Why a grep script and not a forbidigo rule: forbidigo's patterns
# match against AST expression strings (e.g. fmt.Println), not against
# `go` statements, so a regex like `^\s*go\s+` never fires. The
# CONTRIBUTING.md "Goroutines and panic recovery" invariant is
# therefore enforced here instead of by golangci-lint.
#
# A "raw goroutine" is any line that begins with optional whitespace
# followed by `go ` followed by a non-keyword token (i.e. a function
# or method call, anonymous or otherwise). The grep regex
# `^[[:space:]]*go[[:space:]]+[A-Za-z_*(&]` catches:
#
#   - `go func(...)` (anonymous goroutine literal)
#   - `go m.sweep(...)` / `go pump()` (method or package-function form)
#   - `go (*x).method(...)` (rare but legal)
#
# It does NOT match:
#
#   - identifiers that merely start with "go" (e.g. `goroutineCount`)
#     because the trailing `[[:space:]]+` requires whitespace after `go`
#   - `go:build` / `go:generate` directives (no space between `go` and `:`)
#   - lines beginning with `//` (the `^` anchor sits before the `//`)
#
# Suppression: a goroutine that genuinely needs bespoke recovery
# semantics (e.g. forwarding a recovered panic value through a result
# channel, closing a done-channel even on panic) carries a same-line
# `//safe-goroutine: <rationale>` marker. The rationale must be at
# least 5 characters; bare `//safe-goroutine:` does NOT suppress.
#
# Why a distinct marker (not //nolint:forbidigo): nolintlint in
# .golangci.yml is configured with allow-unused: false, so a
# //nolint:forbidigo directive on a `go func()` line would be flagged
# as unused (forbidigo never fires on goroutine statements). Using a
# distinct token keeps the two enforcers' suppression vocabularies
# from colliding.
#
# Exclusions:
#
#   - _test.go: descriptive goroutines in tests are fine
#   - <pkg>test/ contract-test runner packages (e.g. queuetest, cachetest,
#     schedulertest): test infrastructure imported by both framework and
#     third parties. Treated as test-only code, same policy as *_test.go.
#     Matched via --exclude-dir='*test'; does NOT match `testing/`
#     (ends in `ing`).
#   - async/: canonical panic-safe primitives, raw goroutines are the
#     implementation, not the policy violation
#   - router/event_dispatcher.go: safeInvokeListener wraps every
#     listener invocation, see the file's package comment
#
# Prints "file:line:code" for each offender. Prints nothing on success.
# The CI job treats any output as failure.

set -euo pipefail

cd "$(dirname "$0")/../.."

# Match raw goroutine statements and filter out:
#   - the path exclusions (router/event_dispatcher.go)
#   - properly-tagged sites: //safe-goroutine: followed by >=5 chars
# A line carrying only a bare `//safe-goroutine:` (no rationale, or
# rationale shorter than 5 chars) does NOT match the suppression
# pattern and therefore stays in the offender list.
OFFENDERS=$(
	grep -RnE '^[[:space:]]*go[[:space:]]+[A-Za-z_*(&]' \
		--include='*.go' \
		--exclude='*_test.go' \
		--exclude-dir=async \
		--exclude-dir='*test' \
		. 2>/dev/null \
	| grep -v 'router/event_dispatcher\.go' \
	| grep -vE '//safe-goroutine:[[:space:]]+.{5,}' \
	|| true
)

if [ -n "$OFFENDERS" ]; then
	echo "$OFFENDERS"
	exit 1
fi
