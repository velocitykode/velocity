package ownctx

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/tracekeys"
)

type userKey struct{}

// countingCtx counts every call of its methods: the caller's context, as
// user code the owned context must not call.
type countingCtx struct {
	context.Context
	calls *atomic.Int64
}

func (c countingCtx) Deadline() (time.Time, bool) { c.calls.Add(1); return c.Context.Deadline() }
func (c countingCtx) Done() <-chan struct{}       { c.calls.Add(1); return c.Context.Done() }
func (c countingCtx) Err() error                  { c.calls.Add(1); return c.Context.Err() }
func (c countingCtx) Value(k any) any             { c.calls.Add(1); return c.Context.Value(k) }

func withIDs(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, tracekeys.RequestID, "req-1")
	ctx = context.WithValue(ctx, tracekeys.TraceID, "trace-1")
	ctx = context.WithValue(ctx, tracekeys.SpanID, "span-1")
	return context.WithValue(ctx, tracekeys.ParentID, "parent-1")
}

func TestBridge_NothingToCarryIsBackground(t *testing.T) {
	var nilCtx context.Context
	for name, ctx := range map[string]context.Context{
		"nil":         nilCtx,
		"background":  context.Background(),
		"todo":        context.TODO(),
		"user values": context.WithValue(context.Background(), userKey{}, "v"),
	} {
		if got := Bridge(ctx); got != context.Background() {
			t.Errorf("Bridge(%s) = %v, want context.Background()", name, got)
		}
		if got := Detached(ctx); got != context.Background() {
			t.Errorf("Detached(%s) = %v, want context.Background()", name, got)
		}
	}
}

func TestBridge_FollowsTheCallersEnd(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	o := Bridge(parent)
	if o.Done() != parent.Done() {
		t.Fatal("Done is not the caller's channel")
	}
	if err := o.Err(); err != nil {
		t.Fatalf("Err before cancel = %v", err)
	}
	if _, ok := o.Deadline(); ok {
		t.Fatal("a deadline the caller does not have")
	}
	cancel()
	<-o.Done()
	if err := o.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Err after cancel = %v, want context.Canceled", err)
	}
}

func TestBridge_CarriesTheDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	want, _ := parent.Deadline()
	o := Bridge(parent)
	if got, ok := o.Deadline(); !ok || !got.Equal(want) {
		t.Fatalf("Deadline = %v, %v; want %v", got, ok, want)
	}
	<-o.Done()
	if err := o.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Err after the deadline = %v, want context.DeadlineExceeded", err)
	}
}

// A caller's error of its own is not carried: Err is the stdlib sentinel.
func TestBridge_ErrIsTheSentinelNotTheCause(t *testing.T) {
	cause := errors.New("caller's cause")
	parent, cancel := context.WithCancelCause(context.Background())
	o := Bridge(parent)
	cancel(cause)
	<-o.Done()
	if err := o.Err(); err != context.Canceled {
		t.Fatalf("Err = %v, want context.Canceled", err)
	}
	if got := context.Cause(o); got != context.Canceled {
		t.Fatalf("Cause = %v, want context.Canceled (the caller's cause is not carried)", got)
	}
}

func TestBridge_AnswersTheCorrelationIDsOnly(t *testing.T) {
	parent := withIDs(context.WithValue(context.Background(), userKey{}, "user"))
	for name, o := range map[string]context.Context{"Bridge": Bridge(parent), "Detached": Detached(parent)} {
		for key, want := range map[tracekeys.Key]string{
			tracekeys.RequestID: "req-1", tracekeys.TraceID: "trace-1",
			tracekeys.SpanID: "span-1", tracekeys.ParentID: "parent-1",
		} {
			if got := o.Value(key); got != want {
				t.Errorf("%s: Value(%s) = %v, want %q", name, key, got, want)
			}
		}
		if got := o.Value(userKey{}); got != nil {
			t.Errorf("%s: a caller's own value %v was answered", name, got)
		}
	}
}

// Detached never ends, whatever the caller's context does.
func TestDetached_NeverEnds(t *testing.T) {
	parent, cancel := context.WithTimeout(withIDs(context.Background()), time.Hour)
	o := Detached(parent)
	cancel()
	if o.Done() != nil || o.Err() != nil {
		t.Fatalf("Detached ended with its caller: Done %v, Err %v", o.Done(), o.Err())
	}
	if _, ok := o.Deadline(); ok {
		t.Fatal("Detached carries the caller's deadline")
	}
	bounded, stop := context.WithTimeout(o, time.Millisecond)
	defer stop()
	<-bounded.Done()
	if got := bounded.Value(tracekeys.TraceID); got != "trace-1" {
		t.Fatalf("a timeout over Detached lost the trace id: %v", got)
	}
}

// Bridge reads the caller's context once, before the lock; the owned
// context never calls it again, whatever is asked of it.
func TestBridge_NeverCallsTheCallersContextAfterwards(t *testing.T) {
	var calls atomic.Int64
	base, cancel := context.WithTimeout(withIDs(context.Background()), time.Hour)
	defer cancel()
	parent := countingCtx{base, &calls}
	for name, build := range map[string]func(context.Context) context.Context{"Bridge": Bridge, "Detached": Detached} {
		o := build(parent)
		before := calls.Load()
		for range 3 {
			o.Deadline()
			o.Done()
			_ = o.Err()
			o.Value(tracekeys.TraceID)
			o.Value(userKey{})
		}
		_, stop := context.WithTimeout(o, time.Hour)
		stop()
		if n := calls.Load() - before; n != 0 {
			t.Errorf("%s: the owned context called the caller's %d times", name, n)
		}
	}
}

// Readers of an owned context race its caller's cancel.
func TestBridge_Concurrent(t *testing.T) {
	for range 50 {
		parent, cancel := context.WithCancel(withIDs(context.Background()))
		o := Bridge(parent)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for range 100 {
					_ = o.Err()
					_ = o.Value(tracekeys.RequestID)
				}
				<-o.Done()
				if !errors.Is(o.Err(), context.Canceled) {
					t.Error("Err after Done closed is not context.Canceled")
				}
			})
		}
		cancel()
		wg.Wait()
	}
}

var sink context.Context

func BenchmarkBridge(b *testing.B) {
	withDeadline, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	cancellable, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"background", context.Background()},
		{"cancellable", cancellable},
		{"deadline", withDeadline},
		{"deadline+ids", withIDs(withDeadline)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sink = Bridge(tc.ctx)
			}
		})
		b.Run(tc.name+"/parallel", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				var local context.Context
				for pb.Next() {
					local = Bridge(tc.ctx)
				}
				_ = local
			})
		})
	}
}
