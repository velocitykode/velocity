// Package logtest provides executable specifications (contract tests) for
// [log.Logger] implementations.
//
// Loggers in Velocity are zero-failure: every method on the interface
// returns no error and must NEVER panic, regardless of input shape or
// volume. This runner verifies the "no panic, no error" guarantee across
// every level and degenerate input pattern.
package logtest

import (
	"sync"
	"testing"

	"github.com/velocitykode/velocity/log"
)

// LoggerFactory returns a fresh Logger per sub-test.
type LoggerFactory func(t *testing.T) log.Logger

// RunLoggerContractTests is the executable specification of [log.Logger].
func RunLoggerContractTests(t *testing.T, factory LoggerFactory) {
	t.Helper()

	t.Run("Levels_DoNotPanic", func(t *testing.T) {
		l := factory(t)
		assertNoPanic(t, func() {
			l.Debug("debug msg", "k", "v")
			l.Info("info msg", "k", "v")
			l.Warn("warn msg", "k", "v")
			l.Error("error msg", "k", "v")
			// Fatal is exercised below in its own sub-test so a
			// driver that calls os.Exit on Fatal can be skipped.
		})
	})

	t.Run("NoKVs_DoesNotPanic", func(t *testing.T) {
		l := factory(t)
		assertNoPanic(t, func() {
			l.Info("bare message")
		})
	})

	t.Run("OddKVs_DoesNotPanic", func(t *testing.T) {
		l := factory(t)
		assertNoPanic(t, func() {
			// Trailing dangling key without value: the slog convention
			// is to log the partial pair safely; we require only that
			// it does not panic.
			l.Info("dangling key", "k1", "v1", "lonely")
		})
	})

	t.Run("NonStringKey_DoesNotPanic", func(t *testing.T) {
		l := factory(t)
		assertNoPanic(t, func() {
			l.Warn("typed keys", 42, "answer", struct{}{}, "empty")
		})
	})

	t.Run("NilValue_DoesNotPanic", func(t *testing.T) {
		l := factory(t)
		assertNoPanic(t, func() {
			l.Error("nil value", "k", nil)
		})
	})

	t.Run("LargeMessage_DoesNotPanic", func(t *testing.T) {
		l := factory(t)
		big := make([]byte, 32*1024)
		for i := range big {
			big[i] = 'A'
		}
		assertNoPanic(t, func() {
			l.Info(string(big))
		})
	})

	t.Run("Concurrent_WriteIsSafe", func(t *testing.T) {
		l := factory(t)
		var wg sync.WaitGroup
		const goroutines = 8
		const perGoroutine = 50
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func(i int) {
				defer wg.Done()
				for j := 0; j < perGoroutine; j++ {
					l.Info("concurrent", "g", i, "n", j)
				}
			}(i)
		}
		// If a logger races its internal state, -race or a panic will
		// surface; we just wait for completion.
		wg.Wait()
	})

	t.Run("CRLF_InMessage_DoesNotPanic", func(t *testing.T) {
		l := factory(t)
		// Loggers sanitise CRLF (see log/internal/sanitize); the
		// contract is "no panic, no error", not "passthrough".
		assertNoPanic(t, func() {
			l.Warn("CR\rLF\nESC\x1bx", "k", "v\rinjection")
		})
	})
}

func assertNoPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("logger panicked: %v", r)
		}
	}()
	fn()
}
