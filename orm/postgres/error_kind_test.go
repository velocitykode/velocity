package postgres

import (
	"fmt"
	"testing"

	"github.com/lib/pq"

	"github.com/velocitykode/velocity/internal/sqlerr"
)

// The Postgres driver's error is a listed kind, bare and wrapped, and the
// kind carries none of its detail text.
func TestErrorKind_PostgresDriverError(t *testing.T) {
	err := &pq.Error{Code: "23505", Message: "duplicate key value", Detail: "Key (email)=(hunter2@example.com) already exists."}
	for _, e := range []error{err, fmt.Errorf("insert: %w", err)} {
		if got := sqlerr.Kind(e); got != "*pq.Error" {
			t.Errorf("Kind(%v) = %q, want %q", e, got, "*pq.Error")
		}
	}
}
