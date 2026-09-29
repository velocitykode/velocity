package trace

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// With the entropy source down, the first Must* call warns once through the
// package logger. The line is claimed before it is written and no caller
// waits on it: a logger that panics is contained (the Must* caller still
// gets its fallback id), one that mints an id itself returns, and one that
// blocks holds only its own caller while every other Must* call proceeds.
func TestMustGenerateTraceID_EntropyWarningIsContained(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			withRandReader(t, failingReader{})
			t.Cleanup(func() { SetLogger(nil) })
			code := hostile.New(t, mode, func() { _ = MustGenerateSpanID() })
			SetLogger(hostile.NewLogger(code, hostile.Warn))

			blocked := make(chan struct{})
			call := func() {
				if id := MustGenerateTraceID(); !strings.HasPrefix(id, FallbackTraceIDPrefix) {
					t.Errorf("id = %q, want a fallback id", id)
				}
			}
			if mode == hostile.Block {
				go func() { //safe-goroutine: the test releases the block below and waits for it
					defer close(blocked)
					_ = MustGenerateTraceID()
				}()
				code.AwaitEntered(t)
			} else {
				close(blocked)
			}
			if p := hostile.Within(t, hostile.Deadline, call); p != nil {
				t.Errorf("MustGenerateTraceID panicked: %v", p)
			}
			code.Release()
			// The package state is reset at cleanup: the blocked call must
			// be done with it first.
			hostile.Within(t, hostile.Deadline, func() { <-blocked })
			if code.Calls() != 1 {
				t.Errorf("warning written %d times, want once", code.Calls())
			}
		})
	}
}
