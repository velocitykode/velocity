package async

import (
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
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

// SetLogger returns only after every line in flight through the logger it
// replaces has been written, so the caller may close that logger.
func TestSetLogger_WaitsForInFlightWrites(t *testing.T) {
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

	done := make(chan struct{})
	go func() {
		SetLogger(nil)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("SetLogger returned while a line was still being written through the old logger")
	case <-time.After(100 * time.Millisecond):
	}
	close(l.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetLogger did not return after the in-flight line finished")
	}
}

// Replacements racing package-logger writes from many goroutines are safe.
func TestSetLogger_ConcurrentWithWrites(t *testing.T) {
	t.Cleanup(func() { SetLogger(nil) })
	SetPanicHook(nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				SetLogger(&captureLogger{})
			}
		}()
		go func() {
			defer wg.Done()
			var inner sync.WaitGroup
			for j := 0; j < 50; j++ {
				inner.Add(1)
				Go(func() {
					defer inner.Done()
					panic("stress")
				})
			}
			inner.Wait()
		}()
	}
	wg.Wait()
}
