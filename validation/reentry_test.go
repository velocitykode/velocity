package validation

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// within fails the test when fn does not return before the deadline.
func within(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("call did not return: user code ran under a validation lock")
	}
}

// A rule handler is user code and may call back into the validator that
// runs it: SetMessages and a nested validation both return, on Validate
// and ValidateValue alike, and the new messages apply to the next
// validation.
func TestValidator_HandlerReentersValidator(t *testing.T) {
	v := NewValidator()
	failing := func(field string, value interface{}, params []string, data map[string]interface{}) error {
		return errors.New("default message")
	}
	reenter := Custom("reenter", func(field string, value interface{}, params []string, data map[string]interface{}) error {
		v.SetMessages(Messages{{Field: "value", Rule: "fails"}: "custom message"})
		_, _ = v.Validate(map[string]any{"x": "y"}, Rules{"x": {Required()}})
		return nil
	})
	within(t, func() {
		if _, err := v.Validate(map[string]any{"name": "n"}, Rules{"name": {reenter}}); err != nil {
			t.Errorf("Validate: %v", err)
		}
	})
	within(t, func() {
		if err := v.ValidateValue("n", reenter); err != nil {
			t.Errorf("ValidateValue: %v", err)
		}
	})
	err := v.ValidateValue("n", Custom("fails", failing))
	if err == nil || err.Error() != "custom message" {
		t.Errorf("next validation error = %v, want the message SetMessages installed", err)
	}
}

// Validations racing SetMessages see either the old or the new messages,
// never a torn set, and nothing races.
func TestValidator_SetMessagesRacesValidate(t *testing.T) {
	v := NewValidator()
	fails := Custom("fails", func(string, interface{}, []string, map[string]interface{}) error {
		return errors.New("default")
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				v.SetMessages(Messages{{Field: "value", Rule: "fails"}: "a"})
				v.SetMessages(Messages{{Field: "value", Rule: "fails"}: "b"})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				err := v.ValidateValue("x", fails)
				if msg := err.Error(); msg != "a" && msg != "b" && msg != "default" {
					t.Errorf("message = %q", msg)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// A unique-violation classifier runs outside the registry lock, so one
// that registers another classifier returns.
func TestClassifyUniqueViolation_ClassifierReentersRegistry(t *testing.T) {
	var once sync.Once
	RegisterUniqueViolationClassifier(func(err error) (string, bool, bool) {
		once.Do(func() {
			RegisterUniqueViolationClassifier(func(error) (string, bool, bool) { return "", false, false })
		})
		return "", false, false
	})
	within(t, func() { ClassifyUniqueViolation(errors.New("not a violation")) })
}
