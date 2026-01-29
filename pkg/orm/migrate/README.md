# Velocity Database Migrations

Database schema versioning and evolution system for Velocity.

## Features

- ✅ Timestamp-based versioning (no conflicts in team environments)
- ✅ Up/Down migrations with rollback support
- ✅ TableBuilder DSL for type-safe schema definitions
- ✅ Cross-database support (SQLite, MySQL, PostgreSQL)
- ✅ Automatic migration tracking (migrations table)
- ✅ Batch-based rollbacks

## Quick Start

### 1. Create a Migration

**`migrations/20251009120000_create_users.go`**:
```go
package migrations

import "github.com/velocitykode/velocity/pkg/orm/migrate"

func init() {
    migrate.Register(&migrate.Migration{
        Version: "20251009120000",
        Up: func(m *migrate.Migrator) error {
            return m.CreateTable("users", func(t *migrate.TableBuilder) {
                t.ID()
                t.String("email").Unique()
                t.String("name")
                t.Timestamps()
            })
        },
        Down: func(m *migrate.Migrator) error {
            return m.DropTable("users")
        },
    })
}
```

### 2. Run Migrations

```go
import (
    "github.com/velocitykode/velocity/pkg/orm"
    "github.com/velocitykode/velocity/pkg/orm/migrate"
    _ "myapp/migrations" // Import to register
)

// Initialize ORM
orm.Init("sqlite", map[string]any{"database": "./database.db"})

// Run migrations
migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())
err := migrator.Up()
```

## TableBuilder API

```go
t.ID()                           // Auto-increment primary key
t.String("name")                 // VARCHAR(255)
t.String("code", 10)            // VARCHAR(10)
t.Text("bio")                   // TEXT (unlimited)
t.Integer("count")              // INTEGER
t.BigInteger("views")           // BIGINT
t.Boolean("active")             // BOOLEAN
t.Timestamp("verified_at")      // Single TIMESTAMP column
t.Date("birth_date")            // DATE
t.Timestamps()                  // created_at, updated_at
t.SoftDeletes()                 // deleted_at (nullable)

// Modifiers
t.String("email").Unique()      // UNIQUE constraint
t.String("bio").Nullable()      // Allow NULL
t.Integer("status").Default(0)  // Default value
```

## Commands

```go
migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())

// Run pending migrations
migrator.Up()

// Rollback last batch
migrator.Down(1)

// Check status
statuses, _ := migrator.Status()
for _, s := range statuses {
    fmt.Printf("%s: %s\n", s.Version, s.State)
}
```

## Version Format

Migrations use timestamp-based versioning: `YYYYMMDDHHmmss`

Example: `20251009143052` = December 9, 2025 at 14:30:52

## See Also

- `/specs/002-database-testing-system/contracts/migration-api.md` - Full API reference
- `/specs/002-database-testing-system/quickstart.md` - Complete tutorial
