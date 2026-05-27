package bus

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/queue"
)

// roundTripCmd, durableCmd, registryCmd, unregisteredCmd, and missingBusCmd
// are command types the round-trip tests use exclusively. They must NOT
// collide with the names used elsewhere in the bus tests, because the
// package-level command-factory registry is process-wide and shared
// across all tests.
type roundTripCmd struct {
	Name  string
	Count int
}

type durableCmd struct {
	ID  int
	Tag string
}

type registryCmd struct {
	V string
}

type unregisteredCmd struct {
	X string
}

type missingBusCmd struct {
	Z string
}

// memoryQueueAdapter wraps a queue.Driver so it satisfies the bus's
// QueuePusher interface (a Push method with the bus's job shape).
type memoryQueueAdapter struct {
	d queue.Driver
}

func (m memoryQueueAdapter) Push(job interface {
	Handle() error
	Failed(error)
}, qname ...string) error {
	return m.d.PushCtx(context.Background(), job.(queue.Job), qname...)
}

// TestDispatchAsync_MemoryQueueRoundTrip exercises the in-process memory
// driver path. The producer-side commandJob retains live cmd / bus /
// cmdType fields so the worker dispatches the original pointer without a
// JSON round-trip.
func TestDispatchAsync_MemoryQueueRoundTrip(t *testing.T) {
	b := New()
	driver := queue.NewMemoryDriver()
	driver.Start()
	defer driver.Shutdown(context.Background())

	b.SetQueue(memoryQueueAdapter{driver})

	var got roundTripCmd
	var mu sync.Mutex
	Register(b, func(cmd roundTripCmd) error {
		mu.Lock()
		got = cmd
		mu.Unlock()
		return nil
	})

	if err := b.DispatchAsync(roundTripCmd{Name: "memory", Count: 7}); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}

	job, err := driver.PopCtx(context.Background(), "default")
	if err != nil {
		t.Fatalf("PopCtx: %v", err)
	}
	if job == nil {
		t.Fatal("expected job from queue, got nil")
	}

	if err := job.Handle(); err != nil {
		t.Fatalf("job.Handle: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got.Name != "memory" || got.Count != 7 {
		t.Fatalf("handler received wrong command: %+v", got)
	}
}

// TestDispatchAsync_DurableRoundTrip simulates a durable driver (Redis or
// database) by forcing the commandJob through encoding/json. This bypasses
// the in-process fast-path fields the same way a worker in another process
// would after the wire transport stripped them. The hydrated job MUST
// reconstruct the command via the package factory registry and dispatch
// through the default bus.
func TestDispatchAsync_DurableRoundTrip(t *testing.T) {
	b := New()
	mock := &mockQueuePusher{}
	b.SetQueue(mock)

	var got durableCmd
	var mu sync.Mutex
	Register(b, func(cmd durableCmd) error {
		mu.Lock()
		got = cmd
		mu.Unlock()
		return nil
	})

	if err := b.DispatchAsync(durableCmd{ID: 42, Tag: "durable"}); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}

	mock.mu.Lock()
	if len(mock.jobs) != 1 {
		mock.mu.Unlock()
		t.Fatalf("expected 1 job pushed, got %d", len(mock.jobs))
	}
	pushed := mock.jobs[0]
	mock.mu.Unlock()

	// Marshal then unmarshal the job to drop the in-process fast-path
	// fields exactly as a Redis or database driver would after a worker
	// fetches the payload from the durable store.
	wireBytes, err := json.Marshal(pushed)
	if err != nil {
		t.Fatalf("json.Marshal(commandJob): %v", err)
	}

	// Sanity check: the marshalled form must carry both the command type
	// and its data. A `{}` payload is the bug this fix closes.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(wireBytes, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	if _, ok := probe["type"]; !ok {
		t.Fatalf("commandJob JSON missing type field: %s", string(wireBytes))
	}
	if _, ok := probe["data"]; !ok {
		t.Fatalf("commandJob JSON missing data field: %s", string(wireBytes))
	}

	rehydrated, err := commandJobFactory(wireBytes)
	if err != nil {
		t.Fatalf("commandJobFactory: %v", err)
	}
	if rehydrated.cmd != nil || rehydrated.bus != nil {
		t.Fatal("hydrated commandJob must not carry in-process pointers")
	}

	if err := rehydrated.Handle(); err != nil {
		t.Fatalf("rehydrated Handle: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got.ID != 42 || got.Tag != "durable" {
		t.Fatalf("handler received wrong command after round-trip: %+v", got)
	}
}

