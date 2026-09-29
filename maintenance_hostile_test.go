package velocity

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// The one-time marker-path warning calls the middleware's logger, user
// code: a logger that panics, blocks, or resolves the marker path itself
// must not break the maintenance check or deadlock it.
func TestMaintenanceMarkerPath_WarningWithHostileLogger(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			fallbacklogtest.Capture(t)
			useTempMaintRoot(t)
			var logger *hostile.Logger
			resolve := func() {
				if _, err := maintenanceMarkerPath(logger); err != nil {
					t.Errorf("maintenanceMarkerPath: %v", err)
				}
			}
			code := hostile.New(t, mode, func() { _ = isDownForMaintenance() })
			logger = hostile.NewLogger(code, hostile.Warn)

			if mode == hostile.Block {
				go resolve()
				if !code.AwaitEntered(t) {
					return
				}
			}
			if p := hostile.Within(t, hostile.Deadline, resolve); p != nil {
				t.Fatalf("maintenanceMarkerPath panicked: %v", p)
			}
			code.Release()
			code.Disarm()
			hostile.Within(t, hostile.Deadline, resolve)
		})
	}
}
