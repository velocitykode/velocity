#!/usr/bin/env bash
# check-unconditional-skip.sh — reports t.Skip( calls that are not
# guarded by a preceding conditional in the enclosing function. Phase
# 1.1 of the test audit removed unconditional skips because they erase
# coverage silently. This script enforces they don't come back.
#
# Delegates to a go/ast walker in scripts/ci/check-unconditional-skip/
# which understands function boundaries. Replaces the earlier 8-line
# sed-window that misfired under refactor (guard ifs further than 8
# lines away read as unguarded; unrelated ifs within 8 lines read as
# guarded, false-negative).
#
# Prints "file:line:col:code" for each unguarded skip. Prints nothing on
# success. The CI job treats any output as failure.

set -euo pipefail

cd "$(dirname "$0")/../.."

exec go run ./scripts/ci/check-unconditional-skip .
