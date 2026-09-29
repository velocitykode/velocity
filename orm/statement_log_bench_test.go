package orm

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// discardLogger takes every line and keeps none, so the benchmark measures
// the statement log's path, not a sink.
type discardLogger struct{}

func (discardLogger) Debug(string, ...any)              {}
func (discardLogger) Info(string, ...any)               {}
func (discardLogger) Warn(string, ...any)               {}
func (discardLogger) Error(string, ...any)              {}
func (discardLogger) Fatal(string, ...any)              {}
func (d discardLogger) With(kvs ...any) contract.Logger { return contract.BindFields(d, kvs...) }

// BenchmarkManagerExecLogged runs a write with the statement log on, the
// line going through the manager's logger as a connection holds it.
func BenchmarkManagerExecLogged(b *testing.B) {
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:", LogQueries: true})
	if err != nil {
		b.Fatalf("NewManager: %v", err)
	}
	defer m.Shutdown(context.Background())
	m.SetLogger(discardLogger{})
	ctx := context.Background()
	if _, err := m.Exec(ctx, "CREATE TABLE logged (id INTEGER, name TEXT)"); err != nil {
		b.Fatalf("create: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Exec(ctx, "INSERT INTO logged (id, name) VALUES (?, ?)", i, "n"); err != nil {
			b.Fatalf("exec: %v", err)
		}
	}
}
