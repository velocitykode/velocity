package drivers

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewConsoleLoggerTo_WritesToInjectedWriter(t *testing.T) {
	var buf bytes.Buffer
	c := NewConsoleLoggerTo(&buf, 0)

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
	c := NewConsoleLoggerTo(&buf, 2)

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
