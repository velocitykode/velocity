#!/usr/bin/env bash
# check-recover-sites.sh - reports every recover() in the non-test code of
# the request's render and commit path that is not on the allowlist.
#
# Why: a commit listener (router.Context.BeforeCommit) that panics leaves
# the router's response writer as a *panicerr.Listener, and the router's
# boundary answers it and runs the listeners still pending only when that
# value reaches it. A recover() anywhere between the listener and the
# boundary that swallows the value, or http.ErrAbortHandler, ends that
# silently: the response goes out as an empty 200, or an abort net/http
# was meant to see is lost. So every recover() on this path is a decision,
# and this check makes each one be written down.
#
# The allowlist is in place: a recover() line carries a same-line
# `//recover-ok: <reason>` comment, the reason at least 5 characters,
# saying either how the site passes a listener's panic and
# http.ErrAbortHandler on, or why neither can reach it (it wraps one call
# into a logger, a reporter or a store, which writes no response).
#
# What it does NOT check: that the site does what its reason says. Whether
# a recovering frame re-panics the two values is a property of the code
# after the recover(), across branches and helpers, and a grep cannot
# decide it. The check holds the allowlist only; the reason is for the
# reviewer of the line, and the behaviour is held by the tests of the
# sites that pass the values on (router boundary, problem stage and last
# resort).
#
# Scope: router, problem (with problem/routerbridge), bond, view, contract
# and auth/drivers/schemes, recursively. Excluded: *_test.go and <pkg>test
# directories, as in check-raw-goroutines.sh. Comment lines are not sites.
#
# A marker on a line with no recover() is stale and is reported too.
#
# Prints "file:line:code" for each offender. Prints nothing on success.
# The CI job treats any output as failure.

set -euo pipefail

cd "$(dirname "$0")/../.."

DIRS=(router problem bond view contract auth/drivers/schemes)

scan() {
	grep -RnE "$1" \
		--include='*.go' \
		--exclude='*_test.go' \
		--exclude-dir='*test' \
		"${DIRS[@]}" 2>/dev/null \
	|| true
}

# recover() in code: not a line that is only a comment.
SITES=$(scan '(^|[^A-Za-z0-9_.])recover\(\)' | grep -vE '^[^:]+:[0-9]+:[[:space:]]*//' || true)

UNLISTED=$(printf '%s\n' "$SITES" | grep -vE '//recover-ok:[[:space:]]+.{5,}' | grep -v '^$' || true)

# A marker whose line has no recover() in its code part.
STALE=$(
	scan '//recover-ok:' \
	| grep -vE '^[^:]+:[0-9]+:[^/]*(/[^/][^/]*)*(^|[^A-Za-z0-9_.])recover\(\).*//recover-ok:' \
	|| true
)

if [ -n "$UNLISTED$STALE" ]; then
	[ -n "$UNLISTED" ] && echo "$UNLISTED"
	[ -n "$STALE" ] && echo "$STALE"
	exit 1
fi
