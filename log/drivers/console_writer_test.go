package drivers

import (
	"bytes"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

func TestNewConsoleLoggerTo_WritesToInjectedWriter(t *testing.T) {
	var buf bytes.Buffer
	c := NewConsoleLoggerTo(&buf, contract.LogLevelDebug)

	c.Debug("dbg")
	c.Info("inf")
	c.Warn("wrn")
	c.Error("err")

	out := buf.String()
	for _, want := range []string{"DEBUG: dbg", "INFO: inf", "WARN: wrn", "ERROR: err"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestNewConsoleLoggerTo_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	c := NewConsoleLoggerTo(&buf, contract.LogLevelWarn)

	c.Debug("dbg")
	c.Info("inf")
	c.Warn("wrn")

	out := buf.String()
	if strings.Contains(out, "dbg") || strings.Contains(out, "inf") {
		t.Errorf("below-level messages leaked:\n%s", out)
	}
	if !strings.Contains(out, "wrn") {
		t.Errorf("missing warn message:\n%s", out)
	}
}

// A logger With returned writes its bound pairs before each line's own,
// to the same writer at the same level, and leaves its parent unchanged.
func TestConsoleLogger_With(t *testing.T) {
	var buf bytes.Buffer
	parent := NewConsoleLoggerTo(&buf, contract.LogLevelInfo)
	child := parent.With("request_id", "r1").With("job_id", "j1", "dangling")

	child.Debug("dropped")
	child.Info("bound line", "k", "v")
	parent.Info("parent line", "k", "v")

	out := buf.String()
	if strings.Contains(out, "dropped") {
		t.Errorf("below-level line written:\n%s", out)
	}
	if !strings.Contains(out, "bound line | request_id=r1 job_id=j1 k=v") {
		t.Errorf("bound line missing its pairs:\n%s", out)
	}
	if strings.Contains(out, "dangling") {
		t.Errorf("dangling key written:\n%s", out)
	}
	if !strings.Contains(out, "parent line | k=v") || strings.Contains(out, "parent line | request_id") {
		t.Errorf("parent changed by With:\n%s", out)
	}
}
