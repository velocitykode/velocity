package console

import (
	"testing"
)

func TestScheduleWork_NilScheduler(t *testing.T) {
	err := ScheduleWork(nil)
	if err != nil {
		t.Fatalf("ScheduleWork(nil) returned error: %v", err)
	}
}
