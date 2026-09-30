package drivers

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A key or value whose Error, String or Format method panics, a nested
// panic included, is written as errchain.Unreadable: no panic leaves the
// log call and the line is kept.
func TestConsoleLogger_UnformattableValues(t *testing.T) {
	for name, v := range hostile.Unformattables() {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			l := NewConsoleLoggerTo(&buf, contract.LogLevelDebug)
			if p := hostile.Within(t, hostile.Deadline, func() {
				l.Info("value line", "v", v)
				l.Info("key line", v, "x")
				l.With("bound", v).Warn("bound line")
			}); p != nil {
				t.Fatalf("a panic escaped: %v", p)
			}
			out := buf.String()
			for _, want := range []string{"value line | v=" + errchain.Unreadable, "key line | " + errchain.Unreadable + "=x", "bound line | bound=" + errchain.Unreadable} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
	}
}

// Concurrent lines carrying hostile values; run with -race -cpu 1,2.
func TestConsoleLogger_UnformattableValuesConcurrent(t *testing.T) {
	var buf safeBuffer
	l := NewConsoleLoggerTo(&buf, contract.LogLevelDebug)
	values := hostile.Unformattables()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				for _, v := range values {
					l.Info("line", "v", v)
				}
			}
		})
	}
	wg.Wait()
	if n := strings.Count(buf.String(), "\n"); n != 8*50*len(values) {
		t.Fatalf("lines = %d, want %d", n, 8*50*len(values))
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
