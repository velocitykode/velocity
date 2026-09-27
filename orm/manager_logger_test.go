package orm

import (
	"context"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
)

// Logger returns what SetLogger installed, and the fallback logger when
// nothing is installed or nil was.
func TestManagerLogger_ReturnsTheInstalledLoggerOrTheFallback(t *testing.T) {
	m := &Manager{}
	if _, ok := m.Logger().(fallbacklog.Logger); !ok {
		t.Errorf("Logger() without a logger = %T, want fallbacklog.Logger", m.Logger())
	}
	l := logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0)
	m.SetLogger(l)
	if got := m.Logger(); got != contract.Logger(l) {
		t.Errorf("Logger() = %T, want the installed logger", got)
	}
	m.SetLogger(nil)
	if _, ok := m.Logger().(fallbacklog.Logger); !ok {
		t.Errorf("Logger() after SetLogger(nil) = %T, want fallbacklog.Logger", m.Logger())
	}
}

// Logger may be read while SetLogger runs: both go through the manager's
// read-write mutex.
func TestManagerLogger_WhileSetLoggerIsSafe(t *testing.T) {
	m := &Manager{}
	out := &fallbacklogtest.Output{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
				m.SetLogger(nil)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = m.Logger()
			}
		}()
	}
	wg.Wait()
}

// Logger keeps answering after Shutdown: the installed logger, then the
// fallback once nil is set.
func TestManagerLogger_AfterShutdown(t *testing.T) {
	m := &Manager{}
	l := logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0)
	m.SetLogger(l)
	_ = m.Shutdown(context.Background())
	if got := m.Logger(); got != contract.Logger(l) {
		t.Errorf("Logger() after Shutdown = %T, want the installed logger", got)
	}
	m.SetLogger(nil)
	if _, ok := m.Logger().(fallbacklog.Logger); !ok {
		t.Errorf("Logger() after SetLogger(nil) = %T, want fallbacklog.Logger", m.Logger())
	}
}
