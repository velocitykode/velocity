package drivers

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/websocket"
)

// BenchmarkSendOrDropReal measures one message to one connected client
// (served over an in-memory pipe): dropped on a full queue (to an onDrop
// callback, or as a line), sent in blocking mode to a client whose peer
// keeps reading, and sent in blocking mode by many broadcasters at once
// (16 per GOMAXPROCS) to one such client. The blocking cases measure the
// whole pipeline (JSON, pipe I/O, scheduling), not the enqueue alone, and
// report their drops per op; the parallel one reports aggregate throughput.
func BenchmarkSendOrDropReal(b *testing.B) {
	full := func(b *testing.B) *websocket.Client {
		c, _ := newClientHost(b).full(b)
		return c
	}
	reading := func(b *testing.B) *websocket.Client {
		c, ws := newClientHost(b).connect(b)
		discard(ws)
		return c
	}
	b.Run("dropped/onDrop", func(b *testing.B) {
		d := &WebSocketDriver{onDrop: func(string, string, string) {}}
		c := full(b)
		b.ReportAllocs()
		for b.Loop() {
			d.sendOrDrop(context.Background(), c, "ch", "evt", "data")
		}
	})
	b.Run("dropped/logger", func(b *testing.B) {
		d := &WebSocketDriver{}
		d.SetLogger(discardLogger{})
		c := full(b)
		b.ReportAllocs()
		for b.Loop() {
			d.sendOrDrop(context.Background(), c, "ch", "evt", "data")
		}
	})
	b.Run("blocking-sent", func(b *testing.B) {
		d := &WebSocketDriver{blockingSendTO: time.Minute}
		c := reading(b)
		b.ReportAllocs()
		for b.Loop() {
			d.sendOrDrop(context.Background(), c, "ch", "evt", "data")
		}
		b.ReportMetric(float64(d.DroppedCount())/float64(b.N), "drops/op")
	})
	b.Run("blocking-congested", func(b *testing.B) {
		d := &WebSocketDriver{blockingSendTO: time.Minute}
		c := reading(b)
		b.ReportAllocs()
		b.SetParallelism(16)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				d.sendOrDrop(context.Background(), c, "ch", "evt", "data")
			}
		})
		b.ReportMetric(float64(d.DroppedCount())/float64(b.N), "drops/op")
	})
}
