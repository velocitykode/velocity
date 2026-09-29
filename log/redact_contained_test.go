package log

import (
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// benchStringer is a kv value whose text comes from String.
type benchStringer struct{ s string }

func (b benchStringer) String() string { return b.s }

// discardLogger keeps nothing, so the benchmarks measure the wrapper.
type discardLogger struct{}

func (discardLogger) Debug(string, ...any)              {}
func (discardLogger) Info(string, ...any)               {}
func (discardLogger) Warn(string, ...any)               {}
func (discardLogger) Error(string, ...any)              {}
func (discardLogger) Fatal(string, ...any)              {}
func (d discardLogger) With(kvs ...any) contract.Logger { return contract.BindFields(d, kvs...) }

// BenchmarkRedactingLogger_Stringer measures a redacted line carrying a
// fmt.Stringer value.
func BenchmarkRedactingLogger_Stringer(b *testing.B) {
	wrapped := WithRedactors(discardLogger{}, RedactorFunc(func(s string) string { return s }))
	v := benchStringer{s: "value"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wrapped.Info("message", "k", v)
	}
}

// panicStringer panics when formatted.
type panicStringer struct{}

func (panicStringer) String() string { panic("stringer boom") }

// nilStringer has a pointer receiver; a nil one panics when called directly.
type nilStringer struct{ s string }

func (n *nilStringer) String() string { return n.s }

// A value whose String panics, or a nil Stringer, is formatted through fmt
// by the redacting logger, as every driver formats it: the panic stays in
// the value's text and never escapes the log call.
func TestRedactingLogger_FaultyStringerDoesNotPanic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"panicking String", panicStringer{}},
		{"nil receiver", (*nilStringer)(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := &capturingLogger{}
			wrapped := WithRedactors(cap, RedactorFunc(func(s string) string { return s }))
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("log call panicked: %v", p)
				}
			}()
			wrapped.Info("m", "k", tc.value)
			if len(cap.kvs) != 2 {
				t.Fatalf("kvs = %v, want the pair", cap.kvs)
			}
		})
	}
}
