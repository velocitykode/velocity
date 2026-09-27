package drivers

import "testing"

// TestAESDriver_NoDispatcherBuildsNoEvent requires a legacy decrypt to
// build no LegacyDecryptEvent when no event dispatcher is installed.
func TestAESDriver_NoDispatcherBuildsNoEvent(t *testing.T) {
	d, err := NewAESDriver(make([]byte, 32), nil, "AES-256-CBC")
	if err != nil {
		t.Fatal(err)
	}
	d.noteLegacyIfV0(0) // the once-per-driver warning is not an event
	allocs := testing.AllocsPerRun(100, func() { d.noteLegacyIfV0(0) })
	if allocs != 0 {
		t.Errorf("noteLegacyIfV0 allocated %.0f times with no dispatcher, want 0", allocs)
	}
}
