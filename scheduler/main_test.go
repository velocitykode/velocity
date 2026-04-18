package scheduler

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces zero goroutine leaks for the scheduler package.
// Tickers and cron loops are the usual culprits; any test that creates
// a scheduler must ensure Stop is called before returning.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("testing.tRunner"),
		goleak.IgnoreTopFunction("testing.(*T).Run"),
	)
}
