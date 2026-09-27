package velocity

import (
	"testing"

	"github.com/velocitykode/velocity/app"
)

// A logger the app logger's With returns still forwards each line to
// Services.Log as it stands when the line is written, with the bound pairs
// first.
func TestAppLogger_WithKeepsForwarding(t *testing.T) {
	a := &App{Services: &app.Services{}}
	first, second := newFieldLogger(), newFieldLogger()
	a.Services.Log = first
	bound := appLogger{a: a}.With("request_id", "r1")

	a.Services.Log = second
	bound.Warn("after the swap", "k", "v")

	if got := first.snapshot(); len(got) != 0 {
		t.Errorf("logger replaced before the line got %+v", got)
	}
	lines := second.snapshot()
	if len(lines) != 1 || lines[0].msg != "after the swap" || lines[0].field("request_id") != "r1" || lines[0].field("k") != "v" {
		t.Errorf("current logger got %+v, want the line with request_id=r1 and k=v", lines)
	}
}
