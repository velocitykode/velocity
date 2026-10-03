#!/usr/bin/env bash
# check-lock-held-calls.sh - reports calls to user code (a logger, a func
# value, a value formatted through its own String or Error, a value a json,
# gob, xml or io call calls into, a caller's context and the code it is
# handed to) made while framework code holds a lock or
# runs inside a sync.Once. User code can call back into the component that
# called it; under the component's lock that deadlocks for good. Also (rmw)
# a session store read followed by a blind write of the same key in csrf
# and auth, which two requests of one session race (last write wins).
#
# Delegates to the go/types walker in scripts/ci/check-lock-held-calls/,
# whose package comment documents the held regions (lock-returning helpers
# and deferred statements in execution order included), the flagged calls,
# the known limits and the excluded test-infrastructure directories.
#
# Suppression: a same-line `//lock-held-ok: <rationale>` comment, the
# rationale at least 5 characters, saying why the held call is safe or that
# its fix is filed; for rmw, `//store-rmw-ok: <rationale>` on the write
# or the delete.
#
# Prints "file:line: kind: call while holding lock" per offender, then on
# stderr how to fix each kind reported, and exits non-zero when there is
# any. A marker that suppresses nothing is stale and is reported too.
# Prints nothing on success.

set -euo pipefail

cd "$(dirname "$0")/../.."

exec go run ./scripts/ci/check-lock-held-calls ./...
