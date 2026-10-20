package stack

import (
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout pointed at a temp file and returns
// what was written. The console driver reads os.Stdout at write time.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	orig := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = orig }()
	fn()
	os.Stdout = orig
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(data)
}

// The stack passes its Config to every child, so "redact": true on the
// stack redacts in its console child; without it the child writes raw.
func TestStackLeaf_RedactReachesChildren(t *testing.T) {
	const card = "4111111111111111"
	for _, tc := range []struct {
		name   string
		redact bool
	}{
		{"redact", true},
		{"no redact", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{"channels": []string{"console", "null"}}
			if tc.redact {
				cfg["redact"] = true
			}
			logger, err := resolveStack(t, cfg)
			if err != nil {
				t.Fatalf("resolve stack: %v", err)
			}
			out := captureStdout(t, func() { logger.Info("card=" + card + " saved") })
			if !strings.Contains(out, "saved") {
				t.Fatalf("console child wrote %q, want the line", out)
			}
			if leaked := strings.Contains(out, card); leaked == tc.redact {
				t.Fatalf("redact=%v: output %q, card present = %v", tc.redact, out, leaked)
			}
			if tc.redact && !strings.Contains(out, "[CARD]") {
				t.Fatalf("output %q, want [CARD]", out)
			}
		})
	}
}
