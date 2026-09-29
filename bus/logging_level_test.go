package bus

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/log/drivers"
)

// lockedBuffer is a bytes.Buffer safe for the logger's concurrent writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// With the log level at warn (LOG_LEVEL=warn), a command that fails still
// leaves a line naming it and its error, while the dispatch and completion
// lines stay below the level.
func TestLoggingMiddleware_FailedCommandShowsAtWarnLevel(t *testing.T) {
	out := &lockedBuffer{}
	logger := drivers.NewConsoleLoggerTo(out, log.ExtractLevel(map[string]any{"level": "warn"}))

	b := New()
	b.Through(LoggingMiddleware(logger))
	Register(b, func(createUser) error { return errors.New("mailbox full") })
	Register(b, func(deleteUser) error { return nil })

	if err := b.Dispatch(createUser{Name: "Test"}); err == nil {
		t.Fatal("Dispatch = nil, want the handler's error")
	}
	if err := b.Dispatch(deleteUser{ID: 1}); err != nil {
		t.Fatalf("Dispatch = %v, want nil", err)
	}

	got := out.String()
	if !strings.Contains(got, "Command failed") || !strings.Contains(got, "mailbox full") || !strings.Contains(got, "createUser") {
		t.Errorf("output at warn level = %q, want the failed command's line with its type and error", got)
	}
	if strings.Contains(got, "Dispatching command") || strings.Contains(got, "Command completed") {
		t.Errorf("output at warn level = %q, want no dispatch or completion lines", got)
	}
}

// A logger that panics never fails the command it describes: the handler
// runs and the dispatch returns the handler's result.
func TestLoggingMiddleware_PanickingLoggerDoesNotFailTheCommand(t *testing.T) {
	b := New()
	b.Through(LoggingMiddleware(hostile.NewLogger(hostile.New(t, hostile.Panic, nil))))
	ran := 0
	Register(b, func(createUser) error { ran++; return nil })
	Register(b, func(deleteUser) error { ran++; return errors.New("mailbox full") })

	if err := b.Dispatch(createUser{Name: "Test"}); err != nil {
		t.Errorf("Dispatch = %v, want nil", err)
	}
	if err := b.Dispatch(deleteUser{ID: 1}); err == nil || err.Error() != "mailbox full" {
		t.Errorf("Dispatch = %v, want the handler's error", err)
	}
	if ran != 2 {
		t.Errorf("handlers ran %d times, want 2", ran)
	}
}
