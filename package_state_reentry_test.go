package velocity

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
)

// packageTestConfig is a minimal in-memory app config logging through the
// driver named driver.
func packageTestConfig(driver string) Config {
	return Config{
		Env:   "testing",
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "package_state"},
		Log:   log.LogConfig{Driver: driver, Config: make(map[string]any)},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
	}
}

// useLogDriver registers a log driver named name that returns l for the
// test's duration.
func useLogDriver(t *testing.T, name string, l log.Logger) {
	t.Helper()
	prev := log.Drivers().Override(name, func(context.Context, log.LogConfig) (log.Logger, error) {
		return l, nil
	})
	t.Cleanup(func() { log.Drivers().Override(name, prev) })
}

// reentrantLogger shuts another app down from inside its first GoCtx
// cancellation line, after a pause that lets its own app's Shutdown reach
// the package-state release.
type reentrantLogger struct {
	levelLogger
	armed    atomic.Bool
	entered  chan struct{}
	shutdown func()
}

func (l *reentrantLogger) Error(msg string, kvs ...any) {
	l.levelLogger.Error(msg, kvs...)
	if msg == "async: GoCtx context done" && l.armed.CompareAndSwap(true, false) {
		close(l.entered)
		time.Sleep(200 * time.Millisecond)
		l.shutdown()
	}
}

func (l *reentrantLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// Shutdown never waits on a package-logger line in flight: a line through
// the owning app's logger that shuts another app down, while the owning
// app shuts down, completes both shutdowns instead of deadlocking.
func TestPackageState_ShutdownDoesNotWaitOnALineThatShutsAnotherAppDown(t *testing.T) {
	fallbacklogtest.Capture(t)
	a := newOwnedApp(t)

	aDone := make(chan struct{})
	rl := &reentrantLogger{entered: make(chan struct{})}
	rl.shutdown = func() {
		defer close(aDone)
		_ = a.app.Shutdown(context.Background())
	}
	const driver = "package-state-reentrant"
	useLogDriver(t, driver, rl)
	b, err := New(WithConfig(packageTestConfig(driver)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := async.GetLogger(); got != contract.Logger(rl) {
		t.Fatalf("async logger = %T, want the newest app's", got)
	}

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	rl.armed.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	async.GoCtx(ctx, func(context.Context) { <-release })

	select {
	case <-rl.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the GoCtx cancellation line never reached the app logger")
	}
	bDone := make(chan struct{})
	go func() {
		defer close(bDone)
		_ = b.Shutdown(context.Background())
	}()
	for name, done := range map[string]chan struct{}{"owning app": bDone, "other app": aDone} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s Shutdown never returned: shutdown waited on the line in flight", name)
		}
	}
}
