// Package fallbacklogtest captures what framework values write through the
// fallback logger (internal/fallbacklog), and what reaches the standard
// library log package and slog.Default, so a test can assert that a value
// used without a logger writes its warnings and failures through the one
// fallback and nowhere else.
package fallbacklogtest

import (
	"bytes"
	stdlog "log"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/fallbacklog"
)

// Output is a captured stream. It is safe for concurrent use.
type Output struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *Output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

// String returns everything captured so far.
func (o *Output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// Lines returns the captured lines, without their line breaks.
func (o *Output) Lines() []string {
	s := strings.TrimSuffix(o.String(), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// Count returns how many captured lines contain level followed by msg, the
// fallback format's "<LEVEL> <message>" (for example "WARN velocity/bond:").
func (o *Output) Count(level, msg string) int {
	n := 0
	for _, line := range o.Lines() {
		if strings.Contains(line, " "+level+" "+msg) {
			n++
		}
	}
	return n
}

// Wait polls until Count(level, msg) reaches n or the deadline passes, for
// lines a background goroutine writes, and returns the last count.
func (o *Output) Wait(level, msg string, n int, within time.Duration) int {
	deadline := time.Now().Add(within)
	for {
		got := o.Count(level, msg)
		if got >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Capture redirects the fallback logger to a fresh Output until the test
// ends, and returns it.
func Capture(t testing.TB) *Output {
	t.Helper()
	out := &Output{}
	prev := fallbacklog.SetOutput(out)
	t.Cleanup(func() { fallbacklog.SetOutput(prev) })
	return out
}

// CaptureStdlib redirects the standard library log package, and with it the
// default slog handler, to a fresh Output until the test ends, and returns
// it. A slog default a test installed elsewhere is replaced with the
// standard one for the duration, so slog.Default writes land here too.
func CaptureStdlib(t testing.TB) *Output {
	t.Helper()
	out := &Output{}
	prevSlog, prevOut, prevFlags := slog.Default(), stdlog.Writer(), stdlog.Flags()
	stdlog.SetOutput(out)
	slog.SetDefault(slog.New(slog.NewTextHandler(out, nil)))
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		stdlog.SetOutput(prevOut)
		stdlog.SetFlags(prevFlags)
	})
	return out
}
