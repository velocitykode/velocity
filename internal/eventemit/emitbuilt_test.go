package eventemit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

type builtEvent struct{ n int }

// TestEmitBuilt_BuildsOnlyForADispatcher is the one proof that a framework
// component builds no event nobody would receive: every component hands
// its events over through EmitBuilt, and EmitBuilt calls the builder only
// while a dispatcher is installed.
func TestEmitBuilt_BuildsOnlyForADispatcher(t *testing.T) {
	var nilEmitter *Emitter
	withDispatcher := func() *Emitter {
		e := &Emitter{}
		e.Set(func(context.Context, any) error { return nil })
		return e
	}
	removed := withDispatcher()
	removed.Set(nil)

	tests := []struct {
		name      string
		emitter   *Emitter
		wantBuilt int
	}{
		{"nil emitter", nilEmitter, 0},
		{"zero emitter", &Emitter{}, 0},
		{"dispatcher removed", removed, 0},
		{"dispatcher installed", withDispatcher(), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			built := 0
			installed := tt.emitter.EmitBuilt(context.Background(), func() any {
				built++
				return &builtEvent{n: built}
			})
			if built != tt.wantBuilt {
				t.Errorf("builder called %d times, want %d", built, tt.wantBuilt)
			}
			if installed != (tt.wantBuilt == 1) {
				t.Errorf("EmitBuilt reported installed=%v with the builder called %d times", installed, built)
			}
		})
	}
}

// TestEmitBuilt_HandsTheBuiltEventOver requires the event the builder
// returns, and the caller's context, to reach the dispatcher, with a nil
// context replaced by context.Background as Emit does.
func TestEmitBuilt_HandsTheBuiltEventOver(t *testing.T) {
	type key struct{}
	var got []any
	var ctxs []context.Context
	e := &Emitter{}
	e.Set(func(ctx context.Context, event any) error {
		ctxs = append(ctxs, ctx)
		got = append(got, event)
		return nil
	})
	want := &builtEvent{n: 7}
	ctx := context.WithValue(context.Background(), key{}, "v")
	e.EmitBuilt(ctx, func() any { return want })
	var nilCtx context.Context
	e.EmitBuilt(nilCtx, func() any { return want })
	if len(got) != 2 || got[0] != want || got[1] != want {
		t.Fatalf("dispatcher received %v, want the built event twice", got)
	}
	if ctxs[0].Value(key{}) != "v" || ctxs[1] == nil {
		t.Errorf("dispatcher contexts = %v, want the caller's and context.Background", ctxs)
	}
}

// TestEmitBuilt_FailedDispatchMeetsThePolicy requires a dispatch of a built
// event that fails, by an error or a panic, to be counted once, as Emit's.
func TestEmitBuilt_FailedDispatchMeetsThePolicy(t *testing.T) {
	for name, dispatch := range map[string]func(context.Context, any) error{
		"error": func(context.Context, any) error { return errListener },
		"panic": func(context.Context, any) error { panic("sink broke") },
	} {
		t.Run(name, func(t *testing.T) {
			e := &Emitter{}
			e.UseLogger(func() contract.Logger { return benchLogger{} })
			e.Set(dispatch)
			e.EmitBuilt(context.Background(), func() any { return &builtEvent{} })
			if got := e.FailureCount(); got != 1 {
				t.Errorf("failure count = %d, want 1", got)
			}
		})
	}
}

// TestEmitBuilt_ConcurrentSet races EmitBuilt against a dispatcher being
// installed and removed: every event built reaches a dispatcher, so the
// builds and the deliveries match exactly.
func TestEmitBuilt_ConcurrentSet(t *testing.T) {
	var built, delivered atomic.Int64
	e := &Emitter{}
	fn := func(context.Context, any) error { delivered.Add(1); return nil }
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.Set(fn)
			e.Set(nil)
		}
	}()
	var emitters sync.WaitGroup
	for range 8 {
		emitters.Add(1)
		go func() {
			defer emitters.Done()
			for range 2000 {
				e.EmitBuilt(context.Background(), func() any { built.Add(1); return &builtEvent{} })
			}
		}()
	}
	emitters.Wait()
	close(stop)
	wg.Wait()
	if built.Load() != delivered.Load() {
		t.Errorf("built %d events, delivered %d: an event was built for no dispatcher", built.Load(), delivered.Load())
	}
}
