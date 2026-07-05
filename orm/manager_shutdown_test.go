package orm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/orm/drivers"
)

// setupShutdownTest builds a connected manager with the test_users table,
// installs it as the package default, and seeds one row. The caller decides
// when to Shutdown; the setupConvenienceTests cleanup makes a second
// Shutdown a no-op.
func setupShutdownTest(t *testing.T) *Manager {
	t.Helper()
	m := setupConvenienceTests(t)
	seedUser(t, m, "Alice", "alice-shutdown@example.com", 30)
	return m
}

// TestQueryAfterShutdown_ReturnsErrManagerShutdown asserts that every
// execution entry point returns ErrManagerShutdown (never panics) when the
// query is built and executed after Manager.Shutdown.
func TestQueryAfterShutdown_ReturnsErrManagerShutdown(t *testing.T) {
	m := setupShutdownTest(t)

	// A named connection registered pre-shutdown must also surface the
	// sentinel post-shutdown instead of a stale or nil driver.
	other := newTestManager(t)
	t.Cleanup(func() { other.Shutdown(context.Background()) })
	m.AddConnection("other", other.DefaultDriver())

	ctx := context.Background()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{"query.Get", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).Get(ctx)
			return err
		}},
		{"query.First", func() error {
			var u TestUser
			return Model[TestUser]{}.Where("age > ?", 0).First(ctx, &u)
		}},
		{"model.Find", func() error {
			_, err := Model[TestUser]{}.Find(ctx, 1)
			return err
		}},
		{"query.Count", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).Count(ctx)
			return err
		}},
		{"query.Exists", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).Exists(ctx)
			return err
		}},
		{"query.Pluck", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).Pluck(ctx, "name")
			return err
		}},
		{"query.Value", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).Value(ctx, "name")
			return err
		}},
		{"query.Sum", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).Sum(ctx, "age")
			return err
		}},
		{"query.Increment", func() error {
			return Model[TestUser]{}.Where("age > ?", 0).Increment(ctx, "age")
		}},
		{"query.Update", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).Update(ctx, map[string]any{"name": "x"})
			return err
		}},
		{"query.Delete", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).Delete(ctx)
			return err
		}},
		{"query.ForceDelete", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).ForceDelete(ctx)
			return err
		}},
		{"query.Save", func() error {
			u := TestUser{Name: "Bob", Email: "bob-shutdown@example.com", Age: 20}
			return Model[TestUser]{}.Where("age > ?", 0).Save(ctx, &u)
		}},
		{"query.Create", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).Create(ctx, map[string]any{
				"name": "Bob", "email": "bob2-shutdown@example.com", "age": 20,
			})
			return err
		}},
		{"query.InsertGetId", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).InsertGetId(ctx, map[string]any{
				"name": "Bob", "email": "bob3-shutdown@example.com", "age": 20,
			})
			return err
		}},
		{"query.Paginate", func() error {
			_, err := Model[TestUser]{}.Where("age > ?", 0).Paginate(ctx, 1, 10)
			return err
		}},
		{"package.Save", func() error {
			u := TestUser{Name: "Bob", Email: "bob4-shutdown@example.com", Age: 20}
			return Save(ctx, m, &u)
		}},
		{"rawquery.Get", func() error {
			_, err := NewRawQuery[TestUser]("SELECT * FROM test_users").Get(ctx)
			return err
		}},
		{"rawquery.First", func() error {
			var u TestUser
			return NewRawQuery[TestUser]("SELECT * FROM test_users LIMIT 1").First(ctx, &u)
		}},
		{"rawquery.Scan", func() error {
			var n int
			return NewRawQuery[TestUser]("SELECT COUNT(*) FROM test_users").Scan(ctx, &n)
		}},
		{"rawquery.Exec", func() error {
			_, err := NewRawQuery[TestUser]("DELETE FROM test_users").Exec(ctx)
			return err
		}},
		{"manager.Raw", func() error {
			_, err := m.Raw(ctx, "SELECT 1")
			return err
		}},
		{"manager.Exec", func() error {
			_, err := m.Exec(ctx, "DELETE FROM test_users")
			return err
		}},
		{"manager.Begin", func() error {
			_, err := m.Begin(ctx)
			return err
		}},
		{"manager.Transaction", func() error {
			return m.Transaction(ctx, func(ctx context.Context) error { return nil })
		}},
		{"manager.Ping", func() error {
			return m.Ping()
		}},
		{"manager.Connection", func() error {
			_, err := m.Connection("other")
			return err
		}},
		{"manager.Introspector", func() error {
			_, err := m.Introspector()
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatalf("%s after Shutdown: expected ErrManagerShutdown, got nil", tt.name)
			}
			if !errors.Is(err, ErrManagerShutdown) {
				t.Fatalf("%s after Shutdown: expected ErrManagerShutdown, got %v", tt.name, err)
			}
		})
	}
}

