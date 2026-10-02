package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// admitProbeJob's optional methods read the receiver, as a user job's do:
// on a typed nil each one panics, so an entry point that calls one before
// refusing the job panics instead of returning ErrNilJob. It does not
// implement Identifiable, so the memory driver warns for its type.
type admitProbeJob struct {
	Queue   string
	BatchID BatchID
}

func (j *admitProbeJob) Handle() error                { return nil }
func (j *admitProbeJob) Failed(error)                 {}
func (j *admitProbeJob) OnQueue() string              { return j.Queue }
func (j *admitProbeJob) GetBatchID() BatchID          { return j.BatchID }
func (j *admitProbeJob) SetBatchID(id BatchID)        { j.BatchID = id }
func (j *admitProbeJob) MaxAttempts() int             { return len(j.Queue) + 1 }
func (j *admitProbeJob) Delay() string                { return j.Queue }
func (j *admitProbeJob) String() string               { return j.Queue }
func (j *admitProbeJob) MarshalJSON() ([]byte, error) { return []byte(`"` + j.Queue + `"`), nil }

// countingRepo counts the batches saved through it.
type countingRepo struct {
	BatchRepository
	saves atomic.Int32
}

func (r *countingRepo) Save(ctx context.Context, b *Batch) error {
	r.saves.Add(1)
	return r.BatchRepository.Save(ctx, b)
}

func installCountingRepo(t *testing.T) *countingRepo {
	t.Helper()
	prev := DefaultBatchRepository()
	repo := &countingRepo{BatchRepository: NewInMemoryBatchRepository()}
	SetDefaultBatchRepository(repo)
	t.Cleanup(func() { SetDefaultBatchRepository(prev) })
	return repo
}

// A refused push leaves no trace on the memory driver: the typed nil's
// type gets no warning, so the first admitted job of that type still
// gets the once-per-type warning.
func TestMemoryDriver_RefusedNilPushConsumesNoWarning(t *testing.T) {
	ctx := context.Background()
	pushes := map[string]func(d *MemoryDriver, job Job) error{
		"PushCtx":            func(d *MemoryDriver, job Job) error { return d.PushCtx(ctx, job) },
		"PushDelayedCtx":     func(d *MemoryDriver, job Job) error { return d.PushDelayedCtx(ctx, job, 0) },
		"PushIfNotExistsCtx": func(d *MemoryDriver, job Job) error { return d.PushIfNotExistsCtx(ctx, job, "k") },
	}
	for name, push := range pushes {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var lines [][]any
			d := NewMemoryDriver()
			d.SetLogger(pairLogger{mu: &mu, lines: &lines})
			t.Cleanup(func() { _ = d.Shutdown(ctx) })

			if err := push(d, (*admitProbeJob)(nil)); !errors.Is(err, ErrNilJob) {
				t.Fatalf("%s(typed nil) = %v, want ErrNilJob", name, err)
			}
			mu.Lock()
			n := len(lines)
			mu.Unlock()
			if n != 0 {
				t.Fatalf("refused push logged %d lines, want 0: %v", n, lines)
			}
			if size, _ := d.Size("default"); size != 0 {
				t.Fatalf("Size(default) = %d after the refused push, want 0", size)
			}

			if err := push(d, &admitProbeJob{}); err != nil {
				t.Fatalf("%s(job) = %v", name, err)
			}
			mu.Lock()
			n = len(lines)
			mu.Unlock()
			if n != 1 {
				t.Fatalf("admitted push logged %d lines, want the 1 once-per-type warning", n)
			}
		})
	}
}

// plainBatchJob does not implement Batchable: the worker could never
// settle a batch holding it.
type plainBatchJob struct{ ID string }

func (j *plainBatchJob) Handle() error { return nil }
func (j *plainBatchJob) Failed(error)  {}

