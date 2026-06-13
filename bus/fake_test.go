package bus

import (
	"context"
	"testing"
)

func TestFakeBus_Dispatch(t *testing.T) {
	f := NewFakeBus()

	err := f.Dispatch(createUser{Name: "Alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dispatched := f.GetDispatched()
	if len(dispatched) != 1 {
		t.Fatalf("expected 1 dispatched, got %d", len(dispatched))
	}
	if dispatched[0].(createUser).Name != "Alice" {
		t.Fatalf("wrong command recorded")
	}
}

func TestFakeBus_AssertDispatched(t *testing.T) {
	tests := []struct {
		name     string
		dispatch []Command
		assert   Command
		callback func(Command) bool
		wantErr  bool
	}{
		{
			name:    "nothing dispatched",
			assert:  createUser{},
			wantErr: true,
		},
		{
			name:     "type match, nil predicate",
			dispatch: []Command{createUser{Name: "Bob"}},
			assert:   createUser{},
			wantErr:  false,
		},
		{
			name:     "wrong type",
			dispatch: []Command{deleteUser{ID: 1}},
			assert:   createUser{},
			wantErr:  true,
		},
		{
			name:     "predicate matches",
			dispatch: []Command{createUser{Name: "Bob"}},
			assert:   createUser{},
			callback: func(c Command) bool { return c.(createUser).Name == "Bob" },
			wantErr:  false,
		},
		{
			name:     "type matches but predicate rejects",
			dispatch: []Command{createUser{Name: "Bob"}},
			assert:   createUser{},
			callback: func(c Command) bool { return c.(createUser).Name == "Alice" },
			wantErr:  true,
		},
		{
			name:     "predicate matches one of several",
			dispatch: []Command{createUser{Name: "Bob"}, createUser{Name: "Alice"}},
			assert:   createUser{},
			callback: func(c Command) bool { return c.(createUser).Name == "Alice" },
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFakeBus()
			for _, c := range tt.dispatch {
				f.Dispatch(c)
			}
			err := f.AssertDispatched(tt.assert, tt.callback)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AssertDispatched err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFakeBus_AssertDispatchedTimes(t *testing.T) {
	tests := []struct {
		name     string
		dispatch []Command
		assert   Command
		times    int
		wantErr  bool
	}{
		{name: "zero matches expected zero", assert: createUser{}, times: 0, wantErr: false},
		{name: "one dispatch expected two", dispatch: []Command{createUser{}}, assert: createUser{}, times: 2, wantErr: true},
		{name: "two dispatch expected two", dispatch: []Command{createUser{}, createUser{}}, assert: createUser{}, times: 2, wantErr: false},
		{name: "different type counts zero", dispatch: []Command{createUser{}}, assert: deleteUser{}, times: 0, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFakeBus()
			for _, c := range tt.dispatch {
				f.Dispatch(c)
			}
			err := f.AssertDispatchedTimes(tt.assert, tt.times)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AssertDispatchedTimes err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFakeBus_AssertNotDispatched(t *testing.T) {
	f := NewFakeBus()

	// Should pass when nothing dispatched
	if err := f.AssertNotDispatched(createUser{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should fail after dispatch
	f.Dispatch(createUser{})
	if err := f.AssertNotDispatched(createUser{}); err == nil {
		t.Fatal("expected error when command was dispatched")
	}

	// Different type should still pass
	if err := f.AssertNotDispatched(deleteUser{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFakeBus_AssertNothingDispatched(t *testing.T) {
	f := NewFakeBus()

	// Should pass with no dispatches
	if err := f.AssertNothingDispatched(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should fail after any dispatch
	f.Dispatch(createUser{})
	if err := f.AssertNothingDispatched(); err == nil {
		t.Fatal("expected error when commands were dispatched")
	}
}

func TestFakeBus_AssertAsyncDispatched(t *testing.T) {
	tests := []struct {
		name     string
		sync     []Command
		async    []Command
		assert   Command
		callback func(Command) bool
		wantErr  bool
	}{
		{
			name:    "nothing async dispatched",
			assert:  createUser{},
			wantErr: true,
		},
		{
			name:    "sync dispatch does not count as async",
			sync:    []Command{createUser{}},
			assert:  createUser{},
			wantErr: true,
		},
		{
			name:    "async type match, nil predicate",
			async:   []Command{createUser{Name: "Bob"}},
			assert:  createUser{},
			wantErr: false,
		},
		{
			name:     "predicate matches",
			async:    []Command{createUser{Name: "Bob"}},
			assert:   createUser{},
			callback: func(c Command) bool { return c.(createUser).Name == "Bob" },
			wantErr:  false,
		},
		{
			name:     "type matches but predicate rejects",
			async:    []Command{createUser{Name: "Bob"}},
			assert:   createUser{},
			callback: func(c Command) bool { return c.(createUser).Name == "Alice" },
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFakeBus()
			for _, c := range tt.sync {
				f.Dispatch(c)
			}
			for _, c := range tt.async {
				f.DispatchAsync(c)
			}
			err := f.AssertAsyncDispatched(tt.assert, tt.callback)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AssertAsyncDispatched err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFakeBus_AssertAsyncDispatchedTimes(t *testing.T) {
	tests := []struct {
		name    string
		async   []Command
		assert  Command
		times   int
		wantErr bool
	}{
		{name: "zero matches expected zero", assert: createUser{}, times: 0, wantErr: false},
		{name: "one async expected two", async: []Command{createUser{}}, assert: createUser{}, times: 2, wantErr: true},
		{name: "two async expected two", async: []Command{createUser{}, createUser{}}, assert: createUser{}, times: 2, wantErr: false},
		{name: "different type counts zero", async: []Command{createUser{}}, assert: deleteUser{}, times: 0, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFakeBus()
			for _, c := range tt.async {
				f.DispatchAsync(c)
			}
			err := f.AssertAsyncDispatchedTimes(tt.assert, tt.times)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AssertAsyncDispatchedTimes err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFakeBus_AssertAsyncNotDispatched(t *testing.T) {
	f := NewFakeBus()

	// Should pass when nothing async dispatched
	if err := f.AssertAsyncNotDispatched(createUser{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sync dispatch should not trip the async assertion
	f.Dispatch(createUser{})
	if err := f.AssertAsyncNotDispatched(createUser{}); err != nil {
		t.Fatalf("sync dispatch should not count as async: %v", err)
	}

	// Should fail after async dispatch
	f.DispatchAsync(createUser{})
	if err := f.AssertAsyncNotDispatched(createUser{}); err == nil {
		t.Fatal("expected error when command was async dispatched")
	}

	// Different type should still pass
	if err := f.AssertAsyncNotDispatched(deleteUser{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFakeBus_AssertNothingAsyncDispatched(t *testing.T) {
	f := NewFakeBus()

	// Should pass with no async dispatches
	if err := f.AssertNothingAsyncDispatched(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sync dispatch alone should still pass
	f.Dispatch(createUser{})
	if err := f.AssertNothingAsyncDispatched(); err != nil {
		t.Fatalf("sync dispatch should not count as async: %v", err)
	}

	// Should fail after any async dispatch
	f.DispatchAsync(createUser{})
	if err := f.AssertNothingAsyncDispatched(); err == nil {
		t.Fatal("expected error when commands were async dispatched")
	}
}

func TestFakeBus_DispatchAsyncCtx(t *testing.T) {
	f := NewFakeBus()

	if err := f.DispatchAsyncCtx(context.Background(), createUser{Name: "Ctx"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := f.AssertAsyncDispatched(createUser{}, nil); err != nil {
		t.Fatalf("DispatchAsyncCtx should record async dispatch: %v", err)
	}
}

func TestFakeBus_ClearDispatched(t *testing.T) {
	f := NewFakeBus()

	f.Dispatch(createUser{})
	f.DispatchAsync(deleteUser{})

	f.ClearDispatched()

	if err := f.AssertNothingDispatched(); err != nil {
		t.Fatalf("expected nothing dispatched after clear: %v", err)
	}

	dispatched := f.GetDispatched()
	if len(dispatched) != 0 {
		t.Fatalf("expected empty dispatched list after clear, got %d", len(dispatched))
	}

	// Async should also be cleared
	if err := f.AssertAsyncDispatched(deleteUser{}, nil); err == nil {
		t.Fatal("expected async dispatched to be cleared")
	}
}

func TestFakeBus_ImplementsDispatcher(t *testing.T) {
	var _ Dispatcher = NewFakeBus()
}

func TestFakeBus_GetDispatched_ReturnsCopy(t *testing.T) {
	f := NewFakeBus()
	f.Dispatch(createUser{Name: "A"})

	d1 := f.GetDispatched()
	d2 := f.GetDispatched()

	// Modifying one should not affect the other
	d1[0] = createUser{Name: "modified"}
	if d2[0].(createUser).Name != "A" {
		t.Fatal("GetDispatched should return a copy, not the original slice")
	}
}

func TestFakeBus_GetAsyncDispatched_ReturnsCopy(t *testing.T) {
	f := NewFakeBus()
	f.DispatchAsync(createUser{Name: "A"})

	d1 := f.GetAsyncDispatched()
	d2 := f.GetAsyncDispatched()
	if len(d1) != 1 {
		t.Fatalf("expected 1 async dispatched, got %d", len(d1))
	}

	// Modifying one should not affect the other
	d1[0] = createUser{Name: "modified"}
	if d2[0].(createUser).Name != "A" {
		t.Fatal("GetAsyncDispatched should return a copy, not the original slice")
	}
}

// TestBus_EmittedEventsCarryContext is a regression guard: the lifecycle
// events emitted by a real *Bus must carry a non-nil Context. Earlier the
// events omitted Context entirely; this pins that they keep populating it so
// downstream listeners (tracing, APM) can read the active context.
func TestBus_EmittedEventsCarryContext(t *testing.T) {
	var captured []any
	b := New()
	b.SetEventDispatcher(func(_ context.Context, event any) error {
		captured = append(captured, event)
		return nil
	})

	// Sync path: emits CommandDispatching + CommandCompleted.
	Register(b, func(createUser) error { return nil })
	if err := b.Dispatch(createUser{Name: "Sync"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Sync error path: emits CommandFailed.
	Register(b, func(deleteUser) error { return context.Canceled })
	_ = b.Dispatch(deleteUser{ID: 1})

	// Async path: emits CommandQueued (createUser already has a factory).
	b.SetQueue(&mockQueuePusher{})
	if err := b.DispatchAsync(createUser{Name: "Async"}); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}

	var sawDispatching, sawCompleted, sawFailed, sawQueued bool
	for _, e := range captured {
		switch ev := e.(type) {
		case *CommandDispatching:
			sawDispatching = true
			if ev.Context == nil {
				t.Error("CommandDispatching.Context is nil")
			}
		case *CommandCompleted:
			sawCompleted = true
			if ev.Context == nil {
				t.Error("CommandCompleted.Context is nil")
			}
		case *CommandFailed:
			sawFailed = true
			if ev.Context == nil {
				t.Error("CommandFailed.Context is nil")
			}
		case *CommandQueued:
			sawQueued = true
			if ev.Context == nil {
				t.Error("CommandQueued.Context is nil")
			}
		}
	}
	if !sawDispatching || !sawCompleted || !sawFailed || !sawQueued {
		t.Fatalf("missing events: dispatching=%v completed=%v failed=%v queued=%v",
			sawDispatching, sawCompleted, sawFailed, sawQueued)
	}
}
