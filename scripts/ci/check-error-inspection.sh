#!/usr/bin/env bash
# check-error-inspection.sh - reports framework code that inspects an
# error it did not make outside internal/errchain: a call to errors.Is,
# errors.As or errors.Unwrap, or an Error, Unwrap, Is or As method call on
# an interface value. Those methods are user code when a handler, job,
# driver, store or callback returned the error: they may panic, and a
# chain may loop back on itself. internal/errchain holds the bounded,
# contained forms (Is, As, Unwrap, Text, Walk).
#
# It also reports a comparison with nil of a parameter of an exported
# function or method whose type is an interface the module declares: a
# typed nil passes it. internal/nilval.Is answers for both.
#
# Delegates to the go/types walker in scripts/ci/check-error-inspection/,
# whose package comment documents the flagged calls, the known limits and
# the excluded test-infrastructure directories.
#
# Suppression: a same-line `//error-inspection-ok: <rationale>` comment,
# the rationale at least 5 characters, saying why the inspected value runs
# no user method. A marker that suppresses nothing is stale and is
# reported too.
#
# Prints "file:line: kind: call" per offender, then on stderr how to fix
# each kind reported, and exits non-zero when there is any. Prints nothing
# on success.

set -euo pipefail

cd "$(dirname "$0")/../.."

exec go run ./scripts/ci/check-error-inspection ./...
