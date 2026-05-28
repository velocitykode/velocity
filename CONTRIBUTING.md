# Contributing to Velocity

Thank you for your interest in contributing to Velocity! We welcome contributions from the community and are grateful for your support.

## Code of Conduct

We are committed to providing a welcoming and inclusive experience for everyone. We expect all contributors to:

- Be respectful and considerate in communication
- Welcome newcomers and help them get started
- Accept constructive criticism gracefully
- Focus on what is best for the community
- Show empathy towards other community members

## How to Contribute

### Reporting Bugs

Before creating a bug report, please check existing issues to avoid duplicates.

When reporting a bug, include:

- **Clear title**: Summarize the issue in one line
- **Description**: Detailed explanation of the problem
- **Steps to reproduce**: Numbered steps to recreate the issue
- **Expected behavior**: What you expected to happen
- **Actual behavior**: What actually happened
- **Environment**: Go version, OS, Velocity version
- **Code sample**: Minimal reproducible example (if applicable)
- **Error messages**: Full error output or stack traces

### Suggesting Features

We love feature suggestions! Please create an issue with:

- **Use case**: Describe the problem you're trying to solve
- **Proposed solution**: Your idea for solving it
- **Alternatives considered**: Other approaches you've thought about
- **Additional context**: Any relevant examples or references

### Pull Request Process

1. **Fork the repository** and create a new branch from `main`:
   ```bash
   git checkout -b feature/my-new-feature
   # or
   git checkout -b fix/issue-description
   ```

2. **Make your changes** following our coding standards (see below)

3. **Write or update tests** for your changes:
   - Add test cases for new functionality
   - Ensure existing tests still pass
   - Maintain >90% code coverage

4. **Run the test suite**:
   ```bash
   # Run all tests
   go test ./...

   # Run with race detection
   go test ./... -race

   # Check coverage
   go test ./... -cover
   ```

5. **Run linters**:
   ```bash
   # Format code
   gofmt -w .

   # Run golangci-lint (if available)
   golangci-lint run
   ```

6. **Commit your changes** with clear, descriptive commit messages:
   ```bash
   git commit -m "feat: Add new cache driver for Memcached"
   # or
   git commit -m "fix: Resolve race condition in queue worker"
   ```

   Commit message format:
   - `feat:` New feature
   - `fix:` Bug fix
   - `docs:` Documentation changes
   - `test:` Test additions or modifications
   - `refactor:` Code refactoring
   - `perf:` Performance improvements
   - `chore:` Maintenance tasks

7. **Push to your fork** and create a pull request:
   ```bash
   git push origin feature/my-new-feature
   ```

8. **Describe your PR** with:
   - Summary of changes
   - Related issue numbers (e.g., "Fixes #123")
   - Testing performed
   - Breaking changes (if any)

### Pull Request Review

- Maintainers will review your PR and may request changes
- Address feedback by pushing additional commits
- Once approved, a maintainer will merge your PR
- Your contribution will be included in the next release!

## Development Setup

### Prerequisites

- Go 1.21 or higher
- Git
- Make (optional, for convenience commands)

### Getting Started

1. **Clone the repository**:
   ```bash
   git clone https://github.com/velocitykode/velocity.git
   cd velocity
   ```

2. **Install dependencies**:
   ```bash
   go mod download
   ```

3. **Run tests**:
   ```bash
   go test ./...
   ```

4. **Run a specific package's tests**:
   ```bash
   go test ./pkg/log/...
   go test ./pkg/cache/...
   ```

5. **Run with verbose output**:
   ```bash
   go test ./pkg/log -v
   ```

### Project Structure

```
velocity/
├── pkg/                    # Core packages
│   ├── auth/              # Authentication
│   ├── cache/             # Caching system
│   ├── log/               # Logging
│   ├── orm/               # ORM
│   └── ...                # Other packages
├── openspec/              # OpenSpec change proposals
├── .specify/              # Internal planning (not in public repo)
└── README.md
```

### Local Development Workflow

1. Create a `.env` file in the root (for testing):
   ```env
   LOG_DRIVER=console
   CACHE_DRIVER=memory
   QUEUE_DRIVER=memory
   ```

2. Make your changes to the relevant package

3. Write tests in `*_test.go` files

4. Run tests frequently:
   ```bash
   # Test specific package
   go test ./pkg/log -v

   # Test with coverage
   go test ./pkg/log -cover
   ```

5. Check for race conditions:
   ```bash
   go test ./pkg/cache -race
   ```

