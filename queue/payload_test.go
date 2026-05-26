package queue

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
)

// crossProcessJob is a plain-data job whose Handle has an observable side
// effect (incrementing a registry-resolved atomic counter) so we can prove
// that the original handler logic ran on the consumer side, not a silent
// no-op stub. It deliberately has no closure / chan / func fields, mirroring
// the shape of a real production job.
type crossProcessJob struct {
	ID    string `json:"id"`
	Value int    `json:"value"`
}

// crossProcessRan counts how many times crossProcessJob.Handle has executed
// the real branch. Each registered factory grabs the producer-side pointer to
// this counter at registration time; if hydration silently substituted a stub
// (the pre-C-01 GenericJob path) the counter would stay at 0.
var crossProcessRan atomic.Int64

func (j *crossProcessJob) Handle() error {
	crossProcessRan.Add(1)
	return nil
}

func (j *crossProcessJob) Failed(err error) {}

// TestC01_DatabaseDriver_CrossProcessHydration is the headline regression
// test for finding C-01. It proves a job pushed via one DatabaseDriver
// instance can be popped and executed via a SECOND, independent
// DatabaseDriver instance that shares only the backing SQLite database (no
// shared package-global jobStore, no shared in-memory pointers).
//
// Before the fix, CreateJobWrapper stashed the live Job pointer in a
// package-level map keyed by an opaque job_id and wrote only that opaque
// id into Payload.Data. Because the registry/jobStore was process-local,
// a worker hydrating the wrapper from the DB row would miss the lookup and
// fall through to &GenericJob{}, whose Handle() returns nil. The worker
// reported success, deleted the row inside the same pop transaction (C-02
// territory), and the real job effects vanished without a trace.
//
// After the fix, the job is JSON-marshalled into Payload.Data at push time
// and reconstructed via the registry at pop time, so any worker on any
// process can run it.
func TestC01_DatabaseDriver_CrossProcessHydration(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)
	crossProcessRan.Store(0)

	// Register the factory exactly once, then construct TWO drivers against
	// the same backing DB. Each driver simulates a separate worker process.
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "crossProcessJob")
		registry.mu.Unlock()
	})
	RegisterJob(func(data []byte) (*crossProcessJob, error) {
		j := &crossProcessJob{}
		if len(data) == 0 {
			return j, nil
		}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	})

	producer, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// Re-use the same *sql.DB so the consumer sees the row, but build a
	// brand-new DatabaseDriver. This is the multi-process worker fleet model:
	// shared backing store, distinct process state. There is no shared in-
	// memory job pointer between producer and consumer, so cross-process
	// hydration MUST round-trip through the wire payload alone.
	consumer := NewDatabaseDriver(producer.db, "sqlite")

	original := &crossProcessJob{ID: "c01-proof", Value: 7}
	if err := producer.PushCtx(context.Background(), original, "c01-queue"); err != nil {
		t.Fatalf("push: %v", err)
	}

	popped, err := consumer.PopCtx(context.Background(), "c01-queue")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if popped == nil {
		t.Fatal("pop returned nil; the only producer push must surface to the consumer")
	}

	// The hydrated job must be the real type, not a fallback stub.
	cpj, ok := popped.(*crossProcessJob)
	if !ok {
		t.Fatalf("pop returned %T, want *crossProcessJob (pre-fix would have returned *queue.GenericJob)", popped)
	}
	if cpj.ID != "c01-proof" {
		t.Errorf("hydrated ID = %q, want %q (state did not survive the wire)", cpj.ID, "c01-proof")
	}
	if cpj.Value != 7 {
		t.Errorf("hydrated Value = %d, want 7 (state did not survive the wire)", cpj.Value)
	}

	// Execute Handle and assert the real branch ran. This is the
	// behavioural test: the pre-fix GenericJob.Handle returned nil and did
	// nothing, so the counter would stay at 0.
	if err := popped.Handle(); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := crossProcessRan.Load(); got != 1 {
		t.Fatalf("crossProcessJob.Handle ran %d times, want 1 (pre-fix GenericJob stub would yield 0)", got)
	}

	// Belt-and-suspenders: payload bytes themselves must contain real job
	// state, not the {"job_id":"..."} stub the pre-fix code wrote. We push
	// a second job and inspect the row directly.
	if err := producer.PushCtx(context.Background(), &crossProcessJob{ID: "wire-shape", Value: 99}, "c01-shape"); err != nil {
		t.Fatalf("push wire-shape: %v", err)
	}
	row := producer.db.QueryRow("SELECT payload FROM jobs WHERE queue = ?", "c01-shape")
	var rawPayload string
	if err := row.Scan(&rawPayload); err != nil {
		t.Fatalf("scan payload: %v", err)
	}
	var wrapperOnTheWire JobWrapper
	if err := json.Unmarshal([]byte(rawPayload), &wrapperOnTheWire); err != nil {
		t.Fatalf("unmarshal wire payload: %v", err)
	}
	if wrapperOnTheWire.Payload == nil {
		t.Fatal("wire payload has no Payload section")
	}
	// Decode the inner Data and assert it has the real fields. The pre-fix
	// code wrote {"job_id":"job_<N>"} here; the fix writes the job's own
	// JSON.
	var inner crossProcessJob
	if err := json.Unmarshal(wrapperOnTheWire.Payload.Data, &inner); err != nil {
		t.Fatalf("unmarshal inner data %q: %v", string(wrapperOnTheWire.Payload.Data), err)
	}
	if inner.ID != "wire-shape" || inner.Value != 99 {
		t.Fatalf("wire payload Data did not carry job state: got %+v (raw=%s)", inner, string(wrapperOnTheWire.Payload.Data))
	}
}

// TestC01_DatabaseDriver_UnregisteredJobReturnsError documents the
// post-fix failure mode: a payload whose Type is not in the registry no
// longer silently degrades to a no-op GenericJob. It surfaces an error so
// the worker can route the row to failed_jobs / JobFailed observers.
func TestC01_DatabaseDriver_UnregisteredJobReturnsError(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// Push a TestJob, then steal its registry entry so the consumer side
	// cannot resolve it. (We restore at the end via t.Cleanup so we do not
	// disturb sibling tests that rely on the *queue.TestJob registration.)
	original := func(data []byte) (Job, error) {
		j := &TestJob{}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	}
	t.Cleanup(func() {
		Register("*queue.TestJob", original)
	})

	if err := driver.PushCtx(context.Background(), &TestJob{ID: "no-registry"}, "no-registry-queue"); err != nil {
		t.Fatalf("push: %v", err)
	}
	// Remove the registry entry to simulate a worker that has not registered
	// this job type.
	registry.mu.Lock()
	delete(registry.handlers, "TestJob")
	registry.mu.Unlock()

	job, err := driver.PopCtx(context.Background(), "no-registry-queue")
	if err == nil {
		t.Fatalf("pop returned no error; the pre-fix code silently produced a GenericJob stub (job=%T)", job)
	}
	if job != nil {
		t.Errorf("pop returned non-nil job alongside hydration error: %T", job)
	}
}
