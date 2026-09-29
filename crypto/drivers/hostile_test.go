package drivers

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// The one-time legacy warning calls the driver's logger, which is user
// code: a logger that panics, blocks, or decrypts another legacy value
// through the same driver must not break decryption or deadlock it.
func TestAESDriver_LegacyWarningWithHostileLogger(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			fallbacklogtest.Capture(t)
			d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
			if err != nil {
				t.Fatalf("NewAESDriver: %v", err)
			}
			legacy := handCraftLegacyCBC(t, d, []byte("old"))
			decrypt := func() {
				if got, err := d.Decrypt(legacy); err != nil || got != "old" {
					t.Errorf("Decrypt = %q, %v; want old", got, err)
				}
			}
			code := hostile.New(t, mode, decrypt)
			d.SetLogger(hostile.NewLogger(code, hostile.Warn))

			if mode == hostile.Block {
				go decrypt()
				if !code.AwaitEntered(t) {
					return
				}
			}
			if p := hostile.Within(t, hostile.Deadline, decrypt); p != nil {
				t.Fatalf("Decrypt panicked: %v", p)
			}
			code.Release()
			code.Disarm()
			hostile.Within(t, hostile.Deadline, decrypt)
		})
	}
}
