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

// The authorizer and the presence-data func are user code. For every
// manager entry point they may call back into, one that panics, blocks,
// or makes that call must not deadlock Auth, and Auth works afterwards.
func TestBroadcastManager_AuthWithHostileCallbacks(t *testing.T) {
	entries := map[string]func(b *BroadcastManager){
		"SetAuthorizer":   func(b *BroadcastManager) { b.SetAuthorizer(func(string, interface{}) bool { return true }) },
		"SetPresenceData": func(b *BroadcastManager) { b.SetPresenceData(func(string, interface{}) interface{} { return "d" }) },
		"SetAuthSecret":   func(b *BroadcastManager) { b.SetAuthSecret([]byte("0123456789abcdef0123456789abcdef")) },
		"SetLogger":       func(b *BroadcastManager) { b.SetLogger(nil) },
		"Auth":            func(b *BroadcastManager) { _, _ = b.Auth("public-x", "1.1", nil) },
	}
	for _, callback := range []string{"authorizer", "presence"} {
		for _, mode := range hostile.Modes() {
			for name, entry := range entries {
				t.Run(callback+"/"+mode.String()+"/"+name, func(t *testing.T) {
					fallbacklogtest.Capture(t)
					b := New(NewMockDriver())
					code := hostile.New(t, mode, func() { entry(b) })
					if callback == "authorizer" {
						b.SetAuthorizer(func(string, interface{}) bool { code.Run(); return true })
					} else {
						b.SetAuthorizer(func(string, interface{}) bool { return true })
						b.SetPresenceData(func(string, interface{}) interface{} { code.Run(); return "d" })
					}
					auth := func() { _, _ = b.Auth("presence-room", "1.1", "user") }
					if mode == hostile.Block {
						go auth()
						<-code.Entered()
						hostile.Within(t, hostile.Deadline, func() { entry(b) })
					} else {
						hostile.Within(t, hostile.Deadline, auth)
					}
					code.Release()
					code.Disarm()
					hostile.Within(t, hostile.Deadline, func() {
						if _, err := b.Auth("presence-room", "1.1", "user"); err != nil {
							t.Errorf("Auth after a retry: %v", err)
						}
					})
				})
			}
		}
	}
}
