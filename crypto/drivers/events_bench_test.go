package drivers

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// quietLogger discards every line.
type quietLogger struct{}

func (quietLogger) Debug(string, ...any)          {}
func (quietLogger) Info(string, ...any)           {}
func (quietLogger) Warn(string, ...any)           {}
func (quietLogger) Error(string, ...any)          {}
func (quietLogger) Fatal(string, ...any)          {}
func (q quietLogger) With(...any) contract.Logger { return q }

// BenchmarkLegacyDecryptEvent measures the note a legacy decrypt makes,
// with and without an event dispatcher: with none, no event is built.
func BenchmarkLegacyDecryptEvent(b *testing.B) {
	for _, withDisp := range []bool{false, true} {
		name := "none"
		if withDisp {
			name = "dispatcher"
		}
		b.Run(name, func(b *testing.B) {
			d, err := NewAESDriver(make([]byte, 32), nil, "AES-256-CBC")
			if err != nil {
				b.Fatal(err)
			}
			d.SetLogger(quietLogger{})
			if withDisp {
				d.SetEventDispatcher(func(context.Context, any) error { return nil })
			}
			d.noteLegacyIfV0(0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.noteLegacyIfV0(0)
			}
		})
	}
}
