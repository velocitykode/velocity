package drivers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
)

// A failed event dispatch goes to the one failure policy: counted, and the
// first failure of each event name logged at warn level through the
// component's logger (the standalone fallback here), not ignored.
func TestAESDriver_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	d := &AESDriver{}
	d.SetEventDispatcher(func(context.Context, interface{}) error { return errors.New("listener failed") })
	for i := 0; i < 2; i++ {
		d.noteLegacyIfV0(0)
	}
	if n := strings.Count(out.String(), "WARN event dispatch failed"); n != 1 || !strings.Contains(out.String(), "event=crypto.legacy.payload.decrypted") {
		t.Errorf("fallback output = %q, want one warn line naming crypto.legacy.payload.decrypted", out.String())
	}
}

// With a logger installed, the failure line goes through it, not the
// fallback; installing the logger after the dispatcher works too.
func TestAESDriver_FailedEventDispatchLogsThroughItsLogger(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	var buf lockedLines
	d := &AESDriver{}
	d.SetEventDispatcher(func(context.Context, interface{}) error { return errors.New("listener failed") })
	d.SetLogger(logdrivers.NewConsoleLoggerTo(&buf, contract.LogLevelUnset))
	d.noteLegacyIfV0(0)
	if got := buf.String(); strings.Count(got, "event dispatch failed") != 1 || !strings.Contains(got, "event=crypto.legacy.payload.decrypted") {
		t.Errorf("component logger got %q, want one failure line naming crypto.legacy.payload.decrypted", got)
	}
	if got := fallback.String(); got != "" {
		t.Errorf("fallback got %q, want nothing", got)
	}
}

// lockedLines is an io.Writer safe for concurrent writes.
type lockedLines struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
