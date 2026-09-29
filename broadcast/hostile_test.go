package broadcast

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// The one-time authorizer-without-secret warning calls the manager's
// logger, user code: a logger that panics, blocks, or installs an
// authorizer itself must not break SetAuthorizer or deadlock it.
func TestBroadcastManager_SecretWarningWithHostileLogger(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			fallbacklogtest.Capture(t)
			b := New(NewMockDriver())
			allow := func(string, interface{}) bool { return true }
			set := func() { b.SetAuthorizer(allow) }
			code := hostile.New(t, mode, set)
			b.SetLogger(hostile.NewLogger(code, hostile.Warn))

			if mode == hostile.Block {
				go set()
				<-code.Entered()
			}
			if p := hostile.Within(t, hostile.Deadline, set); p != nil {
				t.Fatalf("SetAuthorizer panicked: %v", p)
			}
			code.Release()
			code.Disarm()
			hostile.Within(t, hostile.Deadline, func() {
				set()
				if _, err := b.Auth("private-x", "1.1", nil); err != nil {
					t.Errorf("Auth after the warning: %v", err)
				}
			})
		})
	}
}
