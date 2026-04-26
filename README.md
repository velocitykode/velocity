<p align="center">
  <img src=".github/assets/logo.png" alt="Velocity" width="480" />
</p>

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
go get github.com/velocitykode/velocity@latest
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

## What's Included

All major services ship as pluggable drivers — switch backends via
config, no code changes.

| Pluggable    | Drivers                          |
| ------------ | -------------------------------- |
| **Database** | MySQL, Postgres, SQLite          |
| **Cache**    | Memory, file, Redis, database    |
| **Queue**    | Memory, Redis, database          |
| **Log**      | Console, file, stack, null       |
| **Storage**  | Local, S3                        |
| **Mail**     | Postmark, Mailgun, log           |

Also included: radix-tree HTTP router with middleware, generic ORM
with migrations and eager loading, auth (session/JWT guards, gates,
policies), validation with database-aware rules, events and typed
command bus, task scheduler, multi-channel notifications
(mail/database/broadcast/Slack), WebSocket broadcasting, gRPC with
HTTP gateway, Inertia.js adapter with optional SSR, AES-256-GCM
encryption with key rotation, CSRF protection, distributed tracing,
structured exceptions with debug pages, maintenance mode, graceful
shutdown, live reload, `vel make:*` scaffolding, and a standard
library of collections, string, async, and pipeline helpers.

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
