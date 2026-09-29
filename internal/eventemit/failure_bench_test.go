package eventemit

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

type benchLogger struct{}

func (benchLogger) Debug(string, ...any)          {}
func (benchLogger) Info(string, ...any)           {}
func (benchLogger) Warn(string, ...any)           {}
func (benchLogger) Error(string, ...any)          {}
func (benchLogger) Fatal(string, ...any)          {}
func (l benchLogger) With(...any) contract.Logger { return l }

// BenchmarkFailuresRecord measures the failure path with a hook installed:
// the count, the (already logged) first-failure check and the hook call
// behind its per-goroutine re-entry guard.
func BenchmarkFailuresRecord(b *testing.B) {
	var f Failures
	f.SetHook(func(error, any) {})
	err := errors.New("boom")
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		f.Record(ctx, benchLogger{}, err, "bench.event")
	}
}
