package broadcasttest

import (
	"context"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/broadcast"
)

// FakeBroadcaster must satisfy the executable Driver contract.
func TestFakeBroadcaster_DriverContract(t *testing.T) {
	RunDriverContractTests(t, func(t *testing.T) broadcast.Driver {
		return NewFakeBroadcaster()
	})
}

// recordVia drives one broadcast through the fake using the named entry point,
// so a single table can exercise every method while asserting the same way.
func recordVia(f *FakeBroadcaster, via string) error {
	switch via {
	case "BroadcastCtx":
		return f.BroadcastCtx(context.Background(), []string{"orders"}, "created", "d")
	case "Broadcast":
		return f.Broadcast([]string{"orders"}, "created", "d")
	case "BroadcastExceptCtx":
		return f.BroadcastExceptCtx(context.Background(), []string{"orders"}, "created", "d", "sock-1")
	case "BroadcastExcept":
		return f.BroadcastExcept([]string{"orders"}, "created", "d", "sock-1")
	default:
		panic("unknown via: " + via)
	}
}

func TestFakeBroadcaster_RecordsAndAsserts(t *testing.T) {
	tests := []struct {
		name    string
		via     string
		assert  func(f *FakeBroadcaster) error
		wantErr bool
	}{
		{
			name:   "BroadcastCtx string matchers",
			via:    "BroadcastCtx",
			assert: func(f *FakeBroadcaster) error { return f.AssertBroadcast("orders", "created") },
		},
		{
			name:   "Broadcast shim recorded",
			via:    "Broadcast",
			assert: func(f *FakeBroadcaster) error { return f.AssertBroadcast("orders", "created") },
		},
		{
			name: "predicate matchers",
			via:  "BroadcastCtx",
			assert: func(f *FakeBroadcaster) error {
				return f.AssertBroadcast(
					func(c string) bool { return c == "orders" },
					func(e string) bool { return e == "created" },
				)
			},
		},
		{
			name:    "wrong event does not match",
			via:     "BroadcastCtx",
			assert:  func(f *FakeBroadcaster) error { return f.AssertBroadcast("orders", "deleted") },
			wantErr: true,
		},
		{
			name:    "wrong channel does not match",
			via:     "BroadcastCtx",
			assert:  func(f *FakeBroadcaster) error { return f.AssertBroadcast("invoices", "created") },
			wantErr: true,
		},
		{
			name:    "AssertNotBroadcast fails when present",
			via:     "BroadcastCtx",
			assert:  func(f *FakeBroadcaster) error { return f.AssertNotBroadcast("orders", "created") },
			wantErr: true,
		},
		{
			name:   "AssertNotBroadcast passes when absent",
			via:    "BroadcastCtx",
			assert: func(f *FakeBroadcaster) error { return f.AssertNotBroadcast("orders", "deleted") },
		},
		{
			name:    "AssertNothingBroadcast fails after record",
			via:     "BroadcastCtx",
			assert:  func(f *FakeBroadcaster) error { return f.AssertNothingBroadcast() },
			wantErr: true,
		},
		{
			name:   "BroadcastExceptCtx records socket",
			via:    "BroadcastExceptCtx",
			assert: func(f *FakeBroadcaster) error { return f.AssertBroadcastExcept("orders", "created", "sock-1") },
		},
		{
			name:   "BroadcastExcept shim records socket",
			via:    "BroadcastExcept",
			assert: func(f *FakeBroadcaster) error { return f.AssertBroadcastExcept("orders", "created", "sock-1") },
		},
		{
			name:    "AssertBroadcastExcept wrong socket fails",
			via:     "BroadcastExceptCtx",
			assert:  func(f *FakeBroadcaster) error { return f.AssertBroadcastExcept("orders", "created", "sock-2") },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFakeBroadcaster()
			if err := recordVia(f, tt.via); err != nil {
				t.Fatalf("recordVia(%s): %v", tt.via, err)
			}
			err := tt.assert(f)
			if tt.wantErr && err == nil {
				t.Fatal("expected assertion error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected assertion error: %v", err)
			}
		})
	}
}

