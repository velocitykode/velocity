package async

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
)

// blockingLogger blocks every Error call until release is closed, after
// signalling entered once.
type blockingLogger struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingLogger() *blockingLogger {
	return &blockingLogger{entered: make(chan struct{}), release: make(chan struct{})}
}

func (l *blockingLogger) Debug(string, ...any) {}
func (l *blockingLogger) Info(string, ...any)  {}
func (l *blockingLogger) Warn(string, ...any)  {}
func (l *blockingLogger) Error(string, ...any) {
	l.once.Do(func() { close(l.entered) })
	<-l.release
}
func (l *blockingLogger) Fatal(string, ...any)            {}
func (l *blockingLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// SetLogger never waits on a line in flight through the logger it
// replaces: a replacement from another goroutine, or from inside the
// logger's own method, returns at once, and the next line goes to the new
// logger.
func TestSetLogger_DoesNotWaitForALineInFlight(t *testing.T) {
	t.Cleanup(func() { SetLogger(nil) })
	SetPanicHook(nil)
	l := newBlockingLogger()
	SetLogger(l)
	Go(func() { panic("in flight") })
	select {
	case <-l.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("panic line never reached the logger")
	}
	defer close(l.release)

	next := &captureLogger{}
	done := make(chan struct{})
	go func() {
		SetLogger(next)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetLogger waited on the line in flight through the old logger")
	}
	if got := GetLogger(); got != contract.Logger(next) {
		t.Fatalf("GetLogger = %T, want the new logger", got)
	}
}

// selfReplacingLogger installs its successor from inside its own Error.
type selfReplacingLogger struct {
	blockingLogger
	next contract.Logger
}

func (l *selfReplacingLogger) Error(string, ...any) { SetLogger(l.next) }

// A logger that replaces the package logger from inside its own method
// does not deadlock.
func TestSetLogger_FromInsideTheLogger(t *testing.T) {
	t.Cleanup(func() { SetLogger(nil) })
	SetPanicHook(nil)
	next := &captureLogger{}
	SetLogger(&selfReplacingLogger{next: next})
	Go(func() { panic("replace me") })
	deadline := time.Now().Add(2 * time.Second)
	for GetLogger() != contract.Logger(next) {
		if time.Now().After(deadline) {
			t.Fatal("the logger's own SetLogger never took effect")
		}
		time.Sleep(time.Millisecond)
	}
}

// Nil, and the zero state, install the standalone fallback logger.
func TestSetLogger_NilIsTheFallback(t *testing.T) {
	t.Cleanup(func() { SetLogger(nil) })
	SetLogger(&captureLogger{})
	SetLogger(nil)
	if _, ok := GetLogger().(fallbacklog.Logger); !ok {
		t.Fatalf("GetLogger after SetLogger(nil) = %T, want fallbacklog.Logger", GetLogger())
	}
}

// tallyLogger counts the error lines written through it into a counter
// shared by every tallyLogger of a test.
type tallyLogger struct{ n *atomic.Int32 }

func (tallyLogger) Debug(string, ...any)          {}
func (tallyLogger) Info(string, ...any)           {}
func (tallyLogger) Warn(string, ...any)           {}
func (l tallyLogger) Error(string, ...any)        { l.n.Add(1) }
func (tallyLogger) Fatal(string, ...any)          {}
func (l tallyLogger) With(...any) contract.Logger { return l }

// Replacements racing package-logger writes from many goroutines are safe.
// The test returns only once every recovered panic has been logged: a
// recovery still running afterwards would write through the logger or
// hook the next test installs.
func TestSetLogger_ConcurrentWithWrites(t *testing.T) {
	t.Cleanup(func() { SetLogger(nil) })
	SetPanicHook(nil)
	const writers, panics = 8, 50
	var logged atomic.Int32
	SetLogger(tallyLogger{n: &logged})
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < panics; j++ {
				SetLogger(tallyLogger{n: &logged})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < panics; j++ {
				Go(func() { panic("stress") })
			}
		}()
	}
	wg.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for logged.Load() < writers*panics {
		if time.Now().After(deadline) {
			t.Fatalf("logged %d recovered panics, want %d", logged.Load(), writers*panics)
		}
		time.Sleep(time.Millisecond)
	}
}
