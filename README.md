# Velocity

**The full-stack Go framework. One binary. Zero compromise.**

Full-stack frameworks give you everything — but re-bootstrap every request, require runtimes in production, and fake concurrency with child processes. Go gives you speed — but leaves you wiring auth, ORM, queues, mail, and caching from scratch.

Velocity eliminates the trade-off.

```bash
go build -o myapp
scp myapp server:/usr/local/bin/
```

Your application *is* the server.

## Requirements

- Go 1.26 or higher

## Quick Start

```bash
# Homebrew (macOS)
brew tap velocitykode/tap
brew install velocity

# Or with Go
go install github.com/velocitykode/velocity-cli@latest
```

```bash
velocity new myapp
cd myapp
velocity serve
```

Or add to an existing project:

```bash
go get github.com/velocitykode/velocity
```

```go
func main() {
    v, _ := velocity.New()

    v.Router.Get("/", func(c *router.Context) error {
        return c.JSON(200, map[string]string{"message": "Hello"})
    })

    v.Serve()
}
```

For larger apps, use the declarative bootstrap API:

```go
func main() {
    v, _ := velocity.New()

    v.Providers(app.Configure).
        Middleware(app.Middleware).
        Routes(routes.Register).
        Events(app.Events(v.Log)).
        Schedule(schedule.Configure).
        Exceptions(exceptions.Configure).
        Serve()
}
```

## Why Velocity

### Boot Once, Serve Forever

15 services initialize in dependency order at startup. Every request hits a fully warmed app with zero bootstrap overhead.

```
Logger → Crypto → DB → Auth → Cache → CSRF → View → Events
→ Queue → Storage → Scheduler → Mail → Router → Exceptions → Validator
```

No per-request config loading. No per-request DI resolution. 100% of CPU goes to your handler.

### Native Concurrency

Goroutines at ~2KB each, scheduled across all cores. No serialization. No process spawning.

```go
results := async.All(
    func() int { return db.Table("users").Count() },
    func() int { return db.Table("orders").Count() },
    func() int { return db.Table("products").Count() },
)
```

Full toolkit: `Run`, `All`, `AllWithError`, `Race`, `RaceWithTimeout`, `ForEach`, `Map`, `Go` — all with built-in panic recovery.

### Type-Safe ORM

Queries checked at compile time. If the types don't match, it won't build.

```go
type User struct {
    orm.Model[User]
    Name  string
    Email string
}

users, err := User{}.Where("active = ?", true).
    OrderBy("created_at", "DESC").
    Limit(10).
    Get() // returns []User, not []interface{}
```

Four model types: `Model[T]`, `UUIDModel[T]`, `SoftDeleteModel[T]`, `SoftDeleteUUIDModel[T]`. Chainable query builder with eager loading, pagination, locking, and raw queries.

### gRPC + REST in One Binary

First-class gRPC server and HTTP/REST gateway. No sidecar. No protocol translation layer.

```go
app.Router.Post("/api/users", createUser)
grpcServer.RegisterService(&userService{})
```

### Pluggable Drivers

Every subsystem swaps via config, not code.

| | Drivers |
|---|---|
| Database | PostgreSQL, MySQL, SQLite |
| Cache | Memory, File, Redis, Database |
| Queue | Memory, Redis, Database |
| Storage | Local, S3, Memory |
| Mail | Postmark, Mailgun, Log |
| Auth | Session, JWT |
| Broadcasting | WebSocket |

SQLite in dev, PostgreSQL in prod. One env var change.

### Secure by Default

Security headers, HTTPS redirect, CORS, CSRF — all built in with safe defaults and configurable via functional options.

```go
router.Use(SecurityHeaders(
    WithCSP("default-src 'self'; script-src 'self' cdn.example.com"),
))
router.Use(HTTPSRedirect(
    WithExcludePaths("/health"),
))
```

CORS rejects all cross-origin requests by default — you opt in to what you allow.

### No Magic

No static proxies. No runtime reflection for DI. No implicit model fetching. No hidden interceptors. What you write is what runs.

```go
app.Router.Get("/users/{id}", func(c *router.Context) error {
    user, err := User{}.Find(c.Param("id"))
    return c.JSON(200, user)
})
```

### And Everything Else

- **Security middleware** — configurable security headers, HTTPS redirect, CORS, CSRF, rate limiting
- **WebSocket server** — client management, rooms, broadcasting, built in
- **Queue jobs** — zero-copy in-memory, typed deserialization for Redis/DB, job signing, automatic retries with backoff
- **Event system** — dispatcher with sync/async listeners, wildcard matching, queued listeners
- **Task scheduler** — cron-based scheduling with callbacks
- **Notifications** — multi-channel (mail, database, broadcast, Slack) with routing
- **HTTP client** — instrumented with event dispatching for APM monitoring
- **Graceful shutdown** — reverse-order teardown, 30-second grace period, nothing drops
- **AES-256-GCM encryption** — authenticated encryption, key rotation
- **CSRF protection** — session and cookie store drivers
- **Validation** — rule engine with database-aware rules (`unique`, `exists`)
- **Storage** — local filesystem, S3, memory with a unified API
- **Distributed tracing** — trace context propagation for APM
- **Collections** — generic, type-safe slice operations with fluent chaining

## The Stack

```
┌─────────────────────────────────────────────────────────┐
│                   Single Binary Output                   │
├─────────────┬─────────────┬──────────────┬──────────────┤
│  Routing    │    ORM      │    Auth      │  Real-time   │
│  Radix tree │  Generics   │ Session+JWT  │  WS + gRPC   │
├─────────────┼─────────────┼──────────────┼──────────────┤
│  Cache      │   Queue     │  Storage     │    Mail      │
├─────────────┼─────────────┼──────────────┼──────────────┤
│  Events     │  Scheduler  │  Validation  │    Async     │
├─────────────┼─────────────┼──────────────┼──────────────┤
│  Collections│  Strings    │  Pipeline    │    Crypto    │
├─────────────┴─────────────┴──────────────┴──────────────┤
│       Service Providers (Register → Boot → Shutdown)     │
├─────────────────────────────────────────────────────────┤
│      Explicit DI — No magic, compile-time safety         │
└─────────────────────────────────────────────────────────┘
```

## Documentation

Full documentation at **[velocity.velocitykode.com/docs](https://velocity.velocitykode.com/docs)**

## Community

- [GitHub Discussions](https://github.com/velocitykode/velocity/discussions)
- [Issues](https://github.com/velocitykode/velocity/issues)
- [Contributing](CONTRIBUTING.md)

## License

Velocity is open-source software licensed under the [MIT License](LICENSE).
