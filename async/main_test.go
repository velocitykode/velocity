package async

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain wraps the package test suite with goleak's goroutine-leak
// check. Any test that launches a goroutine without ensuring it exits
// before the test returns will fail the whole package. This is the
// async package's primary job, so we hold it to the strictest bar.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("testing.tRunner"),
		goleak.IgnoreTopFunction("testing.(*T).Run"),
	)
}
