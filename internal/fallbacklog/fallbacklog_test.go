package fallbacklog

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// capture redirects the fallback output to a buffer for the test.
func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := SetOutput(&buf)
	t.Cleanup(func() { SetOutput(prev) })
	return &buf
}

var linePrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z (WARN|ERROR) `)

// stripTime returns line without its timestamp, failing when the line does
// not open with one.
func stripTime(t *testing.T, line string) string {
	t.Helper()
	if !linePrefix.MatchString(line) {
		t.Fatalf("line %q does not open with a UTC timestamp and a level", line)
	}
	return line[len("2006-01-02T15:04:05.000Z "):]
}

// Warn, Error and Fatal write one line each in the one format; Debug and
// Info write nothing.
func TestLogger_WritesWarnAndAboveInOneLineFormat(t *testing.T) {
	buf := capture(t)
	var l contract.Logger = Logger{}

	l.Debug("debug line", "k", "v")
	l.Info("info line", "k", "v")
	l.Warn("velocity/queue: warn line", "host", "cache.internal", "port", 6379)
	l.Error("error line", "error", errors.New("db down"))
	l.Fatal("fatal line")

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	want := []string{
		`WARN velocity/queue: warn line host=cache.internal port=6379`,
		`ERROR error line error="db down"`,
		`ERROR fatal line`,
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %q, want %d lines", lines, len(want))
	}
	for i, w := range want {
		if got := stripTime(t, lines[i]); got != w {
			t.Errorf("line %d = %q, want %q", i, got, w)
		}
	}
}

// A value, key or message that would break the line or be ambiguous is
// quoted, so every call is exactly one line.
func TestLogger_QuotesWhatWouldBreakTheLine(t *testing.T) {
	buf := capture(t)
	Logger{}.Error("panic\nrecovered", "stack", "goroutine 1\n\tmain.go:3", "empty", "", "eq", "a=b", "odd key", 1, "dangling")

	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("output %q is not exactly one line", out)
	}
	got := stripTime(t, strings.TrimSuffix(out, "\n"))
	want := `ERROR "panic\nrecovered" stack="goroutine 1\n\tmain.go:3" empty="" eq="a=b" "odd key"=1 !BADKEY=dangling`
	if got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}

// Resolve keeps a logger it is given and substitutes the fallback for nil.
func TestResolve(t *testing.T) {
	if _, ok := Resolve(nil).(Logger); !ok {
		t.Errorf("Resolve(nil) = %T, want Logger", Resolve(nil))
	}
	own := &recorder{}
	if Resolve(own) != contract.Logger(own) {
		t.Error("Resolve(l) did not return l")
	}
}

// Lines written from many goroutines never interleave.
func TestLogger_ConcurrentLinesDoNotInterleave(t *testing.T) {
	buf := capture(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Logger{}.Warn("concurrent", "payload", strings.Repeat("x", 512))
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 32 {
		t.Fatalf("lines = %d, want 32", len(lines))
	}
	for _, line := range lines {
		if got := stripTime(t, line); got != "WARN concurrent payload="+strings.Repeat("x", 512) {
			t.Fatalf("interleaved line %q", line)
		}
	}
}

// SetOutput(nil) restores standard error and returns the writer it replaced.
func TestSetOutput_NilRestoresStderr(t *testing.T) {
	var buf bytes.Buffer
	prev := SetOutput(&buf)
	t.Cleanup(func() { SetOutput(prev) })
	if got := SetOutput(nil); got != &buf {
		t.Errorf("SetOutput(nil) returned %v, want the buffer it replaced", got)
	}
}

type recorder struct{}

func (*recorder) Debug(string, ...any) {}
func (*recorder) Info(string, ...any)  {}
func (*recorder) Warn(string, ...any)  {}
func (*recorder) Error(string, ...any) {}
func (*recorder) Fatal(string, ...any) {}
