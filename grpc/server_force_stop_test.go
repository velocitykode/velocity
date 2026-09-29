package grpc_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
)

// pausingLogger blocks on the first line containing match until release
// is closed, and signals entered when it does.
type pausingLogger struct {
	match   string
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newPausingLogger(match string) *pausingLogger {
	return &pausingLogger{match: match, entered: make(chan struct{}), release: make(chan struct{})}
}

func (l *pausingLogger) line(msg string) {
	if !strings.Contains(msg, l.match) {
		return
	}
	paused := false
	l.once.Do(func() { paused = true })
	if paused {
		close(l.entered)
		<-l.release
	}
}
func (l *pausingLogger) Debug(msg string, _ ...any)  { l.line(msg) }
func (l *pausingLogger) Info(msg string, _ ...any)   { l.line(msg) }
func (l *pausingLogger) Warn(msg string, _ ...any)   { l.line(msg) }
func (l *pausingLogger) Error(msg string, _ ...any)  { l.line(msg) }
func (l *pausingLogger) Fatal(msg string, _ ...any)  { l.line(msg) }
func (l *pausingLogger) With(...any) contract.Logger { return l }

// An independent Stop that reaches grpc-go before the owning stop runs as
// stop work: a caller-supplied listener whose Close, which grpc-go calls
// under its own lock, calls Shutdown does not wait on the drain that
// Stop's grpc-go lock holds up.
func TestServerStop_IndependentForceRunsAsStopWork(t *testing.T) {
	logger := newPausingLogger("gracefully stopping")
	nested := newNestedStop(stopCalls["Shutdown"])
	lis := &nestedListener{Listener: loopback(t), nested: nested}
	s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(logger))
	nested.server.Store(s)
	startHealth(t, s)

	owner := make(chan struct{})
	go func() {
		defer close(owner)
		s.GracefulStop()
	}()
	<-logger.entered // the owner is paused before grpc-go
	nested.armed.Store(true)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		s.Stop() // reaches grpc-go first, which closes the listener
	}()
	select {
	case <-nested.done:
	case <-time.After(3 * time.Second):
		close(logger.release)
		t.Fatal("Shutdown from the listener's Close waited on the drain held up by grpc-go's lock")
	}
	close(logger.release)
	within(t, 3*time.Second, "Stop", func() { <-stopped })
	within(t, 3*time.Second, "the owning GracefulStop", func() { <-owner })
	if nested.err == nil {
		t.Error("Shutdown from inside the stop = nil, want the nested-stop error")
	}
}