// A batch holding a nil job, untyped or a typed nil Batchable, or a job
// that does not implement Batchable, is refused whole before it exists:
// nothing saved, nothing pushed, no job before it stamped with a batch
// id, no callback fired, no panic.
func TestPendingBatch_Dispatch_RefusedWhole(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		bad  Job
		want error
	}{
		{"untyped nil", nil, ErrNilJob},
		{"typed nil Batchable", (*admitProbeJob)(nil), ErrNilJob},
		{"not Batchable", &plainBatchJob{ID: "p"}, ErrJobNotBatchable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := installCountingRepo(t)
			d := NewMemoryDriver()
			t.Cleanup(func() { _ = d.Shutdown(ctx) })
			first := &admitProbeJob{}
			var fired atomic.Int32
			b, err := NewBatch(first, tc.bad, &admitProbeJob{}).
				OnQueue("q-batch").
				Then(func(*Batch) { fired.Add(1) }).
				Catch(func(*Batch, error) { fired.Add(1) }).
				Finally(func(*Batch) { fired.Add(1) }).
				Dispatch(ctx, d)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Dispatch = %v, want %v", err, tc.want)
			}
			if b != nil {
				t.Errorf("Dispatch returned batch %v for a refused batch", b.ID())
			}
			if n := repo.saves.Load(); n != 0 {
				t.Errorf("refused batch saved %d times, want 0", n)
			}
			for _, q := range []string{"q-batch", "default"} {
				if size, _ := d.Size(q); size != 0 {
					t.Errorf("Size(%q) = %d after the refused batch, want 0", q, size)
				}
			}
			if first.BatchID != "" {
				t.Errorf("job before the nil was stamped with %q", first.BatchID)
			}
			if n := fired.Load(); n != 0 {
				t.Errorf("refused batch fired %d callbacks, want 0", n)
			}
		})
	}
}

// A batch of only non-Batchable jobs is refused too: accepted, it would
// run every job and never settle, so Then and Finally would never fire.
func TestPendingBatch_Dispatch_NoBatchableJobRefused(t *testing.T) {
	ctx := context.Background()
	repo := installCountingRepo(t)
	d := NewMemoryDriver()
	t.Cleanup(func() { _ = d.Shutdown(ctx) })
	_, err := NewBatch(&plainBatchJob{ID: "a"}, &plainBatchJob{ID: "b"}).Dispatch(ctx, d)
	if !errors.Is(err, ErrJobNotBatchable) {
		t.Fatalf("Dispatch = %v, want ErrJobNotBatchable", err)
	}
	if n := repo.saves.Load(); n != 0 {
		t.Errorf("refused batch saved %d times, want 0", n)
	}
	if size, _ := d.Size("default"); size != 0 {
		t.Errorf("Size(default) = %d after the refused batch, want 0", size)
	}
}

// Concurrent pushes of typed nils and of jobs on one memory driver: every
// nil is refused, every job is stored, and the job type's warning is
// written once.
func TestMemoryDriver_NilPush_Concurrent(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var lines [][]any
	d := NewMemoryDriver()
	d.SetLogger(pairLogger{mu: &mu, lines: &lines})
	t.Cleanup(func() { _ = d.Shutdown(ctx) })

	const callers, each = 16, 50
	var wg sync.WaitGroup
	var refused, stored atomic.Int32
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				if i%2 == 0 {
					if err := d.PushCtx(ctx, (*admitProbeJob)(nil)); errors.Is(err, ErrNilJob) {
						refused.Add(1)
					}
					continue
				}
				if err := d.PushCtx(ctx, &admitProbeJob{Queue: "q-concurrent"}); err == nil {
					stored.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if got, want := refused.Load(), int32(callers/2*each); got != want {
		t.Errorf("refused = %d, want %d", got, want)
	}
	if got, want := stored.Load(), int32(callers/2*each); got != want {
		t.Errorf("stored = %d, want %d", got, want)
	}
	if size, _ := d.Size("q-concurrent"); size != int64(callers/2*each) {
		t.Errorf("Size = %d, want %d", size, callers/2*each)
	}
	if size, _ := d.Size("default"); size != 0 {
		t.Errorf("Size(default) = %d, want 0", size)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 {
		t.Errorf("warnings = %d, want 1", len(lines))
	}
}
