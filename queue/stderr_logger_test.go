package queue

import (
	"bytes"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// The worker's stderr fallback is a contract.Logger: every level writes
// one labelled line, and Fatal writes at error level and returns.
func TestStderrLogger_WritesEveryLevel(t *testing.T) {
	var buf bytes.Buffer
	orig := stderrFallbackWriter()
	stderrFallback.Store(stderrWriter{Writer: &buf})
	t.Cleanup(func() { stderrFallback.Store(stderrWriter{Writer: orig}) })

	var l contract.Logger = stderrLogger{}
	l.Debug("debug line", "k", 1)
	l.Info("info line")
	l.Warn("warn line")
	l.Error("error line")
	l.Fatal("fatal line")

	want := "velocity/queue [DEBUG] debug line k=1\n" +
		"velocity/queue [INFO] info line\n" +
		"velocity/queue [WARN] warn line\n" +
		"velocity/queue [ERROR] error line\n" +
		"velocity/queue [ERROR] fatal line\n"
	if got := buf.String(); got != want {
		t.Errorf("stderr fallback wrote %q, want %q", got, want)
	}
}
