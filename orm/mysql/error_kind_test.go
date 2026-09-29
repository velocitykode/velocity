package mysql

import (
	"fmt"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/velocitykode/velocity/internal/sqlerr"
)

// The MySQL driver's error is a listed kind, bare and wrapped, and the
// kind carries none of its message text.
func TestErrorKind_MySQLDriverError(t *testing.T) {
	err := &drivermysql.MySQLError{Number: 1062, Message: "Duplicate entry 'hunter2@example.com' for key 'email'"}
	for _, e := range []error{err, fmt.Errorf("insert: %w", err)} {
		if got := sqlerr.Kind(e); got != "*mysql.MySQLError" {
			t.Errorf("Kind(%v) = %q, want %q", e, got, "*mysql.MySQLError")
		}
	}
}
