#!/usr/bin/env bash
# check-alloc-averages.sh - reports testing.AllocsPerRun used outside a
# benchmark. AllocsPerRun reads the process-wide malloc count, so a test
# that compares it fails whenever anything else in the process allocates:
# a test proves its contract by a direct observation instead, and an
# allocation budget lives in a benchmark (see check-zero-alloc-benchmarks).
#
# Delegates to the go/ast walker in scripts/ci/check-alloc-averages/, which
# resolves the file's import name for testing (alias and dot import
# included) and knows function boundaries. Prints "file:line:col: source"
# per finding and exits nonzero on any; prints nothing on success.

set -euo pipefail

cd "$(dirname "$0")/../.."

exec go run ./scripts/ci/check-alloc-averages .
