package velocity

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
)

// A module replacing Services.Log moves the built-in error handler and its
// log reporter to the replacement at the next boundary: a reported error
// and the handler's own notices reach the new logger, none the original.
func TestNew_ErrorPipelineFollowsASwappedLogger(t *testing.T) {
	swapped := &levelLogger{}
	a, original := newLoggerWiringApp(t, nil, WithModules(loggerSwapModule{probe: &loggerProbe{}, swapped: swapped}))
	original.reset()

	a.Services.Errors.Report(errors.New("background broke"), &contract.ErrorContext{Source: contract.ErrorSourceGoroutine})
	if n := entriesStartingWith(swapped, "error", "background broke"); n != 1 {
		t.Errorf("swapped logger report lines = %d, want 1", n)
	}
	if n := entriesStartingWith(original, "error", "background broke"); n != 0 {
		t.Errorf("original logger report lines = %d, want 0", n)
	}

	h, ok := a.Services.Errors.(*problem.Handler)
	if !ok {
		t.Fatalf("Services.Errors = %T, want *problem.Handler", a.Services.Errors)
	}
	h.SetDebug(true)
	if n := entriesStartingWith(swapped, "warn", "error handler debug mode enabled"); n != 1 {
		t.Errorf("swapped logger debug notices = %d, want 1", n)
	}
	if n := entriesStartingWith(original, "warn", "error handler debug mode enabled"); n != 0 {
		t.Errorf("original logger debug notices = %d, want 0", n)
	}
}
