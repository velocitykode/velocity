package websocket

import (
	"sync"
	"sync/atomic"
	"testing"
)

// BenchmarkClientSend measures one enqueue onto a client's send queue:
// SendMessage into a queue with room (the loop receives each message back)
// and into a full one, and many goroutines (16 per GOMAXPROCS) attempting
// to enqueue at once while one reader drains: that case counts attempts,
// full or not, and reports the fraction enqueued as sent/op.
func BenchmarkClientSend(b *testing.B) {
	msg := Message{Type: "evt", Data: "data"}
	b.Run("sent", func(b *testing.B) {
		c := &Client{ID: "c1", send: make(chan Message, 1)}
		b.ReportAllocs()
		for b.Loop() {
			_ = c.SendMessage(msg)
			<-c.send
		}
	})
	b.Run("full", func(b *testing.B) {
		c := &Client{ID: "c1", send: make(chan Message, 1)}
		c.send <- msg
		b.ReportAllocs()
		for b.Loop() {
			_ = c.SendMessage(msg)
		}
	})
	b.Run("parallel", func(b *testing.B) {
		c := &Client{ID: "c1", send: make(chan Message, 256)}
		var wg sync.WaitGroup
		wg.Add(1)
		stop := make(chan struct{})
		go func() {
			defer wg.Done()
			for {
				select {
				case <-c.send:
				case <-stop:
					return
				}
			}
		}()
		var sent atomic.Int64
		b.ReportAllocs()
		b.SetParallelism(16)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if c.SendMessage(msg) == nil {
					sent.Add(1)
				}
			}
		})
		close(stop)
		wg.Wait()
		b.ReportMetric(float64(sent.Load())/float64(b.N), "sent/op")
	})
}
