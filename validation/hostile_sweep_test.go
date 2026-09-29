package validation

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/validation/internal/dbcheck"
)

// runForCaller runs call against user code the validator calls
// synchronously for its caller: a panic reaches that caller as the user
// code's own, a block holds up only the caller while other runs, re-entry
// returns. The code is then disarmed and released for the retry.
func runForCaller(t *testing.T, mode hostile.Mode, code *hostile.Code, call, other func()) {
	t.Helper()
	switch mode {
	case hostile.Block:
		go call()
		<-code.Entered()
		hostile.Within(t, hostile.Deadline, other)
	case hostile.Panic:
		if p := hostile.Within(t, hostile.Deadline, call); p != hostile.PanicValue {
			t.Fatalf("panic = %v, want the user code's own to reach the caller", p)
		}
	default:
		if p := hostile.Within(t, hostile.Deadline, call); p != nil {
			t.Fatalf("a panic escaped: %v", p)
		}
	}
	code.Disarm()
	code.Release()
}

// A validator runs rule handlers (a custom rule, and a database rule whose
// failure line goes through a hostile logger) under none of its locks: a
// handler that panics leaves the validator usable, one that blocks holds
// up no other validation nor SetMessages, and one that calls back into the
// validator returns.
func TestValidator_HostileHandlerSweep(t *testing.T) {
	type entry struct {
		name string
		rule func(code *hostile.Code) Rule
	}
	entries := []entry{
		{"custom", func(code *hostile.Code) Rule {
			return Custom("hostile", func(string, interface{}, []string, map[string]interface{}) error {
				code.Run()
				return nil
			})
		}},
		{"database rule logger", func(code *hostile.Code) Rule {
			failing := func(string, ...any) (int64, error) { return 0, errors.New("db down") }
			unique := dbcheck.UniqueRule("sqlite", failing, hostile.NewLogger(code))
			return Custom("hostile", func(field string, value interface{}, _ []string, data map[string]interface{}) error {
				return unique(field, value, []string{"users"}, data)
			})
		}},
	}
	for _, mode := range hostile.Modes() {
		for _, e := range entries {
			for _, via := range []string{"Validate", "ValidateValue"} {
				t.Run(mode.String()+"/"+e.name+"/"+via, func(t *testing.T) {
					v := NewValidator()
					code := hostile.New(t, mode, func() {
						v.SetMessages(Messages{{Field: "x", Rule: "required"}: "x needed"})
						_, _ = v.Validate(map[string]any{"x": "y"}, Rules{"x": {Required()}})
					})
					rule := e.rule(code)
					call := func() { _, _ = v.Validate(map[string]any{"name": "n"}, Rules{"name": {rule}}) }
					if via == "ValidateValue" {
						call = func() { _ = v.ValidateValue("n", rule) }
					}
					other := func() {
						v.SetMessages(Messages{})
						if _, err := v.Validate(map[string]any{"x": "y"}, Rules{"x": {Required()}}); err != nil {
							t.Errorf("other validation: %v", err)
						}
					}
					runForCaller(t, mode, code, call, other)
					if p := hostile.Within(t, hostile.Deadline, call); p != nil {
						t.Fatalf("retry panicked: %v", p)
					}
				})
			}
		}
	}
}

// sweepClassifierCode is the code the sweep's registered classifier runs;
// nil runs nothing. The registry is process-wide and append-only.
var sweepClassifierCode atomic.Pointer[hostile.Code]

// A unique-violation classifier runs outside the registry lock: one that
// panics leaves the registry usable, one that blocks holds up no
// registration, and one that registers another returns.
func TestClassifyUniqueViolation_HostileClassifierSweep(t *testing.T) {
	RegisterUniqueViolationClassifier(func(error) (string, bool, bool) {
		sweepClassifierCode.Load().Run()
		return "", false, false
	})
	t.Cleanup(func() { sweepClassifierCode.Store(nil) })
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			code := hostile.New(t, mode, func() {
				RegisterUniqueViolationClassifier(func(error) (string, bool, bool) { return "", false, false })
			})
			sweepClassifierCode.Store(code)
			call := func() { ClassifyUniqueViolation(errors.New("not a violation")) }
			other := func() {
				RegisterUniqueViolationClassifier(func(error) (string, bool, bool) { return "", false, false })
			}
			runForCaller(t, mode, code, call, other)
			if p := hostile.Within(t, hostile.Deadline, call); p != nil {
				t.Fatalf("retry panicked: %v", p)
			}
		})
	}
}
