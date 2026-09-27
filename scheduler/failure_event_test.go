package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// taskRecordGone is a typed task error.
type taskRecordGone struct{}

func (taskRecordGone) Error() string { return "record gone" }

// TestScheduledTaskFailed_KeepsErrorType asserts the failed-task event
// carries the task's own error, which FailureError returns with its type
// for the failure-report bridge; that the JSON form carries Err as its text
// and decodes back to an event whose FailureError has that text; and that
// FailureSource names a scheduled task.
func TestScheduledTaskFailed_KeepsErrorType(t *testing.T) {
	var captured *ScheduledTaskFailed
	dispatch := func(_ context.Context, event interface{}) {
		if e, ok := event.(*ScheduledTaskFailed); ok {
			captured = e
		}
	}
	cause := &taskRecordGone{}
	dispatchScheduledTaskFailed(dispatch, context.Background(), "cleanup", cause, time.Second)
	if captured == nil {
		t.Fatal("event was not dispatched")
	}
	var gone *taskRecordGone
	if got := captured.FailureError(); !errors.As(got, &gone) || gone != cause {
		t.Errorf("FailureError() = %#v, want the task's own error", got)
	}
	if got := captured.FailureSource(); got != contract.ErrorSourceTask {
		t.Errorf("FailureSource() = %v, want ErrorSourceTask", got)
	}
	if got := (&ScheduledTaskFailed{}).FailureError(); got != nil {
		t.Errorf("FailureError() of an empty event = %v, want nil", got)
	}
	data, err := json.Marshal(&ScheduledTaskFailed{TaskName: "cleanup", Err: errors.New("disk full")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"Err":"disk full"`) {
		t.Errorf("JSON form does not carry Err's text: %s", data)
	}
	var decoded ScheduledTaskFailed
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode the JSON form: %v", err)
	}
	if got := decoded.FailureError(); got == nil || got.Error() != "disk full" {
		t.Errorf("FailureError() of the decoded event = %v, want the Err text", got)
	}
}