## Testing Requirements

All contributions must meet these testing standards:

### Coverage
- **Target**: >90% code coverage for new packages
- **Minimum**: >80% for existing packages
- **Check coverage**: `go test ./pkg/yourpackage -cover`

### Test Types

1. **Unit Tests**: Test individual functions and methods
2. **Integration Tests**: Test package interactions
3. **Concurrent Tests**: Test thread safety with goroutines
4. **Benchmark Tests**: For performance-critical code

### Test Naming

```go
func TestFunctionName(t *testing.T) { }                    // Basic test
func TestFunctionName_EdgeCase(t *testing.T) { }           // Specific scenario
func TestFunctionName_Concurrent(t *testing.T) { }         // Concurrency test
func BenchmarkFunctionName(b *testing.B) { }               // Benchmark
```

### Test Requirements

- All tests must pass: `go test ./...`
- No race conditions: `go test ./... -race`
- Tests should be deterministic (no flaky tests)
- Use table-driven tests for multiple scenarios
- Clean up resources (files, connections) in tests

### Writing Good Tests

```go
func TestCacheSet(t *testing.T) {
    tests := []struct {
        name    string
        key     string
        value   interface{}
        wantErr bool
    }{
        {"valid string", "key1", "value1", false},
        {"valid int", "key2", 42, false},
        {"empty key", "", "value", true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            cache := NewMemoryCache()
            err := cache.Set(tt.key, tt.value, 60*time.Second)

            if (err != nil) != tt.wantErr {
                t.Errorf("Set() error = %v, wantErr %v", err, tt.wantErr)
            }
        })
    }
}
```

## Coding Standards

### General Guidelines

- Follow standard Go idioms and conventions
- Keep functions small and focused
- Use meaningful variable and function names
- Write self-documenting code
- Add comments for complex logic

### Documentation

- **All exported functions** must have godoc comments
- **Package-level documentation** required for each package
- **Examples** encouraged for complex features

Example:

```go
// Cache provides a unified interface for caching operations.
// It supports multiple drivers (memory, file, redis) configured
// via the CACHE_DRIVER environment variable.
package cache

// Get retrieves a value from the cache by key.
// It returns an error if the key doesn't exist or has expired.
//
// Example:
//   value, err := cache.Get("user:123")
//   if err != nil {
//       log.Error("Cache miss", err)
//   }
func Get(key string) (interface{}, error) {
    // Implementation
}
```

### Code Style

- Run `gofmt` before committing (formats code automatically)
- Use `golangci-lint` for additional checks
- Maximum line length: 120 characters (guideline, not strict)
- Use early returns to reduce nesting

### Error Handling

- Return errors, don't panic (except for truly unrecoverable errors)
- Wrap errors with context: `fmt.Errorf("failed to connect: %w", err)`
- Use custom error types for public APIs when appropriate

### Performance

- For performance-critical code, include benchmarks
- Profile before optimizing (`go test -bench=. -cpuprofile=cpu.prof`)
- Document performance characteristics in godoc

### Goroutines and panic recovery

Every goroutine spawned from library code MUST either:

1. Go through `async.Go` (from `github.com/velocitykode/velocity/async`),
   which installs panic recovery and a default logger — the preferred
   option when no custom recovery logic is needed; or
2. Have an explicit `defer recover()` that both **logs** the panic via
   the manager/component's logger **and** dispatches the relevant
   `OperationFailed` event (e.g. `cache.CacheOperationFailed`,
   `mail.MailFailed`, `notification.NotificationFailed`) so operators
   observe it through the same channel as regular runtime failures.

An unrecovered goroutine panic terminates the entire process. This is
non-negotiable for long-running services.

**Lint enforcement.** The `goroutine-lint` CI job runs
`scripts/ci/check-raw-goroutines.sh`, which greps for raw `go `
statements outside `async.Go`. Both the anonymous form
(`go func(...)`) and the method/function form (`go m.sweep(...)`)
are caught. Test files (`_test.go`) and the `async/` /
`router/event_dispatcher.go` packages (which implement the
panic-safe primitives themselves) are exempt. To add a legitimate
exception on a specific line, append a same-line
`//safe-goroutine: <one-line rationale>` directive. The rationale
must be at least 5 characters; a bare `//safe-goroutine:` does NOT
suppress.

