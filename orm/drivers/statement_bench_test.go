package drivers

import (
	"context"
	"testing"
)

// discardObserver wants every statement and drops it.
type discardObserver struct{}

func (discardObserver) Observing() bool                 { return true }
func (discardObserver) ObserveStatement(StatementEvent) {}

// BenchmarkObservedExec measures one Exec through an instrumented pool
// with an observer attached and the query log off: the path that copies
// the bound values into the statement's event.
func BenchmarkObservedExec(b *testing.B) {
	blob := make([]byte, 64)
	for _, bc := range []struct {
		name string
		args []any
	}{
		{"int and string args", []any{int64(7), "alice"}},
		{"64-byte []byte arg", []any{int64(7), blob}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			db, binding, _, _ := openScripted(b, &script{})
			binding.logQueries = false
			binding.set(discardObserver{})
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				if _, err := db.ExecContext(ctx, "UPDATE t SET a = ? WHERE b = ?", bc.args...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
