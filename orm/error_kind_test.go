package orm

import (
	"context"
	"errors"
	"fmt"
	"testing"

	moderncsqlite "modernc.org/sqlite"

	"github.com/velocitykode/velocity/internal/sqlerr"
)

// The pure-Go SQLite driver's error is a listed kind: a real constraint
// failure from the default backend, bare and wrapped.
func TestErrorKind_PureGoSQLiteDriverError(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	ctx := context.Background()
	if _, err := m.Exec(ctx, "CREATE TABLE kinds (email TEXT UNIQUE)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.Exec(ctx, "INSERT INTO kinds (email) VALUES (?)", "hunter2@example.com"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, err := m.Exec(ctx, "INSERT INTO kinds (email) VALUES (?)", "hunter2@example.com")
	var se *moderncsqlite.Error
	if !errors.As(err, &se) {
		t.Fatalf("duplicate insert error = %T %v, want a *sqlite.Error in its chain", err, err)
	}
	for _, e := range []error{err, fmt.Errorf("save: %w", err)} {
		if got := sqlerr.Kind(e); got != "*sqlite.Error" {
			t.Errorf("Kind = %q, want %q", got, "*sqlite.Error")
		}
	}
}
