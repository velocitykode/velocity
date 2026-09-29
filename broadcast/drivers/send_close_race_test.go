package drivers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	gorillaws "github.com/gorilla/websocket"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/websocket"
)

// dialSubscribed connects a client to the driver's server, subscribes it to
// channel and returns once the subscribe is acknowledged.
func dialSubscribed(t *testing.T, ts *httptest.Server, channel string) *gorillaws.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := gorillaws.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var msg websocket.Message
	if err := ws.ReadJSON(&msg); err != nil || msg.Type != "welcome" {
		t.Fatalf("read welcome: %v %q", err, msg.Type)
	}
	if err := ws.WriteJSON(websocket.Message{Type: "subscribe", Data: map[string]interface{}{"channel": channel}}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	for {
		if err := ws.ReadJSON(&msg); err != nil {
			t.Fatalf("read subscribe ack: %v", err)
		}
		if msg.Type != "evt" {
			return ws
		}
	}
}

// A broadcast that snapshotted a client before it disconnected sends to it
// while the server closes its send queue. The send and the close are
// ordered, so the loop is clean under -race, and no send panics: the
// client reports itself gone and the driver drops the message. Run under
// -race.
func TestBroadcast_SendRacingDisconnect(t *testing.T) {
	d := newRealDriver(t)
	t.Cleanup(func() { _ = d.server.Shutdown(context.Background()) })
	ts := httptest.NewServer(http.HandlerFunc(d.server.HandleConnection))
	t.Cleanup(ts.Close)

	for i := range 200 {
		ws := dialSubscribed(t, ts, "room")
		var stop atomic.Bool
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if err := d.Broadcast([]string{"room"}, "evt", i); err != nil {
					t.Errorf("Broadcast: %v", err)
					return
				}
			}
		}()
		_ = ws.Close()
		hostile.Eventually(t, hostile.Deadline, "the disconnected client purged", func() bool {
			return !driverHasSubscribers(d, "room")
		})
		stop.Store(true)
		wg.Wait()
	}
}
