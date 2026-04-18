package bus

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain guards against goroutine leaks in the bus package. Event
// dispatch is the likely leak source (a handler that hangs or a
// subscriber that never unsubscribes), so we enforce "every test
// leaves zero goroutines" at the package boundary.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("testing.tRunner"),
		goleak.IgnoreTopFunction("testing.(*T).Run"),
	)
}
