package scheduler

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestM36_ParseExpression_StepZero_ReturnsTypedError verifies that the
// expression parser returns ErrInvalidCronStep instead of panicking on
// "*/0". Pre-fix the parser called makeRange(min, max, 0) which divided
// by zero and crashed the process - a CLAUDE.md rule-10 violation in
// library code (the cron string usually comes from a caller / config).
func TestM36_ParseExpression_StepZero_ReturnsTypedError(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseExpression must NOT panic on */0; got panic: %v", r)
		}
	}()

	_, err := ParseExpression("*/0 * * * *")
	if err == nil {
		t.Fatal("expected error from */0, got nil")
	}
	if !errors.Is(err, ErrInvalidCronStep) {
		t.Fatalf("expected ErrInvalidCronStep, got %v", err)
	}
}

// TestM36_ParseExpression_NegativeStep_ReturnsTypedError exercises the
// other invalid-step boundary. */-1 hits the same makeRange divide-by-
// zero precursor (negative step also makes makeRange loop forever or
// allocate negatively-sized slice). The parser should reject it cleanly.
func TestM36_ParseExpression_NegativeStep_ReturnsTypedError(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseExpression must NOT panic on */-1; got panic: %v", r)
		}
	}()

	// strconv.Atoi accepts "-1", so the step value reaches the guard.
	_, err := ParseExpression("*/-1 * * * *")
	if err == nil {
		t.Fatal("expected error from negative step, got nil")
	}
	if !errors.Is(err, ErrInvalidCronStep) {
		t.Fatalf("expected ErrInvalidCronStep, got %v", err)
	}
}

// TestM36_ParseExpression_ValidStepStillWorks pins that the new step
// guard does not regress legitimate cron expressions.
func TestM36_ParseExpression_ValidStepStillWorks(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{"*/5 * * * *", "*/1 * * * *", "*/30 * * * *"} {
		if _, err := ParseExpression(expr); err != nil {
			t.Errorf("ParseExpression(%q) unexpected error: %v", expr, err)
		}
	}
}

// TestM36_ScheduleDays_Zero_ReturnsTypedError covers Schedule.Days(0):
// pre-fix this silently wrote dayOfMonth="0" which is out of cron's 1-31
// range, so the job's first tick failed at ParseExpression and the job
// never fired - with no signal to the operator. Post-fix the bad value
// is captured as a deferred validation error and the schedule does not
// install it, so the original dayOfMonth value is preserved.
func TestM36_ScheduleDays_Zero_ReturnsTypedError(t *testing.T) {
	t.Parallel()

	s := NewSchedule()
	s.Days(0)

	if err := s.ValidationError(); err == nil {
		t.Fatal("Days(0) must set a deferred validation error")
	} else if !errors.Is(err, ErrInvalidDayOfMonth) {
		t.Fatalf("expected ErrInvalidDayOfMonth, got %v", err)
	}

	// Bad value must NOT have been written to the cron field.
	if s.dayOfMonth == "0" {
		t.Errorf("Days(0) must not write the invalid value to dayOfMonth; got %q", s.dayOfMonth)
	}
}

// TestM36_ScheduleDays_OutOfRange_ReturnsTypedError covers the upper
// bound (32+) and other invalid inputs.
func TestM36_ScheduleDays_OutOfRange_ReturnsTypedError(t *testing.T) {
	t.Parallel()

	cases := []int{0, -1, 32, 99, -5}
	for _, day := range cases {
		s := NewSchedule()
		s.Days(day)
		err := s.ValidationError()
		if err == nil {
			t.Errorf("Days(%d) must set ErrInvalidDayOfMonth, got nil", day)
			continue
		}
		if !errors.Is(err, ErrInvalidDayOfMonth) {
			t.Errorf("Days(%d): expected ErrInvalidDayOfMonth, got %v", day, err)
		}
	}
}

// TestM36_ScheduleDays_ValidRange covers the documented contract:
// day-of-month 1-31 inclusive.
func TestM36_ScheduleDays_ValidRange(t *testing.T) {
	t.Parallel()

	for _, day := range []int{1, 15, 31} {
		s := NewSchedule()
		s.Days(day)
		if err := s.ValidationError(); err != nil {
			t.Errorf("Days(%d) unexpected validation error: %v", day, err)
		}
	}
}

// TestM36_ScheduleCron_InvalidExpression_DeferredError verifies that
// Schedule.Cron("*/0 * * * *") captures the parse error rather than
// silently letting the bad expression reach the first-tick evaluator.
func TestM36_ScheduleCron_InvalidExpression_DeferredError(t *testing.T) {
	t.Parallel()

	s := NewSchedule()
	s.Cron("*/0 * * * *")
	err := s.ValidationError()
	if err == nil {
		t.Fatal("Cron(*/0 ...) must set a deferred validation error")
	}
	if !errors.Is(err, ErrInvalidCronStep) {
		t.Fatalf("expected ErrInvalidCronStep wrapped in schedule error, got %v", err)
	}
}

// TestM36_InvalidJob_DoesNotFire confirms the IsDue gate: a job with
// a deferred validation error must NEVER fire, regardless of the
// captured cron field values. Combined with the ValidateJobs error
// log, the operator sees a loud "this job will never fire" message
// instead of a silent no-op.
func TestM36_InvalidJob_DoesNotFire(t *testing.T) {
	t.Parallel()

	s := New()
	var ran atomic.Int32
	// Days(0) leaves dayOfMonth="*"; without the IsDue gate the job
	// would fire every minute. With the gate it never fires.
	s.Named("invalid.job", func() { ran.Add(1) }).Days(0)

	s.runDueJobs()
	s.runWg.Wait()
	time.Sleep(20 * time.Millisecond)

	if ran.Load() != 0 {
		t.Fatalf("invalid job must not fire; got %d executions", ran.Load())
	}
}

// TestM36_ValidateJobs_LogsValidationError verifies the audit-time
// signal: ValidateJobs must emit an Error log line for any job whose
// schedule carries a deferred validation error. The log line lists the
// job name and the error, so operators see the misconfiguration at
// boot.
func TestM36_ValidateJobs_LogsValidationError(t *testing.T) {
	t.Parallel()

	log := &captureLogger{}

	s := New()
	s.SetLogger(log)
	s.Named("bad.cron", func() {}).Cron("*/0 * * * *")

	s.ValidateJobs()

	errs := log.errors()
	if len(errs) == 0 {
		t.Fatal("ValidateJobs must log an Error for invalid schedule")
	}
	found := false
	for _, msg := range errs {
		if strings.Contains(msg, "invalid schedule configuration") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'invalid schedule configuration' error log; got %v", errs)
	}
}
