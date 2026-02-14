# Velocity Database Testing

Model factories and test isolation utilities for Velocity.

## Features

- ✅ Fluent factory API for generating test data
- ✅ Faker integration for realistic data (gofakeit/v6)
- ✅ RefreshDatabase for test isolation
- ✅ State and Sequence support
- ✅ Cross-database compatibility

## Quick Start

### 1. Setup Test Environment

Create **`.env.testing`** in your project root:

```env
APP_ENV=testing

DB_DRIVER=sqlite
DB_DATABASE=:memory:

# Or use a dedicated test database:
# DB_DRIVER=postgres
# DB_DATABASE=myapp_test
# DB_HOST=localhost
# DB_USERNAME=postgres
# DB_PASSWORD=secret
```

**`tests/bootstrap_test.go`**:
```go
package tests

import (
    "os"
    "testing"

    "github.com/joho/godotenv"
    "github.com/velocitykode/velocity/orm"
    _ "myapp/migrations"
)

func TestMain(m *testing.M) {
    // Load .env.testing
    godotenv.Load("../.env.testing")

    // Initialize from environment
    orm.Init(
        os.Getenv("DB_DRIVER"),
        map[string]any{
            "database": os.Getenv("DB_DATABASE"),
            "host":     os.Getenv("DB_HOST"),
            "username": os.Getenv("DB_USERNAME"),
            "password": os.Getenv("DB_PASSWORD"),
        },
    )
    defer orm.Close()

    m.Run()
}
```

Now your tests **always** use the test database automatically! 🎉

### 2. Define Factories

```go
package tests

import ormtesting "github.com/velocitykode/velocity/orm/testing"

func UserFactory() *ormtesting.Factory {
    faker := ormtesting.Faker()

    factory := ormtesting.NewFactory("users", func() map[string]interface{} {
        return map[string]interface{}{
            "name":  faker.Name(),
            "email": faker.Email(),
        }
    })

    factory.DefineState("admin", map[string]interface{}{
        "role": "admin",
    })

    return factory
}
```

### 2. Write Tests

**Option A: LazyRefreshDatabase** (Fast - recommended):
```go
func TestExample(t *testing.T) {
    tc := ormtesting.NewTestCase(t)
    tc.LazyRefreshDatabase() // Migrations run once, transaction per test

    // Your test - automatically rolled back after
    user := UserFactory().Create()
    assert.NotNil(t, user)
}
```

**Option B: RefreshDatabase** (Thorough - for DDL changes):
```go
func TestExample(t *testing.T) {
    tc := ormtesting.NewTestCase(t)
    tc.RefreshDatabase() // Drops all tables, runs migrations EVERY test

    // Your test
    user := UserFactory().Create()
}
```

**When to use each:**
- ✅ `LazyRefreshDatabase`: 99% of tests (fast, transactions)
- ✅ `RefreshDatabase`: Tests that modify schema or disable constraints

## Factory API

### Basic Methods

```go
factory.Make()                   // Generate in-memory (no DB)
factory.Create()                 // Generate and persist to DB
factory.Count(10)                // Generate 10 records
factory.State("admin")           // Apply named state
factory.Sequence("email", fn)    // Sequential values
```

### Chaining

```go
UserFactory().
    Count(50).
    State("verified").
    Sequence("email", func(i int) interface{} {
        return fmt.Sprintf("user%d@test.com", i)
    }).
    Create(map[string]interface{}{
        "created_at": time.Now(),
    })
```

## Test Isolation Methods

### LazyRefreshDatabase (Recommended)

Runs migrations once, wraps each test in a transaction:

```go
func TestSomething(t *testing.T) {
    tc := ormtesting.NewTestCase(t)
    tc.LazyRefreshDatabase()

    // Test runs in transaction - rolled back automatically
    UserFactory().Count(100).Create()
}
```

**Performance**: ~1ms per test ⚡

### RefreshDatabase (Thorough)

Drops all tables and re-runs migrations for each test:

```go
func TestSomething(t *testing.T) {
    tc := ormtesting.NewTestCase(t)
    tc.RefreshDatabase()

    // Completely fresh database
}
```

**Performance**: ~15ms per test (SQLite :memory:)

### Safety

RefreshDatabase has built-in safety checks:
- ✅ Requires `testing.T` (only works in tests)
- ✅ Checks `APP_ENV != "production"`
- ✅ Validates database name contains "test" or is ":memory:"

## Faker

Access to gofakeit library:

```go
faker := ormtesting.Faker()

faker.Name()              // "John Doe"
faker.Email()             // "john@example.com"
faker.Phone()             // "+1-555-0123"
faker.City()              // "San Francisco"
faker.Sentence(5)         // Random sentence
faker.Paragraph(3,5,10," ") // Random paragraph
faker.UUID()              // "550e8400-e29b..."
faker.Number(1, 100)      // Random number
faker.Bool()              // true or false
```

## Example Test

```go
func TestPostsIndex(t *testing.T) {
    ormtesting.RefreshDatabase(t)

    // Setup
    user := UserFactory().Create()
    userID := user.(map[string]interface{})["id"]

    PostFactory().Count(3).Create(map[string]interface{}{
        "user_id":   userID,
        "published": true,
    })

    // Test
    router := gin.New()
    router.GET("/posts", controllers.PostsIndex)

    w := httptest.NewRecorder()
    req := httptest.NewRequest("GET", "/posts", nil)
    router.ServeHTTP(w, req)

    // Assert
    assert.Equal(t, 200, w.Code)
    var posts []map[string]interface{}
    json.Unmarshal(w.Body.Bytes(), &posts)
    assert.Equal(t, 3, len(posts))
}
```

## See Also

- `/specs/002-database-testing-system/contracts/factory-api.md` - Full factory API
- `/specs/002-database-testing-system/contracts/refresh-api.md` - RefreshDatabase API
- `/specs/002-database-testing-system/quickstart.md` - Complete tutorial
