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

// Initialize ORM via the Manager
manager, err := orm.NewManager(orm.Config{
    Driver:   "sqlite",
    Database: "./database.db",
})

// Run migrations
migrator := migrate.NewMigrator(manager.DB(), manager.DriverName())
err = migrator.Up()
```

## TableBuilder API

```go
// Primary Keys
t.ID()                           // Auto-increment integer primary key
t.UUIDPrimary()                  // UUID primary key with auto-generation

// Column Types
t.String("name")                 // VARCHAR(255)
t.String("code", 10)            // VARCHAR(10)
t.Text("bio")                   // TEXT (unlimited)
t.Integer("count")              // INTEGER
t.BigInteger("views")           // BIGINT
t.Boolean("active")             // BOOLEAN
t.UUID("external_id")           // UUID column
t.Timestamp("verified_at")      // Single TIMESTAMP column
t.Date("birth_date")            // DATE
t.Timestamps()                  // created_at, updated_at
t.SoftDeletes()                 // deleted_at (nullable)

// Modifiers
t.String("email").Unique()      // UNIQUE constraint
t.String("bio").Nullable()      // Allow NULL
t.Integer("status").Default(0)  // Default value
```

### UUID Primary Keys

For distributed systems or external-facing APIs where sequential IDs pose security risks:

```go
// Migration
m.CreateTable("projects", func(t *migrate.TableBuilder) {
    t.UUIDPrimary()              // UUID primary key
    t.String("name")
    t.Timestamps()
})

// Model (use UUIDModel instead of Model)
type Project struct {
    orm.UUIDModel[Project]
    Name string `orm:"column:name" json:"name"`
}
```

**Database-specific behavior:**
- **PostgreSQL**: Uses `UUID` type with `gen_random_uuid()` default
- **MySQL**: Uses `CHAR(36)`
- **SQLite**: Uses `TEXT`

## Commands

```go
migrator := migrate.NewMigrator(manager.DB(), manager.DriverName())

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
