package drivers

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/websocket"
)

// The one-time authorizer-without-verifier warning calls the driver's
// logger, user code: a logger that panics, blocks, or subscribes another
// client itself must not break the subscribe or deadlock it.
func TestWebSocketDriver_VerifierWarningWithHostileLogger(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			fallbacklogtest.Capture(t)
			d := &WebSocketDriver{
				channels:   make(map[string]map[string]*websocket.Client),
				authorizer: denyAllChannelAuthorizer,
			}
			d.SetAuthorizer(func(*websocket.Client, string) bool { return true })
			host := newClientHost(t)
			clients := make([]*websocket.Client, 8)
			for i := range clients {
				clients[i], _ = host.connect(t)
			}
			n := 0
			subscribe := func() {
				n++
				client := clients[n]
				if err := d.handleSubscribe(client, subscribeMsg("private-a", "")); err != nil {
					t.Errorf("subscribe: %v", err)
				}
			}
			code := hostile.New(t, mode, subscribe)
			d.SetLogger(hostile.NewLogger(code, hostile.Warn))

			if mode == hostile.Block {
				go subscribe()
				if !code.AwaitEntered(t) {
					return
				}
			}
			if p := hostile.Within(t, hostile.Deadline, subscribe); p != nil {
				t.Fatalf("subscribe panicked: %v", p)
			}
			code.Release()
			code.Disarm()
			hostile.Within(t, hostile.Deadline, subscribe)
		})
	}
}
