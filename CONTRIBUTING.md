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

## Questions?

- **GitHub Discussions**: For questions and general discussion
- **Issues**: For bug reports and feature requests
- **Pull Requests**: For code contributions

Thank you for contributing to Velocity! 🚀
