package bus

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/queue"
)

// roundTripCmd, durableCmd, registryCmd, unregisteredCmd, contamCmdA,
// and contamCmdB are command types the round-trip tests use exclusively.
// They must NOT collide with the names used elsewhere in the bus tests.
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

type contamCmdA struct {
	Value string
}

type contamCmdB struct {
	Value string
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
	defer b.Close()
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
// reconstruct the command via the per-bus factory registry on the bus
// identified by the payload's BusID.
func TestDispatchAsync_DurableRoundTrip(t *testing.T) {
	b := New()
	defer b.Close()
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

	// Sanity check: the marshalled form must carry bus_id, type, and data.
	// A `{}` payload would be the original X-01 bug. A payload missing
	// bus_id would be the contamination follow-up bug.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(wireBytes, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	for _, key := range []string{"bus_id", "type", "data"} {
		if _, ok := probe[key]; !ok {
			t.Fatalf("commandJob JSON missing %q field: %s", key, string(wireBytes))
		}
	}

	rehydrated, err := commandJobFactory(wireBytes)
	if err != nil {
		t.Fatalf("commandJobFactory: %v", err)
	}
	if rehydrated.cmd != nil || rehydrated.bus != nil {
		t.Fatal("hydrated commandJob must not carry in-process pointers")
	}
	if rehydrated.BusID != b.ID() {
		t.Fatalf("hydrated BusID = %q, want %q", rehydrated.BusID, b.ID())
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
	defer b.Close()
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
// side refuses to enqueue a command type that has no factory registered
// on THIS bus. The check is per-bus, not global: a sibling bus holding a
// factory for the same type does not unlock dispatch on the bus that
// lacks it.
func TestDispatchAsync_UnregisteredCommand_FailsLoud(t *testing.T) {
	b := New()
	defer b.Close()
	mock := &mockQueuePusher{}
	b.SetQueue(mock)

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
// resolved bus does not know.
func TestDispatchAsync_DurableRoundTrip_MissingFactory(t *testing.T) {
	b := New()
	defer b.Close()
	// Hand-craft a payload that names this bus's id but references a
	// command type the bus has no factory for. This simulates a worker
	// process running an older or different handler set than the producer.
	wire := []byte(`{"bus_id":"` + b.ID() + `","type":"bus.ghostCmd","data":{}}`)
	rehydrated, err := commandJobFactory(wire)
	if err != nil {
		t.Fatalf("commandJobFactory: %v", err)
	}

	err = rehydrated.Handle()
	if err == nil {
		t.Fatal("expected Handle to fail when factory is not registered on the bus")
	}
	if !strings.Contains(err.Error(), "no factory registered") {
		t.Fatalf("expected 'no factory registered' in error, got %q", err.Error())
	}
}

// TestDispatchAsync_DurableRoundTrip_UnknownBus verifies the consumer side
// fails loud when a payload arrives with a BusID no bus in this process
// is registered under. This is the cross-process-restart / wrong-worker
// scenario: pretending success would silently drop the work.
func TestDispatchAsync_DurableRoundTrip_UnknownBus(t *testing.T) {
	wire := []byte(`{"bus_id":"00000000-0000-0000-0000-000000000000","type":"bus.contamCmdA","data":{}}`)
	rehydrated, err := commandJobFactory(wire)
	if err != nil {
		t.Fatalf("commandJobFactory: %v", err)
	}

	err = rehydrated.Handle()
	if err == nil {
		t.Fatal("expected Handle to fail when BusID is not registered")
	}
	if !strings.Contains(err.Error(), "no bus registered for id") {
		t.Fatalf("expected 'no bus registered for id' in error, got %q", err.Error())
	}
}

// TestDispatchAsync_DurableRoundTrip_MissingBusID verifies the consumer
// side fails loud when a legacy or corrupted payload arrives without a
// bus_id. A pre-X-01-follow-up payload would have looked like this.
func TestDispatchAsync_DurableRoundTrip_MissingBusID(t *testing.T) {
	wire := []byte(`{"type":"bus.contamCmdA","data":{}}`)
	rehydrated, err := commandJobFactory(wire)
	if err != nil {
		t.Fatalf("commandJobFactory: %v", err)
	}

	err = rehydrated.Handle()
	if err == nil {
		t.Fatal("expected Handle to fail when bus_id is missing")
	}
	if !strings.Contains(err.Error(), "missing bus_id") {
		t.Fatalf("expected 'missing bus_id' in error, got %q", err.Error())
	}
}

// TestDispatchAsync_TwoBusesDisjointHandlers verifies the reviewer's
// primary contamination scenario: two buses each hold a disjoint set of
// command handlers. A command dispatched via bus A enqueues and rehydrates
// through bus A's handler ONLY, even though bus B exists in the same
// process. This is the regression the per-bus registry closes.
func TestDispatchAsync_TwoBusesDisjointHandlers(t *testing.T) {
	busA := New()
	defer busA.Close()
	busB := New()
	defer busB.Close()

	var aGot contamCmdA
	var bGot contamCmdB
	var aCount, bCount int
	var mu sync.Mutex

	Register(busA, func(cmd contamCmdA) error {
		mu.Lock()
		aGot = cmd
		aCount++
		mu.Unlock()
		return nil
	})
	Register(busB, func(cmd contamCmdB) error {
		mu.Lock()
		bGot = cmd
		bCount++
		mu.Unlock()
		return nil
	})

	mockA := &mockQueuePusher{}
	busA.SetQueue(mockA)
	mockB := &mockQueuePusher{}
	busB.SetQueue(mockB)

	if err := busA.DispatchAsync(contamCmdA{Value: "from-A"}); err != nil {
		t.Fatalf("busA.DispatchAsync: %v", err)
	}
	if err := busB.DispatchAsync(contamCmdB{Value: "from-B"}); err != nil {
		t.Fatalf("busB.DispatchAsync: %v", err)
	}

	// Force JSON round-trip on both jobs to drop the in-process fast
	// path. The BusID is now the only thing tying the payload back to
	// the originating bus.
	for _, pair := range []struct {
		mock *mockQueuePusher
		bus  *Bus
	}{{mockA, busA}, {mockB, busB}} {
		pair.mock.mu.Lock()
		pushed := pair.mock.jobs[0]
		pair.mock.mu.Unlock()

		wire, err := json.Marshal(pushed)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		hydrated, err := commandJobFactory(wire)
		if err != nil {
			t.Fatalf("commandJobFactory: %v", err)
		}
		if hydrated.BusID != pair.bus.ID() {
			t.Fatalf("hydrated BusID = %q, want %q", hydrated.BusID, pair.bus.ID())
		}
		if err := hydrated.Handle(); err != nil {
			t.Fatalf("hydrated Handle: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if aCount != 1 || aGot.Value != "from-A" {
		t.Fatalf("busA handler: count=%d got=%+v", aCount, aGot)
	}
	if bCount != 1 || bGot.Value != "from-B" {
		t.Fatalf("busB handler: count=%d got=%+v", bCount, bGot)
	}
}

// TestDispatchAsync_CrossBusContamination_ExplicitlyFails covers the
// reviewer's "cross-bus contamination" test surface directly: bus A
// dispatches a command type bus A knows, bus B exists but DOES NOT know
// that command. If a worker process accidentally re-routes the payload
// through bus B (because BusID was missing or the registry was global),
// bus B's resolution must fail loud. The check is that swapping BusID
// to bus B's id on the wire makes Handle reject the job, not silently
// route through bus A's handler.
func TestDispatchAsync_CrossBusContamination_ExplicitlyFails(t *testing.T) {
	busA := New()
	defer busA.Close()
	busB := New()
	defer busB.Close()

	// Both buses have a handler for the same command type but with
	// observably different behavior. If contamination leaks, the wrong
	// handler fires and the test sees the wrong sentinel.
	aFired := false
	bFired := false
	Register(busA, func(cmd contamCmdA) error {
		aFired = true
		return nil
	})
	Register(busB, func(cmd contamCmdA) error {
		bFired = true
		return errors.New("bus B handler should NEVER fire for bus A's dispatch")
	})

	mock := &mockQueuePusher{}
	busA.SetQueue(mock)

	if err := busA.DispatchAsync(contamCmdA{Value: "owned-by-A"}); err != nil {
		t.Fatalf("busA.DispatchAsync: %v", err)
	}

	mock.mu.Lock()
	pushed := mock.jobs[0]
	mock.mu.Unlock()

	wire, err := json.Marshal(pushed)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	hydrated, err := commandJobFactory(wire)
	if err != nil {
		t.Fatalf("commandJobFactory: %v", err)
	}
	if hydrated.BusID != busA.ID() {
		t.Fatalf("BusID = %q, want %q", hydrated.BusID, busA.ID())
	}

	// Tamper with the BusID on the wire to point at bus B. If the
	// resolution path ever falls back to a global registry, bus B's
	// handler fires and the test catches the regression.
	tampered := bytes_replace(wire, []byte(`"bus_id":"`+busA.ID()+`"`), []byte(`"bus_id":"`+busB.ID()+`"`))
	if string(tampered) == string(wire) {
		t.Fatal("test setup error: BusID substitution did not change the payload")
	}

	hydratedTampered, err := commandJobFactory(tampered)
	if err != nil {
		t.Fatalf("commandJobFactory(tampered): %v", err)
	}
	// Bus B has a handler for contamCmdA. The dispatch will succeed,
	// but it MUST go through bus B's handler (which is wrong from the
	// producer's standpoint). The point of the test is to confirm the
	// routing is fully governed by BusID and does NOT fall back to the
	// originating bus regardless of who registered which handler.
	err = hydratedTampered.Handle()
	if err == nil {
		t.Fatal("expected tampered payload to fire bus B's error-returning handler")
	}
	if !bFired {
		t.Fatal("bus B's handler did not fire after BusID swap, routing is broken")
	}
	if aFired {
		t.Fatal("bus A's handler fired despite BusID pointing at bus B, contamination still present")
	}

	// Reset and verify the untampered payload routes correctly through
	// bus A, with bus B's handler untouched.
	aFired = false
	bFired = false
	hydratedClean, err := commandJobFactory(wire)
	if err != nil {
		t.Fatalf("commandJobFactory(clean): %v", err)
	}
	if err := hydratedClean.Handle(); err != nil {
		t.Fatalf("clean Handle: %v", err)
	}
	if !aFired {
		t.Fatal("bus A's handler did not fire for bus A's own dispatch")
	}
	if bFired {
		t.Fatal("bus B's handler fired for bus A's dispatch, contamination present")
	}
}

// TestNew_DoesNotOverwriteSibling verifies the reviewer's second test
// requirement: bus.New() must NOT overwrite previously-constructed
// buses in any global pointer. After the fix, two New() calls produce
// two distinct buses, both reachable by their own ids, with neither
// shadowing the other.
func TestNew_DoesNotOverwriteSibling(t *testing.T) {
	first := New()
	defer first.Close()
	firstID := first.ID()

	second := New()
	defer second.Close()
	secondID := second.ID()

	if firstID == secondID {
		t.Fatal("two New() calls produced the same bus id")
	}

	// Both must remain discoverable through the package registry.
	resolved1, ok := lookupBus(firstID)
	if !ok || resolved1 != first {
		t.Fatalf("first bus not in registry after second New(): ok=%v, resolved=%p, want=%p", ok, resolved1, first)
	}
	resolved2, ok := lookupBus(secondID)
	if !ok || resolved2 != second {
		t.Fatalf("second bus not in registry: ok=%v, resolved=%p, want=%p", ok, resolved2, second)
	}
}

// TestNewWithID_StableIdentity verifies the cross-process coordination
// path: NewWithID(name) lets the producer and consumer agree on a stable
// id even when their UUIDs would otherwise differ.
func TestNewWithID_StableIdentity(t *testing.T) {
	b := NewWithID("orders-bus")
	defer b.Close()

	if b.ID() != "orders-bus" {
		t.Fatalf("ID() = %q, want %q", b.ID(), "orders-bus")
	}
	resolved, ok := lookupBus("orders-bus")
	if !ok || resolved != b {
		t.Fatalf("lookup orders-bus: ok=%v, resolved=%p, want=%p", ok, resolved, b)
	}
}

// TestNewWithID_RejectsDuplicate ensures two buses cannot claim the same
// id. A duplicate id would silently route some jobs through the wrong
// bus's factory registry, so registration panics synchronously.
func TestNewWithID_RejectsDuplicate(t *testing.T) {
	first := NewWithID("dup-test")
	defer first.Close()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate bus id")
		}
		regErr, ok := r.(*contract.RegistrationError)
		if !ok {
			t.Fatalf("expected *contract.RegistrationError, got %T: %v", r, r)
		}
		if regErr.Package != "bus" {
			t.Errorf("expected package=bus, got %q", regErr.Package)
		}
		if !strings.Contains(regErr.Error(), "already registered") {
			t.Errorf("expected 'already registered' in error, got %q", regErr.Error())
		}
	}()

	_ = NewWithID("dup-test")
}

// TestNewWithID_RejectsEmpty ensures empty bus ids are rejected. An
// empty id would collide with the missing-bus_id legacy payload check
// and route all such payloads to a single bus, defeating isolation.
func TestNewWithID_RejectsEmpty(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on empty bus id")
		}
		if _, ok := r.(*contract.RegistrationError); !ok {
			t.Fatalf("expected *contract.RegistrationError, got %T: %v", r, r)
		}
	}()
	_ = NewWithID("")
}

// TestClose_RemovesFromRegistry verifies the bus is removed from the
// package registry on Close, and that subsequent lookups for the same
// id fail.
func TestClose_RemovesFromRegistry(t *testing.T) {
	b := NewWithID("close-test")
	if _, ok := lookupBus("close-test"); !ok {
		t.Fatal("bus not in registry before Close")
	}
	b.Close()
	if _, ok := lookupBus("close-test"); ok {
		t.Fatal("bus still in registry after Close")
	}
}

// bytes_replace is a local helper for byte-slice substitution. Avoids
// pulling in bytes just for one call.
func bytes_replace(src, old, new []byte) []byte {
	// strings.Replace via the string conversion is fine for ASCII
	// JSON payloads in tests.
	return []byte(strings.Replace(string(src), string(old), string(new), -1))
}
