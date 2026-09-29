package events

import (
	"context"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

// A debounced or coalesced delivery runs after the Dispatch that accepted
// it returned, so no caller receives a listener's failure: each failing
// listener's error or panic is dispatched as its own AsyncFailed (and
// reported through the failure-report bridge), not lost.
func TestDelayedDeliveries_ListenerFailuresBecomeAsyncFailed(t *testing.T) {
	cases := []struct {
		name string
		new  func() (Dispatcher, *DefaultDispatcher, func())
	}{
		{"debouncing", func() (Dispatcher, *DefaultDispatcher, func()) {
			d := NewDebouncingDispatcher(10 * time.Millisecond)
			return d, d.DefaultDispatcher, d.Stop
		}},
		{"coalescing", func() (Dispatcher, *DefaultDispatcher, func()) {
			d := NewCoalescingDispatcher(10 * time.Millisecond)
			return d, d.DefaultDispatcher, d.Stop
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, inner, stop := tc.new()
			defer stop()
			collector := &failureCollector{}
			inner.Listen(&AsyncFailed{}, collector)
			inner.Listen("evt", failingListener{})
			inner.Listen("evt", panickingListener{})

			if err := d.Dispatch(context.Background(), "evt"); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			testsync.Eventually(t, func() bool { return len(collector.snapshot()) == 2 }, 2*time.Second, "one AsyncFailed per failing listener")
			for _, failed := range collector.snapshot() {
				if failed.EventName != "evt" || failed.Err == nil {
					t.Errorf("AsyncFailed = %+v, want the event and the listener's failure", failed)
				}
			}
		})
	}
}
