# TESTING.md

This document is the canonical reference for how tests in this repository are
structured and what they may (and may not) do. The policy exists because
earlier snapshots had mixed-quality test code — unconditional skips, naked
sleeps, shallow assertions — and the audit that produced this document
raised the floor.

Rules here are enforced by CI. See `.github/workflows/ci.yml` for the exact
jobs (`skip-lint`, `coverage-floors`, `fuzz-smoke`, goleak TestMain per
package).

## Sleep policy (tests only)

Full rule set lives in `testing/sync.go` (read the package doc). Summary:

Adding a `time.Sleep` to a `*_test.go` file requires a comment that names
its category. Acceptable categories:

| Category          | When it applies                                                                                                                                |
|-------------------|------------------------------------------------------------------------------------------------------------------------------------------------|
| TTL MODELING      | Wall-clock-based feature under test (cache expiration, rate-limit window, delayed-job visibility). Real duration must elapse for the assertion. |
| ORCHESTRATION     | Sleep sits inside a closure that the helper under test runs — fake "work" for `async.All`, `async.Race`, `queue.Job.Handle`, scheduler callback. |
| STABILITY WINDOW  | Negative assertion ("this should NOT have fired yet"). A small sleep gives any racing goroutine a chance to violate the invariant before sampling. |
| FIXTURE           | Benchmark timings, deliberate `UpdatedAt` difference, simulated slow HTTP handler feeding duration histograms.                                 |

Not acceptable: "wait for goroutine to finish", "let registration complete",
"give the dispatcher a moment". Use
[`testsync.Eventually`](testing/sync.go) or a channel/`sync.WaitGroup`
instead.

If a reviewer can't tell which category from the surrounding comment, the
sleep is wait-and-hope and should be replaced.

## Skips

Unconditional `t.Skip(...)` is banned. CI's `skip-lint` job runs
[`scripts/ci/check-unconditional-skip`](scripts/ci/check-unconditional-skip/main.go) —
a `go/ast` walker that flags every skip not guarded by a conditional in the
enclosing function. If a test needs to be skipped under a real condition
(env var missing, wrong platform, short mode), gate it:

```go
if os.Getenv("INTEGRATION") == "" {
    t.Skip("integration tests require INTEGRATION=1")
}
```

If a test is broken, delete it or mark it `t.Fatal` — don't silently
skip.

## Integration tests

Integration tests live behind `//go:build integration` and fail loud if
their dependencies are missing. The Makefile's `test-integration` target
is the supported entrypoint.

Pattern:

```go
//go:build integration

package foo

var requiredEnv = []string{"POSTGRES_URL", "REDIS_HOST"}

func TestMain(m *testing.M) {
    var missing []string
    for _, name := range requiredEnv {
        if os.Getenv(name) == "" {
            missing = append(missing, name)
        }
    }
    if len(missing) > 0 {
        fmt.Fprintf(os.Stderr,
            "integration tests require env vars (missing: %s) — use `make test-integration`\n",
            strings.Join(missing, ", "))
        os.Exit(1)
    }
    os.Exit(m.Run())
}
```

The fail-fast exit is deliberate: `t.Skip` would silently hide a sidecar
container failing to start. The Makefile does the gating — it checks env
vars before invoking `go test` and prints the docker setup commands if any
are missing.

## Fuzz targets

Fuzz targets ship with a seed corpus — real adversarial inputs (`alg=none`,
CRLF, traversal, unicode junk), not random bytes. The contract the target
tests must be explicit in the target's header comment. "Didn't panic" is a
floor, not a contract.

CI runs each fuzz target for 30 seconds per PR (`fuzz-smoke` job). That
will not find every new crash against a new code path; it's a regression
floor, not a full campaign. Run longer locally if you're touching a fuzz
target:

```bash
go test -run ^$ -fuzz FuzzSanitizeRedirect -fuzztime=5m ./router/
```

Currently instrumented: JWT validation, redirect sanitization, rule
parsing, flash cookie decoding, CSRF decoding. Add a target whenever you
write a parser / decoder / sanitizer that sees untrusted bytes.

## Goroutine leaks (goleak)

Packages that own background goroutines run
[`goleak.VerifyTestMain`](https://pkg.go.dev/go.uber.org/goleak) — it asserts
no goroutines survive past the final test. Currently instrumented:
`async`, `bus`, `scheduler`, `grpc`.

The per-package `main_test.go` looks like:

```go
func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m,
        goleak.IgnoreTopFunction("testing.tRunner"),
        goleak.IgnoreTopFunction("testing.(*T).Run"),
    )
}
```

Do not use `goleak.IgnoreCurrent()` to paper over a real leak. The
intentional exception (`auth/shutdown_goleak_test.go`) names the concrete
function it's ignoring and explains why.

Queue is not yet instrumented — ~28 test sites create `MemoryDriver`
without `Close()`. Tracked as a follow-up.

## Coverage floors

The `coverage-floors` CI job enforces per-package lower bounds (see
`scripts/ci/check-coverage-floors.sh`). Thresholds sit intentionally below
the current measurement — they guard the floor, not the ceiling. If you
drop below, either add tests or argue for the floor to move in the PR
description.

Current floors:

| Package    | Floor | Why                                                                               |
|------------|-------|-----------------------------------------------------------------------------------|
| auth       | 73%   | Auth-critical — unit + integration + fuzz all pay rent here.                      |
| validation | 66%   | Input sanitization; fuzz-exercised; any drop usually signals a new rule without tests. |
| bus        | 90%   | Event bus is small, fully exercisable; no reason to drop.                         |

## Race detector

Every `go test` invocation in CI passes `-race`. Local convenience target:
`make test` enables it by default. If a test is skipped under `-race`, it
must be gated on `testing.Short()` or `os.Getenv` — not a naked skip.

## When in doubt

Lean on reviewers during PR. The golden rule: a future reader should be
able to tell from the test alone what the code's contract is. If they
can't, the assertion is shallow, the sleep is a wait-and-hope, or the
setup is doing too much.
