// Package broadcasttest provides executable specifications (contract tests)
// for [broadcast.Driver] implementations.
//
// The runner exercises the Driver surface that is observable without an
// active subscriber: empty-channel semantics, ctx-cancellation, and the
// snapshot shape of GetClients. Wire-side behaviour (actual fan-out to
// connected WebSocket clients) is covered by driver-specific tests in
// broadcast/drivers; the contract runner intentionally avoids spinning a
// listener so third-party drivers can run it offline.
package broadcasttest

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/broadcast"
)

// DriverFactory returns a fresh Driver per sub-test.
type DriverFactory func(t *testing.T) broadcast.Driver

// RunDriverContractTests is the executable specification of [broadcast.Driver].
func RunDriverContractTests(t *testing.T, factory DriverFactory) {
	t.Helper()

	t.Run("BroadcastCtx_NoSubscribers_NoError", func(t *testing.T) {
		d := factory(t)
		err := d.BroadcastCtx(context.Background(), []string{"empty-channel"}, "evt", map[string]string{"k": "v"})
		if err != nil {
			t.Fatalf("BroadcastCtx to empty channel: %v", err)
		}
	})

	t.Run("BroadcastCtx_CancelledCtx_ReturnsError", func(t *testing.T) {
		d := factory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := d.BroadcastCtx(ctx, []string{"any-channel"}, "evt", "data")
		if err == nil {
			t.Fatal("BroadcastCtx with cancelled ctx must surface ctx.Err()")
		}
	})

	t.Run("BroadcastCtx_NilCtx_DoesNotPanic", func(t *testing.T) {
		d := factory(t)
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("driver panicked on nil ctx: %v", r)
			}
		}()
		// Drivers MAY treat nil ctx as Background or return an error;
		// the invariant is "never panic". Using a typed nil here is the
		// adversarial test fixture; production callers should pass
		// context.Background or a real request ctx.
		//lint:ignore SA1012 contract probe: nil-ctx defensive guard is what's under test
		_ = d.BroadcastCtx(nil, []string{"any"}, "evt", "data")
	})

	t.Run("BroadcastExceptCtx_NoSubscribers_NoError", func(t *testing.T) {
		d := factory(t)
		err := d.BroadcastExceptCtx(context.Background(), []string{"empty-channel"}, "evt", "data", "exclude-me")
		if err != nil {
			t.Fatalf("BroadcastExceptCtx to empty channel: %v", err)
		}
	})

	t.Run("BroadcastExceptCtx_CancelledCtx_ReturnsError", func(t *testing.T) {
		d := factory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := d.BroadcastExceptCtx(ctx, []string{"any-channel"}, "evt", "data", "x")
		if err == nil {
			t.Fatal("BroadcastExceptCtx with cancelled ctx must surface ctx.Err()")
		}
	})

	t.Run("GetClients_UnknownChannel_ReturnsEmptySlice", func(t *testing.T) {
		d := factory(t)
		got := d.GetClients("never-subscribed")
		if len(got) != 0 {
			t.Fatalf("expected empty slice for unknown channel, got %v", got)
		}
	})

	t.Run("GetClients_DoesNotPanic", func(t *testing.T) {
		d := factory(t)
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("GetClients panicked: %v", r)
			}
		}()
		_ = d.GetClients("")
	})

	t.Run("Broadcast_DeprecatedShim_DelegatesToCtx", func(t *testing.T) {
		d := factory(t)
		// The non-Ctx Broadcast shim delegates to BroadcastCtx with
		// context.Background, so it must succeed against an empty channel.
		if err := d.Broadcast([]string{"shim-empty"}, "evt", "data"); err != nil {
			t.Fatalf("Broadcast shim: %v", err)
		}
		if err := d.BroadcastExcept([]string{"shim-empty"}, "evt", "data", "x"); err != nil {
			t.Fatalf("BroadcastExcept shim: %v", err)
		}
	})
}
