package grpc_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces zero goroutine leaks for the grpc package. The
// StartAsync path spawns a Serve goroutine and background panic
// recovery; any test that starts a server must call Shutdown before
// returning. Integration tests already do this via t.Cleanup.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("testing.tRunner"),
		goleak.IgnoreTopFunction("testing.(*T).Run"),
	)
}
