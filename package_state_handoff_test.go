package velocity

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/trace"
)

// sweepGatedMailer is a mail driver whose SetLogger, called from the app
// logger sweep (wireInstanceLoggers) once armed, blocks until the gate
// opens: it holds an app in the middle of a wiring boundary.
type sweepGatedMailer struct {
	armed   atomic.Bool
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
}

func (*sweepGatedMailer) Send(context.Context, *mail.Message) error { return nil }

func (m *sweepGatedMailer) SetLogger(contract.Logger) {
	if m.armed.Load() && strings.Contains(string(debug.Stack()), "velocity.wireInstanceLoggers") {
		m.once.Do(func() { close(m.entered) })
		<-m.gate
	}
}

// An app's installation of the process-wide state is published in one
// step. A newer app held in the middle of its first wiring boundary does
// not own part of the state while an older app shuts down: once the older
// app's Shutdown returns (and its logger is closed), neither package
// logger is the older app's.
func TestPackageState_OlderAppShutdownDuringANewerAppsWiringLeavesNoClosedLogger(t *testing.T) {
	fallbacklogtest.Capture(t)
	a := newOwnedApp(t)

	gated := &sweepGatedMailer{entered: make(chan struct{}), gate: make(chan struct{})}
	gated.armed.Store(true)
	const driver = "package-state-sweep-gated"
	prev := mail.Drivers().Override(driver, func(context.Context, mail.MailConfig) (mail.Mailer, error) { return gated, nil })
	t.Cleanup(func() { mail.Drivers().Override(driver, prev) })
	useLogDriver(t, driver, &levelLogger{})
	cfg := packageTestConfig(driver)
	cfg.Mail = mail.MailConfig{Driver: driver}

	var (
		b    *App
		bErr error
	)
	built := make(chan struct{})
	go func() {
		defer close(built)
		b, bErr = New(WithConfig(cfg))
	}()
	var gateOnce sync.Once
	openGate := func() {
		gated.armed.Store(false)
		gateOnce.Do(func() { close(gated.gate) })
	}
	t.Cleanup(func() {
		openGate()
		<-built
		if b != nil {
			_ = b.Shutdown(context.Background())
		}
	})

	select {
	case <-gated.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the newer app's logger sweep never reached the mail driver")
	}

	if err := a.app.Shutdown(context.Background()); err != nil {
		t.Fatalf("older app Shutdown: %v", err)
	}
	if got := async.GetLogger(); got == contract.Logger(a.logger) {
		t.Errorf("async logger is the shut-down older app's logger")
	}
	if got := trace.GetLogger(); got == contract.Logger(a.logger) {
		t.Errorf("trace logger is the shut-down older app's logger")
	}

	openGate()
	<-built
	if bErr != nil {
		t.Fatalf("newer app New: %v", bErr)
	}
}

// Removing an installation clears its slot: after apps shut down in
// either order the stack's backing array past the live entries holds no
// reference to a shut-down app's installation.
func TestPackageState_ReleaseClearsTheVacatedSlots(t *testing.T) {
	fallbacklogtest.Capture(t)
	for _, order := range []string{"newest first", "oldest first"} {
		t.Run(order, func(t *testing.T) {
			packageStateMu.Lock()
			before := len(packageStack)
			packageStateMu.Unlock()

			apps := []ownedApp{newOwnedApp(t), newOwnedApp(t), newOwnedApp(t)}
			if order == "newest first" {
				apps[0], apps[2] = apps[2], apps[0]
			}
			for _, o := range apps {
				if err := o.app.Shutdown(context.Background()); err != nil {
					t.Fatalf("Shutdown: %v", err)
				}
			}

			packageStateMu.Lock()
			defer packageStateMu.Unlock()
			if len(packageStack) != before {
				t.Fatalf("stack holds %d entries, want %d", len(packageStack), before)
			}
			for i, e := range packageStack[len(packageStack):cap(packageStack)] {
				if e != nil {
					t.Errorf("vacated slot %d still references an installation", len(packageStack)+i)
				}
			}
		})
	}
}

// A shut-down app with the default error handler is not kept reachable
// by the process-wide package state it installed.
func TestPackageState_ShutDownAppIsCollectable(t *testing.T) {
	fallbacklogtest.Capture(t)
	const driver = "package-state-collectable"
	useLogDriver(t, driver, &levelLogger{})
	w := func() weak.Pointer[App] {
		a, err := New(WithConfig(packageTestConfig(driver)))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := a.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		return weak.Make(a)
	}()
	for i := 0; i < 10 && w.Value() != nil; i++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if w.Value() != nil {
		t.Fatal("a shut-down app is still reachable")
	}
}

// A report made through a shut-down file-logging app's error handler (a
// panic hook selected before the app shut down, running after) reaches
// the standalone fallback logger instead of being lost with the closed
// file.
func TestPackageState_ReportAfterShutdownReachesTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	cfg := packageTestConfig("file")
	cfg.Log.Config = map[string]any{"path": filepath.Join(t.TempDir(), "logs"), "days": 0}
	a, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := a.Services.Errors
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	before := len(fallback.Lines())
	h.Report(errors.New("late panic"), backgroundErrorContext(context.Background(), contract.ErrorSourceGoroutine))
	var found bool
	for _, line := range fallback.Lines()[before:] {
		if strings.Contains(line, "ERROR") && strings.Contains(line, "late panic") {
			found = true
		}
	}
	if !found {
		t.Errorf("the late report never reached the fallback; fallback lines since: %v", fallback.Lines()[before:])
	}
}
