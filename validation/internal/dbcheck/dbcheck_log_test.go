package dbcheck

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// kvLog records every entry with its key-value pairs.
type kvLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *kvLog) add(level, msg string, kvs []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	parts := []string{level, msg}
	for _, v := range kvs {
		parts = append(parts, fmt.Sprint(v))
	}
	l.entries = append(l.entries, strings.Join(parts, " "))
}

func (l *kvLog) Debug(msg string, kvs ...any)    { l.add("DEBUG", msg, kvs) }
func (l *kvLog) Info(msg string, kvs ...any)     { l.add("INFO", msg, kvs) }
func (l *kvLog) Warn(msg string, kvs ...any)     { l.add("WARN", msg, kvs) }
func (l *kvLog) Error(msg string, kvs ...any)    { l.add("ERROR", msg, kvs) }
func (l *kvLog) Fatal(msg string, kvs ...any)    { l.add("FATAL", msg, kvs) }
func (l *kvLog) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// driverError stands in for a database driver's error type, whose text
// echoes the rejected value the way Postgres and MySQL errors do.
type driverError struct{ text string }

func (e *driverError) Error() string { return e.text }

// A unique or exists rule whose query fails logs one error line that names
// the field, table, column, driver and the kind of error, and carries
// neither the bound value nor the driver's error text; the caller still
// gets the generic message.
func TestRules_QueryFailureLogsNoValueAndNoDriverText(t *testing.T) {
	const secret = "hunter2@example.com"
	failing := func(string, ...interface{}) (int64, error) {
		return 0, fmt.Errorf("count: %w", &driverError{text: `duplicate key value violates unique constraint: Key (email)=(` + secret + `)`})
	}
	for _, tc := range []struct {
		name string
		rule func(string, CountFunc, contract.Logger) contract.RuleHandler
		msg  string
	}{
		{"unique", UniqueRule, "velocity/validation: unique rule query failed"},
		{"exists", ExistsRule, "velocity/validation: exists rule query failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &kvLog{}
			err := tc.rule("postgres", failing, logs)("email", secret, []string{"users", "email"}, nil)
			if err == nil || err.Error() != "Unable to validate email." {
				t.Fatalf("rule error = %v, want the generic message", err)
			}
			if len(logs.entries) != 1 {
				t.Fatalf("lines = %q, want 1", logs.entries)
			}
			line := logs.entries[0]
			if !strings.HasPrefix(line, "ERROR "+tc.msg) {
				t.Errorf("line = %q, want an error line %q", line, tc.msg)
			}
			for _, leak := range []string{secret, "duplicate key", "Key (email)"} {
				if strings.Contains(line, leak) {
					t.Errorf("line carries %q: %s", leak, line)
				}
			}
			for _, want := range []string{"field email", "table users", "column email", "driver postgres", "error_kind *dbcheck.driverError"} {
				if !strings.Contains(line, want) {
					t.Errorf("line %q does not carry %q", line, want)
				}
			}
		})
	}
}

// An error without a wrapped cause is classified by its own type.
func TestRules_QueryFailureClassifiesAPlainError(t *testing.T) {
	logs := &kvLog{}
	failing := func(string, ...interface{}) (int64, error) { return 0, errors.New("plain secret-value") }
	_ = UniqueRule("sqlite", failing, logs)("email", "v", []string{"users"}, nil)
	if len(logs.entries) != 1 || strings.Contains(logs.entries[0], "secret-value") || !strings.Contains(logs.entries[0], "error_kind *errors.errorString") {
		t.Errorf("lines = %q, want one line classifying *errors.errorString without its text", logs.entries)
	}
}
