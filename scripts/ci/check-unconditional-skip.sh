#!/usr/bin/env bash
# check-unconditional-skip.sh — reports t.Skip( calls that are NOT guarded
# by a preceding conditional in the same function. Phase 1.1 of the test
# audit removed unconditional skips because they erase coverage silently.
# This script enforces they don't come back.
#
# A skip is "guarded" if ANY `if ` appears in the 8 lines immediately
# above it. That window is intentionally tight — it catches the common
# patterns (env gate, runtime.GOOS, table-driven tt.skip field, err
# check) while rejecting the offender class: `t.Skip("TODO")` sitting at
# the top of a function with no preceding conditional.
#
# Prints "file:line:code" for each unguarded skip. Prints nothing on
# success. The CI job treats non-empty output as failure.

set -euo pipefail

cd "$(dirname "$0")/../.."

has_guard() {
  local file="$1" line="$2"
  local start=$(( line - 8 ))
  (( start < 1 )) && start=1
  sed -n "${start},${line}p" "$file" | grep -qE '\bif[[:space:]]'
}

offenders=""
while IFS= read -r hit; do
  file="${hit%%:*}"
  rest="${hit#*:}"
  lineno="${rest%%:*}"
  code="${rest#*:}"
  if ! has_guard "$file" "$lineno"; then
    offenders+="$file:$lineno:$code"$'\n'
  fi
done < <(grep -rn "t\.Skip(" --include="*_test.go" . 2>/dev/null)

if [[ -n "$offenders" ]]; then
  printf '%s' "$offenders"
fi
