package scheduler_test

import (
	"testing"

	"github.com/velocitykode/velocity/scheduler"
	"github.com/velocitykode/velocity/scheduler/schedulertest"
)

// TestInMemoryLocker_Contract runs the schedulertest spec against the
// in-process scheduler locker.
func TestInMemoryLocker_Contract(t *testing.T) {
	schedulertest.RunLockerContractTests(t, func(t *testing.T) scheduler.Locker {
		return scheduler.NewInMemoryLocker()
	})
}
