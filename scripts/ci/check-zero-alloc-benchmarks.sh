#!/usr/bin/env bash
# check-zero-alloc-benchmarks.sh - runs the benchmarks that hold a function
# to no allocation per call and fails when any of them reports a nonzero
# allocs/op.
#
# Why benchmarks and not tests: a test cannot observe its own goroutine's
# allocations. testing.AllocsPerRun and the benchmark counters both read
# the process-wide malloc count, so a test comparing them fails whenever
# another test, timer or worker allocates meanwhile (scripts/ci/check-alloc-
# averages rejects such tests). A benchmark run on its own, with no tests
# (-run '^$') and many iterations, leaves nothing else allocating, and the
# per-op count divides a stray allocation away.
#
# The list is explicit: "<package> <top-level benchmark>", one per line.
# Every sub-benchmark of a listed benchmark is checked. A listed benchmark
# that does not run fails the check, so a rename cannot drop it silently.

set -euo pipefail

cd "$(dirname "$0")/../.."

BENCHMARKS=(
	". BenchmarkBindingCell_Forward"
	"./contract BenchmarkMarkerPredicates"
	"./contract BenchmarkPreferredMediaRange"
	"./contract BenchmarkStatusOf"
	"./events BenchmarkObserverFire_NoObserver"
	"./internal/nilval BenchmarkIs"
	"./orm/drivers BenchmarkNormalizeTimeArgs_NoTimeArgs"
	"./router BenchmarkClassifyError_Unmatched"
	"./router BenchmarkRequestAdmission"
)

fail=0
for entry in "${BENCHMARKS[@]}"; do
	pkg=${entry%% *}
	name=${entry#* }
	out=$(go test -run '^$' -bench "^${name}\$" -benchtime=100000x -benchmem "$pkg" 2>&1) || {
		echo "$pkg $name: go test failed:"
		echo "$out"
		fail=1
		continue
	}
	lines=$(printf '%s\n' "$out" | grep -E "^${name}(/|-|[[:space:]])" || true)
	if [ -z "$lines" ]; then
		echo "$pkg $name: did not run"
		fail=1
		continue
	fi
	bad=$(printf '%s\n' "$lines" | awk '{ for (i = 2; i <= NF; i++) if ($i == "allocs/op" && $(i-1) != "0") print }')
	if [ -n "$bad" ]; then
		echo "$pkg $name: allocates, want 0 allocs/op:"
		echo "$bad"
		fail=1
	fi
done
exit $fail
