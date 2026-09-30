package drain_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/velocitykode/velocity/internal/drain"
)

// Every Await that times out while the stop is stuck used to start
// another force: a gRPC GracefulStop that never ends turned each retried
// Shutdown into one more goroutine. A Coordinator's stop has one force.
// synctest.Wait proves no further force was started: every goroutine in
// the bubble, a started force included, is blocked when it returns.
func TestCoordinator_AwaitStartsOneForce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c drain.Coordinator
		d := c.Begin()
		release := make(chan struct{})
		var forces atomic.Int32
		force := func() {
			forces.Add(1)
			<-release
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for range 5 {
			if err := c.Await(ctx, d, force); !errors.Is(err, context.Canceled) {
				t.Fatalf("Await = %v, want the ctx error", err)
			}
		}
		synctest.Wait()
		if n := forces.Load(); n != 1 {
			t.Errorf("force started %d times for one stop, want 1", n)
		}
		close(release)
		close(d)
	})
}
