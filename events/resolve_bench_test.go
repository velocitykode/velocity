package events

import (
	"context"
	"strconv"
	"testing"
)

type benchTypedEvent struct{}

func (benchTypedEvent) Name() string { return "bench.typed" }

// BenchmarkDispatch_Resolve measures a synchronous dispatch to one
// succeeding listener, by how the event's listeners are registered.
func BenchmarkDispatch_Resolve(b *testing.B) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(d *DefaultDispatcher)
		event interface{}
	}{
		{"exact", func(d *DefaultDispatcher) {
			d.Listen("user.created", &cacheCountListener{})
			d.Listen("order.*", &cacheCountListener{})
		}, "user.created"},
		{"exact+10wildcards", func(d *DefaultDispatcher) {
			d.Listen("user.created", &cacheCountListener{})
			for i := range 10 {
				d.Listen("other"+strconv.Itoa(i)+".*", &cacheCountListener{})
			}
		}, "user.created"},
		{"wildcard", func(d *DefaultDispatcher) {
			d.Listen("user.*", &cacheCountListener{})
		}, "user.created"},
		{"typed", func(d *DefaultDispatcher) {
			d.Listen(OfType[benchTypedEvent](), &cacheCountListener{})
		}, benchTypedEvent{}},
		{"none", func(d *DefaultDispatcher) {
			d.Listen("user.created", &cacheCountListener{})
		}, "nobody.listens"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			d := NewDispatcher()
			c.setup(d)
			ev := c.event
			_ = d.Dispatch(ctx, ev)
			b.ReportAllocs()
			for b.Loop() {
				_ = d.Dispatch(ctx, ev)
			}
		})
		b.Run(c.name+"/parallel", func(b *testing.B) {
			d := NewDispatcher()
			c.setup(d)
			ev := c.event
			_ = d.Dispatch(ctx, ev)
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_ = d.Dispatch(ctx, ev)
				}
			})
		})
	}
}
