package queue

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// ConfigureSigningWith writes its warnings through the signing logger,
// user code. For every signing entry point a logger may call back into, a
// logger that panics, blocks, or makes that call must not break the
// configuration or deadlock signing, and signing works afterwards.
func TestConfigureSigning_HostileLogger(t *testing.T) {
	const appKey = "an-application-key-of-thirty-two-bytes!"
	entries := map[string]func(){
		"IsSigningEnabled": func() { _ = IsSigningEnabled() },
		"SetSigningKey":    func() { SetSigningKey([]byte("0123456789abcdef0123456789abcdef")) },
		"SetSigningLogger": func() { SetSigningLogger(nil) },
		"signPayload":      func() { _ = signPayload([]byte("x")) },
		"ConfigureSigning": func() { _ = ConfigureSigning("", appKey) },
	}
	for _, mode := range hostile.Modes() {
		for name, entry := range entries {
			t.Run(mode.String()+"/"+name, func(t *testing.T) {
				saveAndRestoreSigningState(t)
				fallbacklogtest.Capture(t)
				t.Cleanup(func() { SetSigningLogger(nil) })
				code := hostile.New(t, mode, entry)
				SetSigningLogger(hostile.NewLogger(code, hostile.Warn))
				configure := func() {
					if err := ConfigureSigning("", appKey); err != nil {
						t.Errorf("ConfigureSigning: %v", err)
					}
				}
				if mode == hostile.Block {
					go configure()
					<-code.Entered()
					if name != "ConfigureSigning" {
						hostile.Within(t, hostile.Deadline, entry)
					}
				} else if p := hostile.Within(t, hostile.Deadline, configure); p != nil {
					t.Fatalf("ConfigureSigning panicked: %v", p)
				}
				code.Release()
				code.Disarm()
				hostile.Within(t, hostile.Deadline, func() {
					configure()
					if !IsSigningEnabled() || signPayload([]byte("x")) == "" {
						t.Error("signing is not enabled after a retry")
					}
				})
			})
		}
	}
}
