#!/usr/bin/env bash
# check-lock-held-calls.sh - reports calls to user code (a logger, a func
# value, a value formatted through its own String or Error) made while
# framework code holds a lock or runs inside a sync.Once. User code can
# call back into the component that called it; under the component's lock
# that deadlocks for good.
#
# Delegates to the go/types walker in scripts/ci/check-lock-held-calls/,
# whose package comment documents the held regions, the flagged calls, the
# known limits and the excluded test-infrastructure directories.
#
# Suppression: a same-line `//lock-held-ok: <rationale>` comment, the
# rationale at least 5 characters.
#
# Prints "file:line: kind: call while holding lock" per offender, then on
# stderr how to fix each kind reported, and exits non-zero when there is
# any. Prints nothing on success. Stale markers never fail the check; run
# the checker with -all to list them.

set -euo pipefail

cd "$(dirname "$0")/../.."

exec go run ./scripts/ci/check-lock-held-calls ./...
