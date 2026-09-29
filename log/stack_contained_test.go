package log

import (
	"bytes"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
)

// BenchmarkStackLogger_Info measures one line fanned out to two children.
func BenchmarkStackLogger_Info(b *testing.B) {
	stack := NewStackLogger(discardLogger{}, discardLogger{})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stack.Info("message", "k", "v")
	}
}

// panicLogger panics on every call, With included.
type panicLogger struct{}

func (panicLogger) Debug(string, ...any)        { panic("child boom") }
func (panicLogger) Info(string, ...any)         { panic("child boom") }
func (panicLogger) Warn(string, ...any)         { panic("child boom") }
func (panicLogger) Error(string, ...any)        { panic("child boom") }
func (panicLogger) Fatal(string, ...any)        { panic("child boom") }
func (panicLogger) With(...any) contract.Logger { panic("child boom") }

// A child that panics is contained: the children after it still get the
// line, the log call returns normally, and the failed child's warning or
// error lands on the fallback logger, bound pairs included.
func TestStackLogger_PanickingChildIsContained(t *testing.T) {
	var buf bytes.Buffer
	prev := fallbacklog.SetOutput(&buf)
	defer fallbacklog.SetOutput(prev)

	after := &capturingLogger{}
	stack := NewStackLogger(panicLogger{}, after)
	levels := map[string]func(Logger){
		"DEBUG": func(l Logger) { l.Debug("m", "k", "v") },
		"INFO":  func(l Logger) { l.Info("m", "k", "v") },
		"WARN":  func(l Logger) { l.Warn("m", "k", "v") },
		"ERROR": func(l Logger) { l.Error("m", "k", "v") },
		"FATAL": func(l Logger) { l.Fatal("m", "k", "v") },
	}
	for level, call := range levels {
		for _, l := range []Logger{stack, stack.With("bound", "b")} {
			*after = capturingLogger{}
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("%s: panic escaped the stack: %v", level, p)
					}
				}()
				call(l)
			}()
			if after.level != level {
				t.Errorf("%s: the child after the panicking one got %q", level, after.level)
			}
		}
	}
	if !strings.Contains(buf.String(), "WARN m bound=b k=v") {
		t.Errorf("fallback output %q lacks the contained bound WARN line", buf.String())
	}
}
