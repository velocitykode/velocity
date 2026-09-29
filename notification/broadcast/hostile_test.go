package broadcast

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// The one-time no-authorizer warning calls the channel's logger, user
// code: a logger that panics, blocks, or sends another notification
// through the channel must not break Send or deadlock it.
func TestBroadcastChannel_NoAuthorizerWarningWithHostileLogger(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			fallbacklogtest.Capture(t)
			ch, _ := newWiredBroadcastChannel(t)
			notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
			n := &multiChannelNotification{channels: []string{"private-anything"}}
			send := func() {
				if err := ch.Send(context.Background(), notifiable, n); err != nil {
					t.Errorf("Send: %v", err)
				}
			}
			code := hostile.New(t, mode, send)
			ch.SetLogger(hostile.NewLogger(code, hostile.Warn))

			if mode == hostile.Block {
				go send()
				<-code.Entered()
			}
			if p := hostile.Within(t, hostile.Deadline, send); p != nil {
				t.Fatalf("Send panicked: %v", p)
			}
			code.Release()
			code.Disarm()
			hostile.Within(t, hostile.Deadline, send)
		})
	}
}