// TestDispatchAsync_DurableRoundTrip_QueueRegistry confirms the factory we
// install in init() under queue.RegisterJob actually rebuilds a *commandJob
// (and not a stub) when a payload arrives carrying our type key. This is
// the path Redis and database drivers use on Pop.
func TestDispatchAsync_DurableRoundTrip_QueueRegistry(t *testing.T) {
	b := New()
	mock := &mockQueuePusher{}
	b.SetQueue(mock)

	var got registryCmd
	var mu sync.Mutex
	Register(b, func(cmd registryCmd) error {
		mu.Lock()
		got = cmd
		mu.Unlock()
		return nil
	})

	if err := b.DispatchAsync(registryCmd{V: "via-registry"}); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}

	mock.mu.Lock()
	pushed := mock.jobs[0]
	mock.mu.Unlock()

	// Drive the same flow a durable driver follows: MarshalJob serializes
	// the job's exported fields, HydrateJob looks up the factory we
	// registered with queue.RegisterJob in init() and returns the
	// rehydrated value.
	payload, err := queue.MarshalJob(pushed.(queue.Job), "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}

	hydrated, err := queue.HydrateJob(payload)
	if err != nil {
		t.Fatalf("HydrateJob: %v", err)
	}

	cj, ok := hydrated.(*commandJob)
	if !ok {
		t.Fatalf("HydrateJob returned %T, want *commandJob", hydrated)
	}
	if cj.cmd != nil || cj.bus != nil {
		t.Fatal("hydrated commandJob must not carry in-process pointers")
	}

	if err := cj.Handle(); err != nil {
		t.Fatalf("hydrated Handle: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got.V != "via-registry" {
		t.Fatalf("handler received wrong command after round-trip: %+v", got)
	}
}

// TestDispatchAsync_UnregisteredCommand_FailsLoud verifies the producer
// side refuses to enqueue a command type that has no factory registered.
// Without this guard, the worker would Pop the payload, fail to look up
// the command type, and either drop the job or return ErrJobNotFound;
// the producer would have already returned success.
func TestDispatchAsync_UnregisteredCommand_FailsLoud(t *testing.T) {
	b := New()
	mock := &mockQueuePusher{}
	b.SetQueue(mock)

	// No Register call for unregisteredCmd. DispatchAsync must refuse.
	err := b.DispatchAsync(unregisteredCmd{})
	if err == nil {
		t.Fatal("expected DispatchAsync to refuse unregistered command")
	}
	if !strings.Contains(err.Error(), "no factory registered") {
		t.Fatalf("expected 'no factory registered' in error, got %q", err.Error())
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.jobs) != 0 {
		t.Fatalf("expected no jobs pushed for unregistered command, got %d", len(mock.jobs))
	}
}

// TestDispatchAsync_DurableRoundTrip_MissingFactory verifies the consumer
// side fails loud when a payload arrives carrying a command type the
// worker process never registered. Returning a nil error with a no-op
// dispatch would reopen the silent-drop hole this fix closes.
func TestDispatchAsync_DurableRoundTrip_MissingFactory(t *testing.T) {
	// Make sure a default bus exists so the failure path under test is
	// specifically the missing-factory case and not the missing-bus case.
	b := New()
	SetDefaultBus(b)

	wire := []byte(`{"type":"bus.ghostCmd","data":{}}`)
	rehydrated, err := commandJobFactory(wire)
	if err != nil {
		t.Fatalf("commandJobFactory: %v", err)
	}

	err = rehydrated.Handle()
	if err == nil {
		t.Fatal("expected Handle to fail when factory is not registered")
	}
	if !strings.Contains(err.Error(), "no factory registered") {
		t.Fatalf("expected 'no factory registered' in error, got %q", err.Error())
	}
}

// TestDispatchAsync_DurableRoundTrip_MissingDefaultBus verifies the
// consumer side fails loud when the worker process has no default bus
// installed. The hydrated command has nowhere to dispatch to; pretending
// success would be the silent-drop hole.
func TestDispatchAsync_DurableRoundTrip_MissingDefaultBus(t *testing.T) {
	// Save and clear the default bus for the duration of this test so
	// the registry-lookup path runs but the dispatch path has nowhere
	// to route to.
	prev := getDefaultBus()
	defer SetDefaultBus(prev)

	// Make sure the factory IS registered, so the failure is specifically
	// the missing-bus case and not the missing-factory case.
	b := New()
	Register(b, func(cmd missingBusCmd) error { return nil })
	// New() called SetDefaultBus above. Clear it here to drive the
	// missing-bus error path.
	SetDefaultBus(nil)

	wire := []byte(`{"type":"bus.missingBusCmd","data":{}}`)
	rehydrated, err := commandJobFactory(wire)
	if err != nil {
		t.Fatalf("commandJobFactory: %v", err)
	}

	err = rehydrated.Handle()
	if err == nil {
		t.Fatal("expected Handle to fail when default bus is missing")
	}
	if !strings.Contains(err.Error(), "no default bus") {
		t.Fatalf("expected 'no default bus' in error, got %q", err.Error())
	}
}
