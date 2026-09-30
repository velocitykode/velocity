#!/usr/bin/env bash
# check-released-ports.sh - reports a test that listens on port 0, reads
# the port, closes the listener and hands the port on: between the close
# and the rebind any process on the machine can take the port. Tests keep
# the listener and hand it to the server instead.
#
# Delegates to the go/ast walker in scripts/ci/check-released-ports/,
# whose package comment documents the rule and its scope. There is no
# suppression marker.
#
# Prints "file:line: message" per offender and exits non-zero when there
# is any. Prints nothing on success.

set -euo pipefail

cd "$(dirname "$0")/../.."

exec go run ./scripts/ci/check-released-ports .
