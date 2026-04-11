# Velocity

**Full-stack Go framework.**

Everything you need to build, ship, and run web applications — routing, ORM, authentication, cache, queues, mail, storage, real-time, and more. One binary. No runtime dependencies.

## Get Started

```bash
brew tap velocitykode/tap && brew install velocity
```

```bash
velocity new myapp && cd myapp && velocity serve
```

Or add to an existing project:

```bash
go get github.com/velocitykode/velocity
```

```go
func main() {
    v, _ := velocity.New()

    v.Router.Get("/", func(c *router.Context) error {
        return c.JSON(200, map[string]string{"hello": "world"})
    })

    v.Serve()
}
```

As your application grows, use the declarative bootstrap API:

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

### Everything Included

ORM, authentication, caching, queues, mail, storage, events, scheduling, validation, encryption, notifications, WebSockets, gRPC. All built in, all designed to work together. No hunting for compatible third-party packages.

### One Binary Deployment

`go build` produces a single binary. No runtime, no containers required, no config files to sync. Copy it to your server and run it.

### Type-Safe ORM

Generic models return `[]User`, not `[]interface{}`. Queries are checked at compile time. If the types don't match, it doesn't build.

```go
users, _ := User{}.Where("active = ?", true).
    OrderBy("created_at", "DESC").
    Limit(10).
    Get()
```

### Swap Drivers, Not Code

Every subsystem uses pluggable drivers configured through environment variables.

| Subsystem | Drivers |
|---|---|
| Database | PostgreSQL, MySQL, SQLite |
| Cache | Memory, File, Redis, Database |
| Queue | Memory, Redis, Database |
| Storage | Local, S3, Memory |
| Mail | Postmark, Mailgun, Log |
| Auth | Session, JWT |
| Broadcasting | WebSocket |

SQLite in development, PostgreSQL in production. One env var.

### Secure by Default

Security headers, HTTPS redirect, CORS, CSRF, rate limiting. All built in with safe defaults. CORS rejects all cross-origin requests until you explicitly allow them.

### No Magic

No runtime reflection for dependency injection. No implicit model fetching. No hidden interceptors. Every line of code does what it says.

## What's Included

| Category | Features |
|---|---|
| **Web** | Radix-tree router, middleware, security headers, CORS, HTTPS redirect, rate limiting |
| **Data** | Generic ORM with query builder, migrations, eager loading, pagination |
| **Auth** | Session and JWT guards, gates, policies |
| **Cache** | Memory, file, Redis, and database drivers |
| **Queue** | Background jobs with retries, batching, and signing |
| **Events** | Sync and async listeners, wildcard matching, queued listeners |
| **Mail** | Postmark, Mailgun, and log drivers with templates |
| **Storage** | Local filesystem, S3, and memory with a unified API |
| **Real-time** | WebSocket server with rooms and broadcasting, gRPC with HTTP gateway |
| **Scheduling** | Cron-based task scheduling with callbacks |
| **Notifications** | Multi-channel delivery: mail, database, broadcast, Slack |
| **Validation** | Rule engine with database-aware rules |
| **Security** | AES-256-GCM encryption, CSRF protection, key rotation |
| **Observability** | Distributed tracing, instrumented HTTP client |
| **Utilities** | Collections, string helpers, async primitives, pipeline processing |

## Requirements

Go 1.26 or higher.

## Documentation

[velocity.velocitykode.com/docs](https://velocity.velocitykode.com/docs)

## Community

- [GitHub Discussions](https://github.com/velocitykode/velocity/discussions)
- [Issues](https://github.com/velocitykode/velocity/issues)
- [Contributing](CONTRIBUTING.md)

## License

Velocity is open-source software licensed under the [MIT License](LICENSE).
