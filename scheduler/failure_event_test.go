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
// for the failure-report bridge; that an event without it (decoded from
// JSON) falls back to the Error text; that Err never reaches the JSON form;
// and that FailureSource names a scheduled task.
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
	if got := (&ScheduledTaskFailed{Error: "disk full"}).FailureError(); got == nil || got.Error() != "disk full" {
		t.Errorf("FailureError() without Err = %v, want the Error text", got)
	}
	if got := (&ScheduledTaskFailed{}).FailureError(); got != nil {
		t.Errorf("FailureError() of an empty event = %v, want nil", got)
	}
	data, err := json.Marshal(&ScheduledTaskFailed{TaskName: "cleanup", Error: "disk full", Err: errors.New("disk full")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), `"Err"`) {
		t.Errorf("JSON form carries Err: %s", data)
	}
}
