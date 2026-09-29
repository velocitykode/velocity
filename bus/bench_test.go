package bus

import (
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// BenchmarkBusDispatch measures the hot path with no middleware and no event
// dispatcher set: the composed chain is read lock-free and the command type
// name is never computed (it is only needed to label bus.command.* events).
func BenchmarkBusDispatch(b *testing.B) {
	bus := New()
	Register(bus, func(cmd createUser) error {
		return nil
	})
	cmd := createUser{Name: "Alice", Email: "alice@example.com"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bus.Dispatch(cmd); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBusDispatchMiddleware measures the middleware-backed hot path. The
// compiled invocation chain is built once and read lock-free, so each dispatch
// avoids rebuilding the pipeline via New().Send().Through().Then().
func BenchmarkBusDispatchMiddleware(b *testing.B) {
	bus := New()
	Register(bus, func(cmd createUser) error {
		return nil
	})
	bus.Through(
		Middleware(func(cmd Command, next func(Command) error) error {
			return next(cmd)
		}),
		Middleware(func(cmd Command, next func(Command) error) error {
			return next(cmd)
		}),
		Middleware(func(cmd Command, next func(Command) error) error {
			return next(cmd)
		}),
	)
	cmd := createUser{Name: "Alice", Email: "alice@example.com"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bus.Dispatch(cmd); err != nil {
			b.Fatal(err)
		}
	}
}

// discardLogger drops every line.
type discardLogger struct{}

func (discardLogger) Debug(string, ...any)          {}
func (discardLogger) Info(string, ...any)           {}
func (discardLogger) Warn(string, ...any)           {}
func (discardLogger) Error(string, ...any)          {}
func (discardLogger) Fatal(string, ...any)          {}
func (l discardLogger) With(...any) contract.Logger { return l }

// BenchmarkBusDispatchLogging measures a dispatch through LoggingMiddleware
// with a logger that drops its lines, so only the middleware's own cost
// shows.
func BenchmarkBusDispatchLogging(b *testing.B) {
	bus := New()
	Register(bus, func(cmd createUser) error {
		return nil
	})
	bus.Through(LoggingMiddleware(discardLogger{}))
	cmd := createUser{Name: "Alice", Email: "alice@example.com"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bus.Dispatch(cmd); err != nil {
			b.Fatal(err)
		}
	}
}
