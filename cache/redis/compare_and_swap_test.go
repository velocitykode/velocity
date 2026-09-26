package redis

import (
	"context"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// interveneAfterGet runs write once, right after the store's first GET
// completes, so a test can land a write between the compare-and-swap's
// read of the stored bytes and its conditioned script run.
type interveneAfterGet struct {
	once  sync.Once
	write func()
}

func (h *interveneAfterGet) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h *interveneAfterGet) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if cmd.Name() == "get" {
			h.once.Do(h.write)
		}
		return err
	}
}

func (h *interveneAfterGet) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// TestRedisStore_CompareAndSwapCtx_WriteAfterStoredRead_WritesNothing pins
// the atomicity of the path that compares a read-back struct: the swap is
// conditioned on the exact bytes it read, so a write landing after that
// read makes it fail instead of being overwritten.
func TestRedisStore_CompareAndSwapCtx_WriteAfterStoredRead_WritesNothing(t *testing.T) {
	s, _ := newTestRedisStore(t, "cas")
	ctx := context.Background()
	type record struct{ Z, A int }
	if err := s.PutCtx(ctx, "k", record{Z: 1, A: 2}, time.Minute); err != nil {
		t.Fatalf("PutCtx: %v", err)
	}
	read, found := s.GetCtx(ctx, "k")
	if !found {
		t.Fatal("GetCtx did not find the stored struct")
	}
	s.client.AddHook(&interveneAfterGet{write: func() {
		if err := s.client.Set(ctx, s.prefixedKey("k"), `{"Z":1,"A":3}`, time.Minute).Err(); err != nil {
			t.Errorf("intervening write: %v", err)
		}
	}})

	ok, err := s.CompareAndSwapCtx(ctx, "k", read, "stale", time.Minute)
	if err != nil {
		t.Fatalf("CompareAndSwapCtx: %v", err)
	}
	if ok {
		t.Fatal("CompareAndSwapCtx overwrote a write that landed after its read")
	}
	got, _ := s.GetCtx(ctx, "k")
	if want := map[string]interface{}{"Z": 1.0, "A": 3.0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("value after a failed swap = %v, want the intervening write %v", got, want)
	}
}
