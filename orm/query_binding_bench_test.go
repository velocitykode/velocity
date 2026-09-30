package orm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// bindingArgs returns n bound values of the kinds an app binds most:
// integers, strings, floats, byte slices and times, in turn.
func bindingArgs(n int) []any {
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	args := make([]any, n)
	for i := range args {
		switch i % 5 {
		case 0:
			args[i] = int64(i)
		case 1:
			args[i] = "value"
		case 2:
			args[i] = 1.5
		case 3:
			args[i] = []byte("bytes")
		case 4:
			args[i] = at
		}
	}
	return args
}

// BenchmarkQueryBindings runs a statement binding 0, 1, 4 and 16 values,
// with and without a dispatcher (the event, and its bindings, are built
// only with one), on one goroutine and from every P.
func BenchmarkQueryBindings(b *testing.B) {
	for _, n := range []int{0, 1, 4, 16} {
		for _, dispatcher := range []bool{false, true} {
			q := "SELECT 1"
			if n > 0 {
				q = "SELECT " + strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
			}
			args := bindingArgs(n)
			setup := func(b *testing.B) *Manager {
				m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:"})
				if err != nil {
					b.Fatalf("NewManager: %v", err)
				}
				b.Cleanup(func() { _ = m.Shutdown(context.Background()) })
				if dispatcher {
					m.SetEventDispatcher(func(context.Context, any) error { return nil })
				}
				return m
			}
			name := fmt.Sprintf("binds=%d/dispatcher=%v", n, dispatcher)
			b.Run(name, func(b *testing.B) {
				m := setup(b)
				db := m.DB()
				ctx := context.Background()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := db.ExecContext(ctx, q, args...); err != nil {
						b.Fatalf("exec: %v", err)
					}
				}
			})
			b.Run(name+"/parallel", func(b *testing.B) {
				m := setup(b)
				db := m.DB()
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					ctx := context.Background()
					for pb.Next() {
						if _, err := db.ExecContext(ctx, q, args...); err != nil {
							b.Errorf("exec: %v", err)
							return
						}
					}
				})
			})
		}
	}
}
