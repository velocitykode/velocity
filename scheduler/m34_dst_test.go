package scheduler

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestRunDueJobs_DSTFallBack_FiresOnce verifies M-34's fix for the
// double-fire on fall-back: when the local clock rewinds from 02:00 to
// 01:00, the 01:xx wall minutes recur at a different UTC instant. The
// scheduler's wall-minute dedup must suppress the second dispatch so a
// daily cron at 01:30 fires once, not twice, on the fall-back day.
//
// Driven directly via runDueJobs in a controlled timezone (without
// waiting on the real ticker). We synthesize the duplicate-minute
// situation by calling job.alreadyFiredAt + markFired against a static
// wall minute, then re-evaluating: the second pass must observe the
// mark and skip.
func TestRunDueJobs_DSTFallBack_FiresOnce(t *testing.T) {
	t.Parallel()

	// America/New_York observes DST. Fall-back 2026 is Nov 1 at 02:00
	// local rewinding to 01:00 local. The 01:30 minute occurs twice.
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York TZ data not available: %v", err)
	}

	// First 01:30 EDT (UTC-4)  -> 2026-11-01T05:30:00Z
	// Second 01:30 EST (UTC-5) -> 2026-11-01T06:30:00Z
	firstInstant := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC).In(loc)
	secondInstant := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC).In(loc)

	// Sanity: both should print the same wall-clock minute string in
	// the local zone even though the underlying instants differ.
	if wallMinuteKey(firstInstant) != wallMinuteKey(secondInstant) {
		t.Fatalf("test prerequisite: wall keys must match for both fall-back instants; got %q vs %q",
			wallMinuteKey(firstInstant), wallMinuteKey(secondInstant))
	}
	if firstInstant.Equal(secondInstant) {
		t.Fatalf("test prerequisite: fall-back instants must be distinct, got identical %v", firstInstant)
	}

	j := &Job{schedule: &Schedule{}, timezone: loc}

	// First tick of the day -- has not fired yet.
	if j.alreadyFiredAt(firstInstant) {
		t.Fatal("expected alreadyFiredAt=false on first tick of the day")
	}
	j.markFired(firstInstant)

	// Second occurrence of 01:30 (post fall-back) -- must observe the
	// mark and report already-fired so runDueJobs skips dispatch.
	if !j.alreadyFiredAt(secondInstant) {
		t.Fatal("expected alreadyFiredAt=true on fall-back repeat of 01:30")
	}

	// Next minute (01:31, still inside the repeated hour) must NOT be
	// suppressed -- the dedup is per-minute, not per-hour.
	nextMinute := secondInstant.Add(time.Minute)
	if j.alreadyFiredAt(nextMinute) {
		t.Fatal("dedup must be per-wall-minute, not per-hour; 01:31 should not be gated by 01:30 mark")
	}
}

// TestRunDueJobs_DSTSpringForward_SkipsMinute documents the
// spring-forward contract: when the local clock jumps from 02:00 to
// 03:00, any minute in 02:xx local simply does not occur, so a cron
// targeting 02:30 fires zero times that day. This matches cronie. The
// test does not assert "did not fire" via a counter (the wall minute
// never arrives so there is nothing to count) -- instead it verifies
// the runDueJobs path does not panic when no due job matches, and that
// IsDue on a 02:30 cron yields false for the surrounding 01:59 and
// 03:00 instants.
func TestRunDueJobs_DSTSpringForward_SkipsMinute(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York TZ data not available: %v", err)
	}

	// Spring-forward 2026: Mar 8 at 02:00 EST -> 03:00 EDT. The 02:xx
	// hour does not occur. We evaluate IsDue against the surrounding
	// instants.
	expr, err := ParseExpression("30 2 * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// 01:59 EST (just before the skip)
	before := time.Date(2026, 3, 8, 1, 59, 0, 0, loc)
	if expr.IsDue(before) {
		t.Errorf("02:30 cron must not be due at 01:59 local")
	}
	// 03:00 EDT (just after the skip; the actual UTC instant that
	// would have been 02:00 EST never has a local representation).
	after := time.Date(2026, 3, 8, 3, 0, 0, 0, loc)
	if expr.IsDue(after) {
		t.Errorf("02:30 cron must not be due at 03:00 local")
	}

	// Drive runDueJobs across the gap to confirm no panic.
	var ran atomic.Int32
	s := New()
	s.SetTimezone(loc)
	s.Named("nightly", func() { ran.Add(1) }).Cron("30 2 * * *")

	// Two ticks that straddle the missing 02:30: one at 01:59 (not
	// due) and one at 03:00 (not due). The job should not run either
	// time. We can't directly inject a tick into runDueJobs (it uses
	// time.Now), so we drive it with the real clock; the job's IsDue
	// is the relevant gate and we exercise that above. This sub-test
	// proves runDueJobs is panic-free for a cron whose target minute
	// is unreachable today.
	s.runDueJobs()
	s.runWg.Wait()

	if ran.Load() != 0 {
		// Could only happen if "now" happens to match 02:30 in the
		// scheduler's timezone, but the test environment is unlikely
		// to be in America/New_York DST gap during test execution.
		t.Logf("ran %d times -- only acceptable if running during real DST gap", ran.Load())
	}
}

// TestRunDueJobs_DSTFallBack_DistinctMinutesNotSuppressed proves the
// dedup is keyed on the *minute*, not the hour, so a job that legitimately
// fires every minute in 01:00-01:59 during fall-back gets all repeated
// minutes in the first occurrence of the hour but suppresses each
// individual minute when it recurs. We assert the per-minute key
// computation directly.
func TestRunDueJobs_DSTFallBack_DistinctMinutesNotSuppressed(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York TZ data not available: %v", err)
	}

	j := &Job{schedule: &Schedule{}, timezone: loc}

	// Fire every minute of the first 01:xx hour (EDT, UTC-4 -> UTC base
	// 05:00). Distinct wall-minute keys must NOT alias each other.
	keys := make(map[string]struct{}, 60)
	for m := 0; m < 60; m++ {
		instant := time.Date(2026, 11, 1, 5, m, 0, 0, time.UTC).In(loc)
		if j.alreadyFiredAt(instant) {
			t.Fatalf("minute %d wrongly reported as already-fired", m)
		}
		j.markFired(instant)
		keys[wallMinuteKey(instant)] = struct{}{}
	}
	if len(keys) != 60 {
		t.Fatalf("expected 60 distinct wall-minute keys for the hour, got %d", len(keys))
	}
}
