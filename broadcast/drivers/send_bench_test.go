package drivers

import (
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/websocket"
)

type discardLogger struct{}

func (discardLogger) Debug(string, ...any)          {}
func (discardLogger) Info(string, ...any)           {}
func (discardLogger) Warn(string, ...any)           {}
func (discardLogger) Error(string, ...any)          {}
func (discardLogger) Fatal(string, ...any)          {}
func (l discardLogger) With(...any) contract.Logger { return l }

// BenchmarkSendOrDrop measures one message to one client: delivered into
// its buffer, and dropped on a full buffer (to an onDrop callback, or as a
// line to an installed logger).
func BenchmarkSendOrDrop(b *testing.B) {
	newClient := func(full bool) *websocket.Client {
		c := &websocket.Client{ID: "c1", Send: make(chan websocket.Message, 1)}
		if full {
			c.Send <- websocket.Message{Type: "filler"}
		}
		return c
	}
	b.Run("sent", func(b *testing.B) {
		d := &WebSocketDriver{channels: map[string]map[string]*websocket.Client{}}
		c := newClient(false)
		b.ReportAllocs()
		for b.Loop() {
			d.sendOrDrop(c, "ch", "evt", "data")
			<-c.Send
		}
	})
	b.Run("dropped/onDrop", func(b *testing.B) {
		d := &WebSocketDriver{channels: map[string]map[string]*websocket.Client{},
			onDrop: func(string, string, string) {}}
		c := newClient(true)
		b.ReportAllocs()
		for b.Loop() {
			d.sendOrDrop(c, "ch", "evt", "data")
		}
	})
	b.Run("dropped/logger", func(b *testing.B) {
		d := &WebSocketDriver{channels: map[string]map[string]*websocket.Client{}}
		d.SetLogger(discardLogger{})
		c := newClient(true)
		b.ReportAllocs()
		for b.Loop() {
			d.sendOrDrop(c, "ch", "evt", "data")
		}
	})
}
