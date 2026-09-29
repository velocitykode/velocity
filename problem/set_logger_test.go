package problem

import (
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// SetLogger moves the handler's own messages and the LogReporter it built
// to l; nil puts both back on the fallback logger.
func TestHandler_SetLogger_MovesItsMessagesAndDefaultReporter(t *testing.T) {
	first, second := newCountLogger(), newCountLogger()
	h := NewHandler(WithHandlerLogger(first), WithEnvironment("local"))
	h.SetLogger(second)
	h.Report(errors.New("moved"), &ErrorContext{})
	h.SetDebug(true)
	if n := first.total(); n != 0 {
		t.Errorf("first logger lines = %d, want 0", n)
	}
	if n := second.count("error"); n != 1 {
		t.Errorf("second logger error lines = %d, want 1", n)
	}
	if n := second.count("warn"); n != 1 {
		t.Errorf("second logger warn lines = %d, want 1", n)
	}

	fallback := fallbacklogtest.Capture(t)
	h.SetLogger(nil)
	h.Report(errors.New("to the fallback"), &ErrorContext{})
	if n := fallback.Count("ERROR", "to the fallback"); n != 1 {
		t.Errorf("fallback lines after SetLogger(nil) = %d, want 1", n)
	}
	if n := second.count("error"); n != 1 {
		t.Errorf("second logger error lines after SetLogger(nil) = %d, want 1", n)
	}
}

// SetLogger leaves reporters the application supplied on their own logger.
func TestHandler_SetLogger_LeavesAppReporters(t *testing.T) {
	own, handlerLog := newCountLogger(), newCountLogger()
	h := NewHandler(WithReporters(NewLogReporter(WithLogger(own))))
	h.SetLogger(handlerLog)
	h.Report(errors.New("app reporter"), &ErrorContext{})
	if n := own.count("error"); n != 1 {
		t.Errorf("app reporter logger lines = %d, want 1", n)
	}
	if n := handlerLog.count("error"); n != 0 {
		t.Errorf("handler logger report lines = %d, want 0", n)
	}
}

// LogReporter.SetLogger replaces its logger; nil restores the fallback.
func TestLogReporter_SetLogger(t *testing.T) {
	a, b := newCountLogger(), newCountLogger()
	r := NewLogReporter(WithLogger(a))
	r.SetLogger(b)
	r.Report(errors.New("x"), nil)
	if a.total() != 0 || b.count("error") != 1 {
		t.Errorf("lines a=%d b=%d, want 0 and 1", a.total(), b.count("error"))
	}
	fallback := fallbacklogtest.Capture(t)
	r.SetLogger(nil)
	r.Report(errors.New("y"), nil)
	if n := fallback.Count("ERROR", "y"); n != 1 {
		t.Errorf("fallback lines = %d, want 1", n)
	}
	var zero LogReporter
	zero.SetLogger(a)
	zero.Report(errors.New("z"), nil)
	if n := a.count("error"); n != 1 {
		t.Errorf("zero-value reporter lines = %d, want 1", n)
	}
}

// Logger replacements racing reports and handler notices are safe.
func TestHandler_SetLoggerConcurrentWithReports(t *testing.T) {
	h := NewHandler(WithEnvironment("local"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.SetLogger(newCountLogger())
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.Report(errors.New("race"), &ErrorContext{})
				h.SetDebug(j%2 == 0)
			}
		}()
	}
	wg.Wait()
}

// countLogger counts lines by level.
type countLogger struct {
	mu     sync.Mutex
	counts map[string]int
}

func newCountLogger() *countLogger { return &countLogger{counts: map[string]int{}} }

func (l *countLogger) add(level string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[level]++
}
func (l *countLogger) count(level string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[level]
}
func (l *countLogger) total() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.counts {
		n += c
	}
	return n
}
func (l *countLogger) Debug(string, ...any)            { l.add("debug") }
func (l *countLogger) Info(string, ...any)             { l.add("info") }
func (l *countLogger) Warn(string, ...any)             { l.add("warn") }
func (l *countLogger) Error(string, ...any)            { l.add("error") }
func (l *countLogger) Fatal(string, ...any)            { l.add("fatal") }
func (l *countLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }
