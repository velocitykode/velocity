# Velocity

> A Laravel-inspired web framework for Go

Velocity brings joy and elegance to Go web development, without sacrificing Go's simplicity, performance, and type safety. Build web applications with expressive APIs and the speed of Go.

## Why Velocity?

**For Framework Users**: Get familiar conventions and developer happiness you love, with Go's performance and concurrency.

**For Go Developers**: Skip the boilerplate and focus on building. Velocity provides a unified, elegant interface over common web development tasks.

### Key Features

- **Single Binary**: Deploy anywhere with zero dependencies
- **Type Safety**: Full compile-time type checking with Go's type system
- **True Concurrency**: Built-in goroutines and channels for high performance
- **Driver-Based Architecture**: Swap implementations via config (Redis ↔ Memory, PostgreSQL ↔ MySQL)
- **Expressive DX**: Familiar conventions, expressive APIs, and developer-friendly errors

## Requirements

- Go 1.25 or higher
- Node.js 18+ (for frontend assets)

## CLI Installation

Install the Velocity CLI to create and manage projects:

```bash
# Homebrew (macOS)
brew tap velocitykode/tap
brew install velocity

# Or with Go
go install github.com/velocitykode/velocity-cli@latest
```

Verify installation:

```bash
velocity version
```

## Quick Start

Create a new project:

```bash
velocity new myapp
cd myapp
velocity serve
```

Your app runs at `http://localhost:3000`

### Manual Setup

Add the framework to an existing project:

```bash
go get github.com/velocitykode/velocity
```

```go
package main

import (
    "github.com/velocitykode/velocity/pkg/router"
    "github.com/velocitykode/velocity/pkg/log"
)

func main() {
    r := router.New()

    r.Get("/", func(c *router.Context) error {
        return c.JSON(200, map[string]string{
            "message": "Welcome to Velocity!",
        })
    })

    log.Info("Starting server on :8080")
    r.Run(":8080")
}
```

## Feature Status

| Package | Status | Description |
|---------|--------|-------------|
| **async** | ✅ Complete | Async primitives and concurrency patterns |
| **auth** | ✅ Complete | Authentication and session management |
| **broadcast** | ✅ Complete | Real-time event broadcasting |
| **cache** | ✅ Complete | Multi-driver caching (memory, file, redis) |
| **config** | ✅ Complete | Configuration management |
| **crypto** | ✅ Complete | Encryption and decryption |
| **csrf** | ✅ Complete | CSRF protection |
| **events** | ✅ Complete | Event system with listeners |
| **log** | ✅ Complete | Structured logging with multiple drivers |
| **mail** | ✅ Complete | Email sending with multiple drivers |
| **orm** | ✅ Complete | Database ORM with relationships |
| **queue** | ✅ Complete | Job queue system with workers |
| **router** | ✅ Complete | HTTP routing with middleware |
| **scheduler** | ✅ Complete | Task scheduling (cron-like) |
| **storage** | ✅ Complete | File storage abstraction |
| **str** | ✅ Complete | String manipulation utilities |
| **testing** | ✅ Complete | Testing helpers and utilities |
| **validation** | ✅ Complete | Input validation |
| **view** | ✅ Complete | Template rendering |
| **vite** | ✅ Complete | Vite.js integration for frontend |
| **websocket** | ✅ Complete | WebSocket support |

## Documentation

Full documentation at **[velocitykode.com/docs](https://velocitykode.com/docs)**

- [Getting Started](https://velocitykode.com/docs/getting-started/)
- [CLI Reference](https://velocitykode.com/docs/cli/)
- [Core Concepts](https://velocitykode.com/docs/core/)
- [Database](https://velocitykode.com/docs/database/)
- [Frontend](https://velocitykode.com/docs/frontend/)
- [Advanced](https://velocitykode.com/docs/advanced/)

## Community

- [GitHub Discussions](https://github.com/velocitykode/velocity/discussions) - Ask questions and share ideas
- [Issues](https://github.com/velocitykode/velocity/issues) - Report bugs and request features
- [Contributing](CONTRIBUTING.md) - Contribution guidelines

## Philosophy

Velocity is built on three principles:

1. **Developer Happiness**: Code should be a joy to write and read
2. **Convention over Configuration**: Sensible defaults, configure only when needed
3. **Go's Strengths**: Embrace simplicity, performance, and type safety

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Velocity is open-source software licensed under the [MIT License](LICENSE).
