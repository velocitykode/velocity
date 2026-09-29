package trace

import (
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
)

// blockingWarnLogger blocks every Warn call until release is closed,
// after signalling entered once.
type blockingWarnLogger struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *blockingWarnLogger) Debug(string, ...any) {}
func (l *blockingWarnLogger) Info(string, ...any)  {}
func (l *blockingWarnLogger) Warn(string, ...any) {
	l.once.Do(func() { close(l.entered) })
	<-l.release
}
func (l *blockingWarnLogger) Error(string, ...any)            {}
func (l *blockingWarnLogger) Fatal(string, ...any)            {}
func (l *blockingWarnLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// SetLogger never waits on the entropy warning in flight through the
// logger it replaces.
func TestSetLogger_DoesNotWaitForTheWarningInFlight(t *testing.T) {
	withRandReader(t, failingReader{})
	l := &blockingWarnLogger{entered: make(chan struct{}), release: make(chan struct{})}
	SetLogger(l)
	t.Cleanup(func() { SetLogger(nil) })

	warned := make(chan struct{})
	go func() {
		defer close(warned)
		warnRandUnavailable()
	}()
	t.Cleanup(func() { <-warned })
	defer close(l.release)
	select {
	case <-l.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("warning never reached the logger")
	}
	done := make(chan struct{})
	go func() {
		SetLogger(nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetLogger waited on the warning in flight through the old logger")
	}
	if _, ok := GetLogger().(fallbacklog.Logger); !ok {
		t.Fatalf("GetLogger after SetLogger(nil) = %T, want fallbacklog.Logger", GetLogger())
	}
}
