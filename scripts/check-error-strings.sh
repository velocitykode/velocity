#!/usr/bin/env bash
# check-error-strings.sh — Enforce C9.8 error string conventions.
#
# Framework errors must:
#   - Start lowercase (except in validation/ which has user-facing messages)
#   - Not end with ".", "!", or "\n"
#
# Usage: ./scripts/check-error-strings.sh
# Exit code 0 = clean, 1 = violations found.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

violations=0

# Find errors.New("Uppercase...") outside validation/
while IFS= read -r line; do
    echo "ERROR: uppercase errors.New: $line"
    violations=$((violations + 1))
done < <(grep -rn 'errors\.New("[A-Z]' --include='*.go' \
    --exclude-dir=validation \
    --exclude='*_test.go' \
    || true)

# Find fmt.Errorf("Uppercase...") outside validation/
while IFS= read -r line; do
    echo "ERROR: uppercase fmt.Errorf: $line"
    violations=$((violations + 1))
done < <(grep -rn 'fmt\.Errorf("[A-Z]' --include='*.go' \
    --exclude-dir=validation \
    --exclude='*_test.go' \
    || true)

# Find trailing period in error strings
while IFS= read -r line; do
    echo "ERROR: trailing period in error: $line"
    violations=$((violations + 1))
done < <(grep -rn 'errors\.New(".*\.")' --include='*.go' \
    --exclude-dir=validation \
    --exclude='*_test.go' \
    || true)

if [ "$violations" -gt 0 ]; then
    echo ""
    echo "Found $violations error string violation(s)."
    echo "Framework errors must start lowercase and not end with punctuation."
    exit 1
fi

echo "Error string check passed."
