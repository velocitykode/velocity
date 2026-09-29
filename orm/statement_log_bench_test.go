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

// BenchmarkManagerQueryObserved reads a result set with a dispatcher
// installed (the statement observer on) and the statement log off.
func BenchmarkManagerQueryObserved(b *testing.B) {
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:"})
	if err != nil {
		b.Fatalf("NewManager: %v", err)
	}
	defer m.Shutdown(context.Background())
	m.SetEventDispatcher(func(context.Context, any) error { return nil })
	ctx := context.Background()
	db := m.DB()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "CREATE TABLE observed (id INTEGER)"); err != nil {
		b.Fatalf("create: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO observed (id) VALUES (1)"); err != nil {
		b.Fatalf("insert: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.QueryContext(ctx, "SELECT id FROM observed")
		if err != nil {
			b.Fatalf("query: %v", err)
		}
		for rows.Next() {
		}
		_ = rows.Close()
	}
}
