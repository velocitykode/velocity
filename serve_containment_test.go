//go:build unix

package velocity

import (
	"context"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/scheduler"
)

// serveLinePanicLogger wraps a logger and panics on the lines serveHTTP
// writes around starting and stopping the server and the scheduler.
type serveLinePanicLogger struct{ contract.Logger }

func (l serveLinePanicLogger) Info(msg string, kvs ...any) {
	for _, line := range []string{"Velocity server started", "Scheduler started in-process", "Shutting down server"} {
		if strings.Contains(msg, line) {
			panic("logger broke: " + msg)
		}
	}
	l.Logger.Info(msg, kvs...)
}

// A logger that panics on serveHTTP's own lines skips none of the work
// they announce: the server listens, the in-process scheduler runs, and
// SIGTERM still shuts the app down.
func TestServeHTTP_PanickingLinesSkipNothing(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(probe.Addr().String())
	_ = probe.Close()

	rec := &shutdownRecorder{}
	a, err := NewTestApp(WithSchedulerInProcess(), WithPort(port), WithModules(rec))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	var ticked atomic.Int32
	a.Scheduler.(*scheduler.Scheduler).Before(func() { ticked.Add(1) })
	a.Log = serveLinePanicLogger{Logger: a.Log}

	errCh := make(chan any, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				errCh <- p
			}
		}()
		errCh <- a.serveHTTP()
	}()

	deadline := time.Now().Add(3 * time.Second)
	listening := false
	for time.Now().Before(deadline) && !(listening && ticked.Load() > 0) {
		if c, err := net.Dial("tcp", "127.0.0.1:"+port); err == nil {
			_ = c.Close()
			listening = true
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !listening {
		t.Error("the server never listened: a panicking start line skipped ListenAndServe")
	}
	if ticked.Load() == 0 {
		t.Error("the in-process scheduler never ran: a panicking start line skipped Run")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("syscall.Kill: %v", err)
	}
	select {
	case got := <-errCh:
		if got != nil {
			t.Errorf("serveHTTP = %v, want nil", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveHTTP did not return after SIGTERM")
	}
	if rec.shutdowns.Load() != 1 {
		t.Errorf("module Shutdown ran %d times, want 1: the app was not shut down", rec.shutdowns.Load())
	}
}

// panickingModule's Shutdown panics.
type panickingModule struct{}

func (panickingModule) Init(*app.Services) error       { return nil }
func (panickingModule) Start(*app.Services) error      { return nil }
func (panickingModule) Shutdown(context.Context) error { panic("module shutdown broke") }

// A module whose Shutdown panics does not cut App.Shutdown short: the
// panic becomes that step's error, and the steps after it still run.
func TestShutdown_PanickingModuleBecomesAnError(t *testing.T) {
	rec := &shutdownRecorder{}
	a, err := NewTestApp(WithModules(rec, panickingModule{}))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	probe := &viewShutdownProbe{}
	a.Services.View = probe
	var shutdownErr error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("App.Shutdown panicked: %v", p)
			}
		}()
		shutdownErr = a.Shutdown(context.Background())
	}()
	if shutdownErr == nil || !strings.Contains(shutdownErr.Error(), "module shutdown broke") {
		t.Errorf("App.Shutdown = %v, want the module's panic as an error", shutdownErr)
	}
	if rec.shutdowns.Load() != 1 {
		t.Error("the module registered before the panicking one was not shut down")
	}
	if probe.shutdowns.Load() != 1 {
		t.Error("the view engine, a later step, was not shut down")
	}
}