The `//safe-goroutine:` marker is intentionally **distinct** from
`//nolint:forbidigo`. nolintlint in `.golangci.yml` is configured
with `allow-unused: false`, which means `//nolint:forbidigo` is
only valid on lines forbidigo actually fires on (in practice,
`io.ReadAll` callsites). Goroutine sites need a separate token so
the two enforcers' suppression vocabularies do not collide.

`forbidigo` itself cannot enforce this rule directly because its
patterns match against AST expression strings (e.g. `fmt.Println`),
and a `go` statement is a `GoStmt`, not an expression. The CI
script is the source of truth; `.golangci.yml` carries an
explanatory note pointing back here.

Example of the acceptable patterns:

```go
// Preferred: async.Go handles recovery and logging.
async.Go(func() {
    if err := server.Run(ctx); err != nil {
        log.Error("server exited", "error", err)
    }
})

// When you need to dispatch a typed failure event on panic:
go func(n interface{}) { //safe-goroutine: dispatches NotificationFailed on panic
    defer func() {
        if r := recover(); r != nil {
            err := panicerr.FromRecovered(r)
            m.dispatchEvent(buildNotificationFailed(ctx, n, notification, "", err))
            errChan <- fmt.Errorf("velocity/notification: send many panic: %w", err)
        }
    }()
    // ... work ...
}(notifiable)
```

### Cross-cutting invariants

The four rules below are enforced framework-wide and verified during code
review. Each has a one-stop home so reviewers and contributors do not
have to re-derive the policy per package.

1. **File modes.** The `console/file_mode.go` constants
   (`defaultFileMode` `0644`, `defaultDirMode` `0755`, `secretFileMode`
   `0600`, `secretDirMode` `0700`) are the canonical values for every
   file the framework writes. Console generators must use them
   verbatim. Other packages that touch the filesystem keep their own
   package-local named constants (e.g. `cacheFileMode`,
   `storageFileMode`, `jobOutputFileMode`, `sqliteDirMode`) with a
   one-paragraph doc comment justifying the tier. Raw `0644` / `0755`
   / `0o600` / `0o700` literals in non-test code are not allowed;
   replace with a named constant or add a one-line comment if a literal
   is genuinely justified.

2. **`io.ReadAll` is forbidden.** Every call site must wrap its source
   in `http.MaxBytesReader` (HTTP request bodies) or `io.LimitReader`
   (any other reader, with a sensible package-local cap such as
   `postmarkErrorPreview` / `ssrResponseCap` / `svgScanCap`) and tag
   the call with `//nolint:forbidigo // bounded by <wrapper>`. The
   `forbidigo` rule in `.golangci.yml` enforces the lint via the
   `golangci-lint` CI job in `.github/workflows/ci.yml`. nolintlint
   (also enabled there) rejects bare `//nolint:`, requires a
   rationale comment after the linter name, and refuses
   `//nolint:forbidigo` directives that do not actually suppress a
   forbidigo finding (`allow-unused: false`).

3. **Long-lived goroutines use `async.Go`.** Worker pools, scheduler
   ticks, websocket pumps, cache sweepers, log rotators, and any
   other goroutine that outlives a single request handler MUST go
   through `async.Go` so panics are recovered through the framework
   logger. Short-lived per-request goroutines may use a raw `go`
   when they need bespoke recovery semantics (e.g. forwarding a
   recovered panic value through a result channel), in which case
   add a same-line `//safe-goroutine: <rationale>` marker (rationale
   text must be at least 5 characters) plus a `// Not async.Go:`
   comment explaining why. Enforced by the `goroutine-lint` CI job
   via `scripts/ci/check-raw-goroutines.sh`. The `//safe-goroutine:`
   token is intentionally distinct from `//nolint:forbidigo` so the
   two enforcers' suppression vocabularies do not collide (see
   "Goroutines and panic recovery" above for the full story).

4. **Client IP via `internal/clientip`.** Rate limiting, login
   throttling, audit logging, abuse heuristics, and the public
   `(*router.Context).IP()` accessor MUST resolve the originating
   client through `clientip.Extract` / `clientip.ExtractString` so
   the framework speaks one trust-proxy policy. Reading raw
   `r.RemoteAddr` is acceptable only for debug logging, raw
   instrumentation event fields, or trust-proxy checks where the
   immediate peer (not the original client) is the question; tag
   such sites with a one-line comment.

## Questions?

- **GitHub Discussions**: For questions and general discussion
- **Issues**: For bug reports and feature requests
- **Pull Requests**: For code contributions

Thank you for contributing to Velocity! 🚀
