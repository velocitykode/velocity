# Velocity

**Full-stack Go framework.**

Everything you need to build, ship, and run web applications — routing,
ORM, authentication, cache, queues, mail, storage, real-time, and more.
One binary. No external runtime. No Docker required for development.

> **Status:** Pre-1.0 (currently v0.32.x). API is still in flux — breaking
> changes may occur between minor releases. See [RELEASES.md](RELEASES.md)
> for the versioning policy and [CHANGELOG.md](CHANGELOG.md) for per-release
> breaking-change notes.

## Get Started

```bash
brew tap velocitykode/tap && brew install velocity
```

```bash
velocity new myapp && cd myapp && vel serve
```

Or add to an existing project:

```bash
go get github.com/velocitykode/velocity
```

```go
package main

import (
    "log"

    "github.com/velocitykode/velocity"
    "github.com/velocitykode/velocity/router"
)

func main() {
    v, err := velocity.New()
    if err != nil {
        log.Fatal(err)
    }

    v.Router.Get("/", func(c *router.Context) error {
        return c.JSON(200, map[string]string{"hello": "world"})
    })

    if err := v.Serve(); err != nil {
        log.Fatal(err)
    }
}
```

For larger apps, declare bootstrap concerns up-front and chain them.
The callbacks below live in your own `app` and `routes` packages —
`velocity new` scaffolds them for you, or you can write them by hand.

```go
func main() {
    v, _ := velocity.New()

    if err := v.
        Middleware(app.Middleware(v)).      // your global/web/API middleware stacks
        Routes(routes.Register).            // your route definitions
        Events(app.Events(v.Log)).          // your event listeners
        Schedule(schedule.Configure).       // your scheduled jobs
        Exceptions(app.ExceptionHandler).   // your custom error handler
        Run(); err != nil {                 // serves HTTP, or runs a `vel ...` command
        log.Fatal(err)
    }
}
```

## Quick Example

User signs up, validation runs, the user is saved, a job goes onto the
queue, and a typed JSON response comes back — all in one handler:

```go
v.Router.Post("/signup", func(c *router.Context) error {
    var input struct {
        Email string `json:"email" validate:"required,email"`
        Name  string `json:"name"  validate:"required"`
    }
    if err := c.Bind(&input); err != nil {
        return c.BadRequest(err.Error())
    }

    user := models.User{Email: input.Email, Name: input.Name}
    if err := user.Save(); err != nil {
        return err
    }

    v.Queue.Push(jobs.SendWelcomeEmail{UserID: user.ID})

    return c.JSON(201, user)
})
```

Validation, ORM, queue, and router — designed to work together.

## Why Velocity

### Everything Included

Data (ORM, cache, queue), auth (sessions, JWT, gates, policies),
messaging (mail, events, notifications, WebSockets, gRPC), and ops
(scheduling, encryption, validation, maintenance mode). All built in,
all designed to work together. No hunting for compatible third-party
packages.

### One Binary Deployment

`go build` produces a single binary. No runtime, no containers
required, no config files to sync. Copy it to your server and run it.

### Type-Safe ORM

Generic models return `[]User`, not `[]interface{}`. Queries are
checked at compile time. If the types don't match, it doesn't build.

```go
users, _ := User{}.Where("active = ?", true).
    OrderBy("created_at", "DESC").
    Limit(10).
    Get()
```

### Swap Drivers, Not Code

Every subsystem uses pluggable drivers configured through environment
variables.

| Subsystem    | Drivers                                                  |
| ------------ | -------------------------------------------------------- |
| Database     | PostgreSQL, MySQL, SQLite                                |
| Cache        | Memory, File, Redis, Database                            |
| Queue        | Memory, Redis, Database                                  |
| Storage      | Local, S3, Memory                                        |
| Mail         | Postmark, Mailgun, Local (writes to disk)                |
| Auth         | Sessions, JWT (plus gates and policies on top)           |
| Broadcasting | WebSocket (additional drivers planned)                   |

SQLite in development, PostgreSQL in production. One env var.

### Secure by Default

Security headers, HTTPS redirect, CORS, CSRF, rate limiting. All built
in with safe defaults. CORS rejects all cross-origin requests until
you explicitly allow them.

### No Magic

No runtime reflection for dependency injection. No implicit model
fetching. No hidden interceptors. Every line of code does what it
says.

## Velocity vs Gin/Fiber

Velocity is a full-stack application framework. Gin and Fiber are HTTP
routers.

Use Gin/Fiber if you want to hand-pick an ORM, cache library, queue,
session store, mailer, and storage backend, then wire them together
yourself.

Use Velocity if you want those decisions made for you with pluggable
drivers and a consistent API — the way Rails and Django make them.

Both approaches are valid. They serve different teams.

## What's Included

| Category                | Features                                                                                  |
| ----------------------- | ----------------------------------------------------------------------------------------- |
| **Web**                 | Radix-tree router, middleware, security headers, CORS, HTTPS redirect, rate limiting     |
| **Data**                | Generic ORM with query builder, migrations, eager loading, pagination                     |
| **Auth**                | Session and JWT guards, gates, policies                                                   |
| **Cache**               | Memory, file, Redis, and database drivers                                                 |
| **Queue**               | Background jobs with retries, batching, and signing                                       |
| **Bus**                 | Typed command bus with middleware and async dispatch via the queue                        |
| **Events**              | Sync and async listeners, wildcard matching, queued listeners                             |
| **Mail**                | Postmark, Mailgun, and local drivers with templates                                       |
| **Storage**             | Local filesystem, S3, and memory with a unified API                                       |
| **Real-time**           | WebSocket server with groups and broadcasting, gRPC with HTTP gateway                     |
| **Frontend**            | Inertia.js adapter (Vue/React/Svelte) with optional SSR via the `bond` package            |
| **Scheduling**          | Cron-based task scheduling with callbacks                                                 |
| **Notifications**       | Multi-channel delivery: mail, database, broadcast, Slack                                  |
| **Validation**          | Rule engine with database-aware rules and form-request structs                            |
| **Security**            | AES-256-GCM encryption, CSRF protection, key rotation                                     |
| **Observability**       | Distributed tracing, instrumented HTTP client, structured exceptions with debug pages     |
| **Operations**          | Maintenance mode (`vel down` / `vel up`), graceful shutdown                               |
| **Developer Experience**| `vel make:*` scaffolding (handler, model, migration, middleware, job, mail, …), live reload |
| **Utilities**           | Collections, string helpers, async primitives, pipeline processing                        |

## Requirements

Go 1.26 or higher.

## Versioning

Velocity follows [semantic versioning](https://semver.org). Until
`v1.0.0` ships, minor versions may include breaking API changes —
always documented in [CHANGELOG.md](CHANGELOG.md) under the version's
**Breaking** section.

## Documentation

[velocity.velocitykode.com/docs](https://velocity.velocitykode.com/docs)

## Community

- [GitHub Discussions](https://github.com/velocitykode/velocity/discussions)
- [Issues](https://github.com/velocitykode/velocity/issues)
- [Contributing](CONTRIBUTING.md)

## License

Velocity is open-source software licensed under the [MIT License](LICENSE).
