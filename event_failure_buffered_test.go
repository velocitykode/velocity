package velocity

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/events"
)

// bufferedEvent is an app event recorded in a transaction's buffer.
type bufferedEvent struct{}

func (bufferedEvent) Name() string { return "app.buffered" }

// An event recorded in a transaction's buffer is dispatched when the
// transaction commits; its listeners failing is a failed delivery the
// app's policy records once, and the transaction still returns it. Two
// failing listeners on one event are one delivery. With two failing
// events, the flush stops at the first and keeps the rest for a retry, so
// that is one failed delivery too.
func TestBufferedTransactionEventFailures_ReachTheFailurePolicyOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events int
	}{
		{"one event, two failing listeners", 1},
		{"two failing events", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rec hookRecorder
			a, _ := newLoggerWiringApp(t, func(c *Config) {
				c.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
			}, WithFailedEventHook(rec.hook))
			a.Services.Events.Listen("app.buffered", failingListener{name: "app.buffered"})
			a.Services.Events.Listen("app.buffered", failingListener{name: "app.buffered"})

			ctx := events.PrepareBuffer(context.Background())
			err := a.DB.Transaction(ctx, func(ctx context.Context) error {
				for range tc.events {
					if err := events.Buffer(ctx).Dispatch(ctx, bufferedEvent{}); err != nil {
						return err
					}
				}
				return nil
			})
			if err == nil {
				t.Fatal("transaction returned nil, want the buffered listeners' failure")
			}
			if got := a.FailedEventCount(); got != 1 {
				t.Errorf("FailedEventCount = %d, want 1", got)
			}
			if got := hookCalls(&rec); got != 1 {
				t.Errorf("hook calls = %d, want 1", got)
			}
		})
	}
}
