package log

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
)

// writePanicLogger binds with With but panics on every write.
type writePanicLogger struct{}

func (writePanicLogger) Debug(string, ...any)          { panic("write boom") }
func (writePanicLogger) Info(string, ...any)           { panic("write boom") }
func (writePanicLogger) Warn(string, ...any)           { panic("write boom") }
func (writePanicLogger) Error(string, ...any)          { panic("write boom") }
func (writePanicLogger) Fatal(string, ...any)          { panic("write boom") }
func (l writePanicLogger) With(...any) contract.Logger { return l }

// A child whose write panics (not its With) has the line written to the
// fallback with every pair bound on the stack, through nested Withs.
func TestStackLogger_FallbackKeepsBoundFields(t *testing.T) {
	var buf bytes.Buffer
	prev := fallbacklog.SetOutput(&buf)
	defer fallbacklog.SetOutput(prev)

	stack := NewStackLogger(writePanicLogger{})
	stack.With("request_id", "r").With("user", "u").Warn("failed", "k", "v")

	line := strings.TrimSpace(buf.String())
	for _, want := range []string{"WARN failed", "request_id=r", "user=u", "k=v"} {
		if !strings.Contains(line, want) {
			t.Errorf("fallback line %q lacks %q", line, want)
		}
	}
	if i, j := strings.Index(line, "request_id=r"), strings.Index(line, "k=v"); i > j {
		t.Errorf("fallback line %q: bound pairs must come before the line's own", line)
	}

	// A stack with nothing bound adds nothing.
	buf.Reset()
	stack.Warn("plain", "k", "v")
	if got := strings.TrimSpace(buf.String()); strings.Contains(got, "request_id") || !strings.Contains(got, "plain k=v") {
		t.Errorf("unbound stack fallback line = %q", got)
	}
}

// Many goroutines binding on one stack and writing through the fallback
// never share a bound slice (run under -race).
func TestStackLogger_BoundFieldsConcurrent(t *testing.T) {
	var buf lockedBuffer
	prev := fallbacklog.SetOutput(&buf)
	defer fallbacklog.SetOutput(prev)

	base := NewStackLogger(writePanicLogger{}).With("base", "b")
	done := make(chan struct{})
	for i := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := range 50 {
				base.With("g", i).With("n", j).Warn("m")
			}
		}()
	}
	for range 8 {
		<-done
	}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !strings.Contains(line, "base=b g=") || !strings.Contains(line, " n=") {
			t.Fatalf("fallback line %q lost its bound pairs", line)
		}
	}
}

// lockedBuffer is a bytes.Buffer safe for concurrent writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
