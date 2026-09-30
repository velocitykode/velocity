package file

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A key or value whose Error, String or Format method panics, a nested
// panic included, is written as errchain.Unreadable: no panic leaves the
// log call and the line is kept.
func TestFileLogger_UnformattableValues(t *testing.T) {
	for name, v := range hostile.Unformattables() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			l := NewFileLogger(dir, 0, contract.LogLevelDebug)
			t.Cleanup(func() { _ = l.Shutdown(t.Context()) })
			if p := hostile.Within(t, hostile.Deadline, func() {
				l.Info("value line", "v", v)
				l.Info("key line", v, "x")
				l.With("bound", v).Warn("bound line")
			}); p != nil {
				t.Fatalf("a panic escaped: %v", p)
			}
			out := fileText(t, dir)
			for _, want := range []string{"v=" + errchain.Unreadable, errchain.Unreadable + "=x", "bound=" + errchain.Unreadable} {
				if !strings.Contains(out, want) {
					t.Errorf("file lacks %q:\n%s", want, out)
				}
			}
		})
	}
}