// TestQueryBuiltBeforeShutdown_ReturnsErrManagerShutdown covers the stale
// driver pointer case: a chain constructed while the manager was live must
// still fail with the sentinel (not a nil dereference, not a raw
// database/sql closed-connection error) when executed after Shutdown.
func TestQueryBuiltBeforeShutdown_ReturnsErrManagerShutdown(t *testing.T) {
	m := setupShutdownTest(t)
	ctx := context.Background()

	preBuilt := Model[TestUser]{}.Where("age > ?", 0)
	preBuiltRaw := NewRawQuery[TestUser]("SELECT * FROM test_users")

	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	if _, err := preBuilt.Get(ctx); !errors.Is(err, ErrManagerShutdown) {
		t.Fatalf("pre-built query.Get after Shutdown: expected ErrManagerShutdown, got %v", err)
	}
	if _, err := preBuiltRaw.Get(ctx); !errors.Is(err, ErrManagerShutdown) {
		t.Fatalf("pre-built rawquery.Get after Shutdown: expected ErrManagerShutdown, got %v", err)
	}
}

// TestQueryWithoutConnection_ReturnsErrNoConnection asserts the two
// sentinels stay distinct: a manager that was never connected reports
// ErrNoConnection, not ErrManagerShutdown.
func TestQueryWithoutConnection_ReturnsErrNoConnection(t *testing.T) {
	m := &Manager{connections: map[string]drivers.Driver{}}
	prev := Default()
	SetDefault(m)
	t.Cleanup(func() { SetDefault(prev) })

	ctx := context.Background()

	if _, err := (Model[TestUser]{}).Where("age > ?", 0).Get(ctx); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("query.Get without connection: expected ErrNoConnection, got %v", err)
	}
	if err := m.Ping(); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("manager.Ping without connection: expected ErrNoConnection, got %v", err)
	}
	if err := m.Transaction(ctx, func(ctx context.Context) error { return nil }); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("manager.Transaction without connection: expected ErrNoConnection, got %v", err)
	}

	// Never-connected must not be misreported as shut down.
	if _, err := (Model[TestUser]{}).Where("age > ?", 0).Get(ctx); errors.Is(err, ErrManagerShutdown) {
		t.Fatal("query.Get without connection: must not match ErrManagerShutdown")
	}
}

// TestShutdownConcurrentWithQueries_NoPanic races Shutdown against a pool
// of goroutines continuously exercising reads, writes, and transactions.
// The invariant under -race: no data race, no panic; queries fail with an
// error once shutdown lands, and a fresh query afterwards reports the
// sentinel.
func TestShutdownConcurrentWithQueries_NoPanic(t *testing.T) {
	m := setupShutdownTest(t)
	ctx := context.Background()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				switch worker % 4 {
				case 0:
					_, _ = Model[TestUser]{}.Where("age > ?", 0).Get(ctx)
				case 1:
					_, _ = Model[TestUser]{}.Where("id = ?", 1).Update(ctx, map[string]any{"name": "racer"})
				case 2:
					_, _ = Model[TestUser]{}.Where("age > ?", 0).Count(ctx)
				case 3:
					_ = m.Transaction(ctx, func(txCtx context.Context) error {
						_, err := Model[TestUser]{}.Where("age > ?", 0).Get(txCtx)
						return err
					})
				}
			}
		}(i)
	}

	time.Sleep(20 * time.Millisecond)
	_ = m.Shutdown(ctx) // close errors from in-flight work are acceptable
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()

	if _, err := (Model[TestUser]{}).Where("age > ?", 0).Get(ctx); !errors.Is(err, ErrManagerShutdown) {
		t.Fatalf("query after concurrent shutdown: expected ErrManagerShutdown, got %v", err)
	}
}
