package auth

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/orm"
)

// TestORMUserProviderEmitsQueryEvents is the regression test for the blind
// spot that motivated moving query telemetry into the database/sql driver.
//
// ORMUserProvider holds the raw *sql.DB unwrapped out of the ORM manager, so
// none of its statements pass through the query builder or drivers.Driver. The
// credentials lookup runs on every single login and used to emit nothing at
// all, leaving authentication invisible to APM. Every provider statement must
// now produce a query.executed event.
func TestORMUserProviderEmitsQueryEvents(t *testing.T) {
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Shutdown(context.Background())

	// No orm.SetDefault: telemetry is bound to the manager that owns the
	// pool, so a manager constructed directly reports through its own
	// dispatcher.
	db := manager.DB()
	if _, err := db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			email TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL,
			remember_token TEXT
		)
	`); err != nil {
		t.Fatalf("create users: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO users (name, email, password) VALUES (?, ?, ?)",
		"Test User", "test@example.com", "hashed",
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	var (
		mu       sync.Mutex
		executed []*orm.QueryExecuted
	)
	manager.SetEventDispatcher(func(_ context.Context, ev any) error {
		mu.Lock()
		defer mu.Unlock()
		if q, ok := ev.(*orm.QueryExecuted); ok {
			executed = append(executed, q)
		}
		return nil
	})

	seen := func(needle string) []*orm.QueryExecuted {
		// Statement events are delivered asynchronously so a listener can
		// never stall a database call; force delivery before asserting.
		if err := manager.FlushQueryEvents(context.Background()); err != nil {
			t.Fatalf("FlushQueryEvents: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		var out []*orm.QueryExecuted
		for _, q := range executed {
			if strings.Contains(q.SQL, needle) {
				out = append(out, q)
			}
		}
		return out
	}

	provider := NewORMUserProviderForDialect(db, "User", &mockHasher{}, "sqlite")
	ctx := context.Background()

	// The login lookup: one query per authentication attempt.
	user, err := provider.FindByCredentialsCtx(ctx, map[string]interface{}{
		"email": "test@example.com",
	})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	byCreds := seen("WHERE email =")
	if len(byCreds) != 1 {
		t.Fatalf("credentials lookup: want 1 QueryExecuted, got %d", len(byCreds))
	}
	if byCreds[0].Connection != "sqlite" {
		t.Errorf("Connection = %q, want sqlite", byCreds[0].Connection)
	}
	if byCreds[0].RowsAffected != 1 {
		t.Errorf("RowsAffected = %d, want 1", byCreds[0].RowsAffected)
	}
	if byCreds[0].Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", byCreds[0].Duration)
	}

	if _, err := provider.FindByIDCtx(ctx, user.GetAuthIdentifier()); err != nil {
		t.Fatalf("FindByIDCtx: %v", err)
	}
	if got := len(seen("WHERE id =")); got != 1 {
		t.Errorf("id lookup: want 1 QueryExecuted, got %d", got)
	}

	if err := provider.UpdateRememberTokenCtx(ctx, user, "token-1"); err != nil {
		t.Fatalf("UpdateRememberTokenCtx: %v", err)
	}
	if got := len(seen("UPDATE users SET remember_token")); got != 1 {
		t.Errorf("remember-token update: want 1 QueryExecuted, got %d", got)
	}

	swapped, err := provider.CompareAndSwapRememberToken(ctx, user, "token-1", "token-2")
	if err != nil {
		t.Fatalf("CompareAndSwapRememberToken: %v", err)
	}
	if !swapped {
		t.Fatal("CompareAndSwapRememberToken reported no swap")
	}
	if got := len(seen("UPDATE users SET remember_token")); got != 2 {
		t.Errorf("remember-token writes: want 2 QueryExecuted total, got %d", got)
	}
}

// TestORMUserProviderEmitsQueryFailed covers the failure side of the same
// path: a provider statement against a missing table must surface as
// query.failed rather than as a zero-row success.
func TestORMUserProviderEmitsQueryFailed(t *testing.T) {
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Shutdown(context.Background())

	var (
		mu       sync.Mutex
		failed   []*orm.QueryFailed
		executed int
	)
	manager.SetEventDispatcher(func(_ context.Context, ev any) error {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *orm.QueryFailed:
			failed = append(failed, e)
		case *orm.QueryExecuted:
			executed++
		}
		return nil
	})

	// No users table exists on this connection.
	provider := NewORMUserProviderForDialect(manager.DB(), "User", &mockHasher{}, "sqlite")
	if _, err := provider.FindByCredentialsCtx(context.Background(), map[string]interface{}{
		"email": "test@example.com",
	}); err == nil {
		t.Fatal("expected the lookup to fail against a missing table")
	}
	if err := manager.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(failed) != 1 {
		t.Fatalf("want 1 QueryFailed, got %d", len(failed))
	}
	if executed != 0 {
		t.Errorf("failing lookup also emitted %d QueryExecuted events", executed)
	}
	if !strings.Contains(failed[0].Query, "FROM users") {
		t.Errorf("Query = %q, want the provider statement", failed[0].Query)
	}
	if failed[0].Error == "" {
		t.Error("Error is empty")
	}
}
