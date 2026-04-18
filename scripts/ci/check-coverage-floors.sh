#!/usr/bin/env bash
# check-coverage-floors.sh — enforces per-package coverage floors.
#
# Each floor is set JUST BELOW the current measured coverage so the gate
# catches regressions without false-positiving normal churn. Raising a
# floor is a separate PR with intent; dropping one requires explicit
# justification in the commit message.
#
# Current measurements (2026-04-18):
#   auth       74.3%
#   validation 67.1%
#   bus        95.5%
#
# Floors are 1–5 points below current to leave room for trivial
# refactors. A bigger drop is the signal we want to catch.

set -euo pipefail

cd "$(dirname "$0")/../.."

# pkg:floor — if you add a new package here, document why in the commit.
FLOORS=(
  "auth 73.0"
  "validation 66.0"
  "bus 90.0"
)

failed=0
for entry in "${FLOORS[@]}"; do
  pkg="${entry% *}"
  floor="${entry##* }"
  # go test -cover emits "coverage: 74.3% of statements"
  out=$(go test -cover -short -timeout 120s "./$pkg/" 2>&1 | tail -5)
  pct=$(echo "$out" | grep -oE 'coverage: [0-9]+\.[0-9]+%' | grep -oE '[0-9]+\.[0-9]+' | head -1)
  if [[ -z "$pct" ]]; then
    echo "::error::could not read coverage for ./$pkg/ — test output:"
    echo "$out"
    failed=1
    continue
  fi
  # awk float compare — bc is less portable across minimal CI images.
  if awk -v a="$pct" -v b="$floor" 'BEGIN{exit !(a < b)}'; then
    echo "::error::./$pkg/ coverage $pct% below floor $floor%"
    failed=1
  else
    echo "OK  ./$pkg/ $pct% (floor $floor%)"
  fi
done

exit $failed
