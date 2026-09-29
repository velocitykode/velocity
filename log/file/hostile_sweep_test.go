package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// fileEntries are the file logger's entry points that format a value.
var fileEntries = map[string]func(l contract.Logger, v any){
	"Debug": func(l contract.Logger, v any) { l.Debug("line", "v", v) },
	"Info":  func(l contract.Logger, v any) { l.Info("line", "v", v) },
	"Warn":  func(l contract.Logger, v any) { l.Warn("line", "v", v) },
	"Error": func(l contract.Logger, v any) { l.Error("line", "v", v) },
	"Fatal": func(l contract.Logger, v any) { l.Fatal("line", "v", v) },
	"With":  func(l contract.Logger, v any) { l.With("bound", v).Info("line") },
}

// Every entry point that formats a value, against a value whose Error or
// String panics, blocks, or logs through the same logger: no panic
// escapes, a blocked value holds up no other writer nor Shutdown, and once
// the value behaves the line is written.
func TestFileLogger_HostileValueSweep(t *testing.T) {
	for _, mode := range hostile.Modes() {
		for name, call := range fileEntries {
			t.Run(mode.String()+"/"+name, func(t *testing.T) {
				dir := t.TempDir()
				l := NewFileLogger(dir, 0, contract.LogLevelDebug)
				code := hostile.New(t, mode, func() { l.Warn("reentered line") })
				v := hostile.NewValue(code, "hostile-value")

				if mode == hostile.Block {
					go call(l, v)
					<-code.Entered()
					hostile.Within(t, hostile.Deadline, func() { l.Info("other writer") })
				} else if p := hostile.Within(t, hostile.Deadline, func() { call(l, v) }); p != nil {
					t.Fatalf("a panic escaped the log call: %v", p)
				}

				code.Disarm()
				code.Release()
				hostile.Within(t, hostile.Deadline, func() { call(l, v) })
				hostile.Within(t, hostile.Deadline, func() { _ = l.Shutdown(context.Background()) })
				if got := fileText(t, dir); !strings.Contains(got, "hostile-value") {
					t.Errorf("file %q lacks the line written once the value behaved", got)
				}
			})
		}
	}
}

// fileText returns the one log file under dir.
func fileText(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("log dir entries = %v, %v; want one file", entries, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
