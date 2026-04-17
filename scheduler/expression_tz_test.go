package scheduler

import (
	"testing"
	"time"
)

// TestIsDue_TimezoneFromCaller verifies Task 7d's contract: the expression
// evaluator honours whatever location the caller attached to t via time.In,
// and does not silently re-anchor to time.Local. This locks in the DST /
// leap-second commentary against accidental future regressions.
func TestIsDue_TimezoneFromCaller(t *testing.T) {
	// Daily at 09:00 (cron "0 9 * * *").
	expr, err := ParseExpression("0 9 * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Same instant, two different observers:
	//   - In UTC it reads 14:00 → not due.
	//   - In UTC-5 it reads 09:00 → due.
	instant := time.Date(2026, 4, 17, 14, 0, 0, 0, time.UTC)

	if expr.IsDue(instant.In(time.UTC)) {
		t.Error("expected not due at 14:00 UTC")
	}

	utcMinus5 := time.FixedZone("UTC-5", -5*3600)
	if !expr.IsDue(instant.In(utcMinus5)) {
		t.Error("expected due at 09:00 in UTC-5")
	}

	// Sanity: the underlying instant is identical — time.In shifts the
	// *representation*, not the moment — so the two calls above prove IsDue
	// is reading t.Hour() off the caller-supplied location.
	if !instant.Equal(instant.In(utcMinus5)) {
		t.Fatal("time.In changed the instant — environment bug")
	}
}
