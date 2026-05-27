package bus

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain guards against goroutine leaks in the bus package. Event
// dispatch is the likely leak source (a handler that hangs or a
// subscriber that never unsubscribes), so we enforce "every test
// leaves zero goroutines" at the package boundary.
//
// The queue.inMemoryBatchRepository.periodicCleanup ignore covers a
// long-lived background goroutine started from queue's package init().
// Bus imports queue (init() registers the commandJob factory with
// queue.RegisterJob), so the goroutine becomes part of the test
// process. It is owned by queue, not bus, and runs for the lifetime
// of the process by design.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("testing.tRunner"),
		goleak.IgnoreTopFunction("testing.(*T).Run"),
		goleak.IgnoreTopFunction("github.com/velocitykode/velocity/queue.(*inMemoryBatchRepository).periodicCleanup"),
	)
}
