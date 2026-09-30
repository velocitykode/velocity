package grpc

import (
	"context"
	"testing"
)

// BenchmarkDefaultCallEvents measures a unary call through the default
// chain with and without an event dispatcher: with none, no call event is
// built.
func BenchmarkDefaultCallEvents(b *testing.B) {
	for _, withDisp := range []bool{false, true} {
		name := "none"
		if withDisp {
			name = "dispatcher"
		}
		b.Run(name, func(b *testing.B) {
			s := quietServer()
			if withDisp {
				s.SetEventDispatcher(func(context.Context, any) error { return nil })
			}
			call := defaultUnaryCall(s)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				call()
			}
		})
	}
}
