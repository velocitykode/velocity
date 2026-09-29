package sqlerr

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

type codedError struct{ text string }

func (e *codedError) Error() string { return e.text }

func TestKind(t *testing.T) {
	coded := &codedError{text: "Duplicate entry 'alice@example.com' for key 'email'"}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"driver type", coded, "*sqlerr.codedError"},
		{"wrapped driver type", fmt.Errorf("insert: %w", fmt.Errorf("exec: %w", coded)), "*sqlerr.codedError"},
		{"joined takes the first branch", errors.Join(coded, errors.New("x")), "*sqlerr.codedError"},
		{"empty join", emptyJoin{}, "sqlerr.emptyJoin"},
		{"plain", errors.New("secret"), "*errors.errorString"},
		{"canceled", fmt.Errorf("q: %w", context.Canceled), "context.Canceled"},
		{"deadline", context.DeadlineExceeded, "context.DeadlineExceeded"},
		{"tx done", sql.ErrTxDone, "sql.ErrTxDone"},
		{"conn done", sql.ErrConnDone, "sql.ErrConnDone"},
		{"no rows", sql.ErrNoRows, "sql.ErrNoRows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Kind(tc.err); got != tc.want {
				t.Errorf("Kind = %q, want %q", got, tc.want)
			}
		})
	}
}

type emptyJoin struct{}

func (emptyJoin) Error() string   { return "empty" }
func (emptyJoin) Unwrap() []error { return nil }
