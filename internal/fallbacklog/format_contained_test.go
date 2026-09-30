package fallbacklog_test

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A key or value whose Error, String or Format method panics, a nested
// panic included, is written as errchain.Unreadable (quoted, as it holds
// spaces): the line is kept, where a nested panic used to lose it.
func TestLogger_UnformattableValues(t *testing.T) {
	quoted := `"` + errchain.Unreadable + `"`
	for name, v := range hostile.Unformattables() {
		t.Run(name, func(t *testing.T) {
			out := fallbacklogtest.Capture(t)
			if p := hostile.Within(t, hostile.Deadline, func() {
				fallbacklog.Write(nil, func(l contract.Logger) { l.Warn("value line", "v", v) })
				fallbacklog.Logger{}.Warn("key line", v, "x")
				fallbacklog.Logger{}.With("bound", v).Error("bound line")
			}); p != nil {
				t.Fatalf("a panic escaped: %v", p)
			}
			s := out.String()
			for _, want := range []string{"value line v=" + quoted, "key line " + quoted + "=x", "bound line bound=" + quoted} {
				if !strings.Contains(s, want) {
					t.Errorf("output lacks %q:\n%s", want, s)
				}
			}
		})
	}
}