func TestFakeBroadcaster_InvalidMatchers(t *testing.T) {
	f := NewFakeBroadcaster()
	if err := f.BroadcastCtx(context.Background(), []string{"orders"}, "created", "d"); err != nil {
		t.Fatalf("BroadcastCtx: %v", err)
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{"bad channel matcher", func() error { return f.AssertBroadcast(123, "created") }},
		{"bad event matcher", func() error { return f.AssertBroadcast("orders", 123) }},
		{"bad matcher in NotBroadcast", func() error { return f.AssertNotBroadcast(123, "created") }},
		{"bad matcher in Except", func() error { return f.AssertBroadcastExcept("orders", []int{1}, "s") }},
		{"typed nil channel predicate in AssertBroadcast", func() error {
			return f.AssertBroadcast((func(string) bool)(nil), "created")
		}},
		{"typed nil event predicate in AssertBroadcast", func() error {
			return f.AssertBroadcast("orders", (func(string) bool)(nil))
		}},
		{"typed nil predicate in AssertNotBroadcast", func() error {
			return f.AssertNotBroadcast((func(string) bool)(nil), "created")
		}},
		{"typed nil predicate in AssertBroadcastExcept", func() error {
			return f.AssertBroadcastExcept((func(string) bool)(nil), "created", "s")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); err == nil {
				t.Fatal("expected descriptive error for invalid matcher, got nil")
			}
		})
	}
}

func TestFakeBroadcaster_CancelledCtxPreCheck(t *testing.T) {
	f := NewFakeBroadcaster()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := f.BroadcastCtx(ctx, []string{"orders"}, "created", "d"); err != context.Canceled {
		t.Fatalf("BroadcastCtx cancelled: got %v, want context.Canceled", err)
	}
	if err := f.BroadcastExceptCtx(ctx, []string{"orders"}, "created", "d", "s"); err != context.Canceled {
		t.Fatalf("BroadcastExceptCtx cancelled: got %v, want context.Canceled", err)
	}
	// Cancellation must short-circuit BEFORE recording.
	if err := f.AssertNothingBroadcast(); err != nil {
		t.Fatalf("cancelled broadcast must not record: %v", err)
	}
}

func TestFakeBroadcaster_NilCtxNoPanic(t *testing.T) {
	f := NewFakeBroadcaster()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil ctx panicked: %v", r)
		}
	}()
	//lint:ignore SA1012 contract probe: nil-ctx defensive guard is what's under test
	if err := f.BroadcastCtx(nil, []string{"orders"}, "created", "d"); err != nil {
		t.Fatalf("nil ctx BroadcastCtx: %v", err)
	}
	//lint:ignore SA1012 contract probe: nil-ctx defensive guard is what's under test
	if err := f.BroadcastExceptCtx(nil, []string{"orders"}, "created", "d", "s"); err != nil {
		t.Fatalf("nil ctx BroadcastExceptCtx: %v", err)
	}
	if err := f.AssertBroadcast("orders", "created"); err != nil {
		t.Fatalf("nil ctx should still record: %v", err)
	}
}

func TestFakeBroadcaster_GetClientsNeverNil(t *testing.T) {
	f := NewFakeBroadcaster()
	got := f.GetClients("anything")
	if got == nil {
		t.Fatal("GetClients returned nil, must be non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("GetClients = %v, want empty", got)
	}
}

func TestFakeBroadcaster_Reset(t *testing.T) {
	f := NewFakeBroadcaster()
	if err := f.BroadcastCtx(context.Background(), []string{"orders"}, "created", "d"); err != nil {
		t.Fatalf("BroadcastCtx: %v", err)
	}
	if err := f.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if err := f.AssertNothingBroadcast(); err != nil {
		t.Fatalf("after Reset: %v", err)
	}
}

// TestFakeBroadcaster_ConcurrentFanOut hammers the recorder from many
// goroutines while a reader asserts concurrently, to flush out data races
// under -race.
func TestFakeBroadcaster_ConcurrentFanOut(t *testing.T) {
	f := NewFakeBroadcaster()
	const writers = 50
	const perWriter = 20

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				_ = f.BroadcastCtx(context.Background(), []string{"orders"}, "created", j)
				_ = f.AssertBroadcast("orders", "created")
				_ = f.GetClients("orders")
			}
		}()
	}
	wg.Wait()

	if err := f.AssertBroadcast("orders", "created"); err != nil {
		t.Fatalf("after fan-out: %v", err)
	}
}

// TestFakeBroadcaster_ViaManager confirms the documented usage:
// broadcast.New(NewFakeBroadcaster()) then assert what the manager sent.
func TestFakeBroadcaster_ViaManager(t *testing.T) {
	f := NewFakeBroadcaster()
	mgr := broadcast.New(f)

	if err := mgr.Channel("orders").EmitCtx(context.Background(), "created", "d"); err != nil {
		t.Fatalf("EmitCtx: %v", err)
	}
	if err := f.AssertBroadcast("orders", "created"); err != nil {
		t.Fatalf("AssertBroadcast: %v", err)
	}

	if err := mgr.Channel("orders").ToOthers("sock-9").EmitCtx(context.Background(), "updated", "d"); err != nil {
		t.Fatalf("EmitCtx ToOthers: %v", err)
	}
	if err := f.AssertBroadcastExcept("orders", "updated", "sock-9"); err != nil {
		t.Fatalf("AssertBroadcastExcept: %v", err)
	}
}
