//go:build cgo

package sqlite

import (
	"fmt"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/velocitykode/velocity/internal/sqlerr"
)

// The cgo SQLite driver's error is a listed kind, bare and wrapped.
func TestErrorKind_CgoSQLiteDriverError(t *testing.T) {
	err := sqlite3.Error{Code: sqlite3.ErrConstraint, ExtendedCode: sqlite3.ErrConstraintUnique}
	for _, e := range []error{err, fmt.Errorf("insert: %w", err)} {
		if got := sqlerr.Kind(e); got != "sqlite3.Error" {
			t.Errorf("Kind(%v) = %q, want %q", e, got, "sqlite3.Error")
		}
	}
}
