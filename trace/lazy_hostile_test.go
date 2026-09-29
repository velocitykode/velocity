package trace

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// With the entropy source down, the first read of a request's lazy ids
// warns through the package logger. The warning is written after the
// ids are cached, outside the LazyTrace's Once: a logger that reads the
// same request's ids, blocks or panics leaves both ids set and every
// other reader served.
func TestLazyTrace_EntropyWarningIsWrittenOutsideTheOnce(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			withRandReader(t, failingReader{})
			t.Cleanup(func() { SetLogger(nil) })
			var l LazyTrace
			code := hostile.New(t, mode, func() { _, _ = l.IDs() })
			SetLogger(hostile.NewLogger(code, hostile.Warn))

			call := func() { _, _ = l.IDs() }
			blocked := make(chan struct{})
			if mode == hostile.Block {
				go func() { //safe-goroutine: the test releases the block below and waits for it
					defer close(blocked)
					_, _ = l.IDs()
				}()
				<-code.Entered()
			} else {
				close(blocked)
			}
			p := hostile.Within(t, hostile.Deadline, call)
			if p != nil && mode != hostile.Panic {
				t.Errorf("IDs panicked: %v", p)
			}
			code.Release()
			code.Disarm()
			// The package state is reset at cleanup: the blocked read
			// must be done with it first.
			hostile.Within(t, hostile.Deadline, func() { <-blocked })
			hostile.Within(t, hostile.Deadline, func() {
				traceID, spanID := l.IDs()
				if !strings.HasPrefix(traceID, FallbackTraceIDPrefix) || !strings.HasPrefix(spanID, FallbackSpanIDPrefix) {
					t.Errorf("ids = %q, %q, want both fallback ids set", traceID, spanID)
				}
			})
		})
	}
}
