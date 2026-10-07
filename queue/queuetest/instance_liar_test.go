package queuetest

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// The drivers below answer true to PreservesJobInstance and break the
// promise on one path each. They wrap the memory driver, which keeps it, so
// every other path is honest: the contract has to find the one that is not.

// rebuildJob returns what a driver that stores payloads delivers: a new job
// rebuilt from the marshalled state of job.
func rebuildJob(job queue.Job) (queue.Job, error) {
	payload, err := queue.MarshalJob(job, "")
	if err != nil {
		return nil, err
	}
	return queue.HydrateJob(payload)
}

// jobSet is a concurrency-safe set of job values.
type jobSet struct {
	mu   sync.Mutex
	jobs map[queue.Job]struct{}
}

func (s *jobSet) add(job queue.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = make(map[queue.Job]struct{})
	}
	s.jobs[job] = struct{}{}
}

func (s *jobSet) has(job queue.Job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.jobs[job]
	return ok
}

// dedupeReservedLiar rebuilds a job accepted through PushIfNotExistsCtx,
// only when a reserving pop consumes it.
type dedupeReservedLiar struct {
	*queue.MemoryDriver
	deduped jobSet
}

func (d *dedupeReservedLiar) PushIfNotExistsCtx(ctx context.Context, job queue.Job, key string, q ...string) error {
	if err := d.MemoryDriver.PushIfNotExistsCtx(ctx, job, key, q...); err != nil {
		return err
	}
	d.deduped.add(job)
	return nil
}

func (d *dedupeReservedLiar) PopCtxReserved(ctx context.Context, q string) (queue.Job, queue.ReservationToken, queue.TraceContext, error) {
	job, token, tc, err := d.MemoryDriver.PopCtxReserved(ctx, q)
	if err != nil || job == nil || !d.deduped.has(job) {
		return job, token, tc, err
	}
	rebuilt, err := rebuildJob(job)
	return rebuilt, token, tc, err
}

// releasedPlainPopLiar rebuilds a job whose reservation was released, only
// when a pop that does not reserve consumes it.
type releasedPlainPopLiar struct {
	*queue.MemoryDriver
	mu       sync.Mutex
	reserved map[int64]queue.Job
	released jobSet
}

func (d *releasedPlainPopLiar) PopCtxReserved(ctx context.Context, q string) (queue.Job, queue.ReservationToken, queue.TraceContext, error) {
	job, token, tc, err := d.MemoryDriver.PopCtxReserved(ctx, q)
	if err == nil && job != nil {
		d.mu.Lock()
		if d.reserved == nil {
			d.reserved = make(map[int64]queue.Job)
		}
		d.reserved[token.ID] = job
		d.mu.Unlock()
	}
	return job, token, tc, err
}

func (d *releasedPlainPopLiar) ReleaseCtx(ctx context.Context, token queue.ReservationToken, delay time.Duration) error {
	if err := d.MemoryDriver.ReleaseCtx(ctx, token, delay); err != nil {
		return err
	}
	d.mu.Lock()
	job := d.reserved[token.ID]
	d.mu.Unlock()
	if job != nil {
		d.released.add(job)
	}
	return nil
}

func (d *releasedPlainPopLiar) PopCtxWithTrace(ctx context.Context, q string) (queue.Job, queue.TraceContext, error) {
	job, tc, err := d.MemoryDriver.PopCtxWithTrace(ctx, q)
	if err != nil || job == nil || !d.released.has(job) {
		return job, tc, err
	}
	rebuilt, err := rebuildJob(job)
	return rebuilt, tc, err
}

func (d *releasedPlainPopLiar) PopCtx(ctx context.Context, q string) (queue.Job, error) {
	job, _, err := d.PopCtxWithTrace(ctx, q)
	return job, err
}

// snapshotRestoreLiar hands back the pushed pointer, after writing the
// state it marshalled at push time back over it: pointer identity and
// unexported state survive, exported state changed since the push does not.
type snapshotRestoreLiar struct {
	*queue.MemoryDriver
	mu        sync.Mutex
	snapshots map[queue.Job][]byte
}

func (d *snapshotRestoreLiar) snapshot(job queue.Job) {
	data, err := json.Marshal(job)
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.snapshots == nil {
		d.snapshots = make(map[queue.Job][]byte)
	}
	d.snapshots[job] = data
}

func (d *snapshotRestoreLiar) restore(job queue.Job) {
	if job == nil {
		return
	}
	d.mu.Lock()
	data, ok := d.snapshots[job]
	d.mu.Unlock()
	if ok {
		_ = json.Unmarshal(data, job)
	}
}

func (d *snapshotRestoreLiar) PushCtx(ctx context.Context, job queue.Job, q ...string) error {
	if err := d.MemoryDriver.PushCtx(ctx, job, q...); err != nil {
		return err
	}
	d.snapshot(job)
	return nil
}

func (d *snapshotRestoreLiar) PushDelayedCtx(ctx context.Context, job queue.Job, delay time.Duration, q ...string) error {
	if err := d.MemoryDriver.PushDelayedCtx(ctx, job, delay, q...); err != nil {
		return err
	}
	d.snapshot(job)
	return nil
}

func (d *snapshotRestoreLiar) PushIfNotExistsCtx(ctx context.Context, job queue.Job, key string, q ...string) error {
	if err := d.MemoryDriver.PushIfNotExistsCtx(ctx, job, key, q...); err != nil {
		return err
	}
	d.snapshot(job)
	return nil
}

func (d *snapshotRestoreLiar) PopCtx(ctx context.Context, q string) (queue.Job, error) {
	job, err := d.MemoryDriver.PopCtx(ctx, q)
	d.restore(job)
	return job, err
}

func (d *snapshotRestoreLiar) PopCtxWithTrace(ctx context.Context, q string) (queue.Job, queue.TraceContext, error) {
	job, tc, err := d.MemoryDriver.PopCtxWithTrace(ctx, q)
	d.restore(job)
	return job, tc, err
}

func (d *snapshotRestoreLiar) PopCtxReserved(ctx context.Context, q string) (queue.Job, queue.ReservationToken, queue.TraceContext, error) {
	job, token, tc, err := d.MemoryDriver.PopCtxReserved(ctx, q)
	d.restore(job)
	return job, token, tc, err
}

// shutdownFlipLiar changes its answer when Shutdown starts.
type shutdownFlipLiar struct {
	*queue.MemoryDriver
	closing atomic.Bool
}

func (d *shutdownFlipLiar) PreservesJobInstance() bool { return !d.closing.Load() }

func (d *shutdownFlipLiar) Shutdown(ctx context.Context) error {
	d.closing.Store(true)
	return d.MemoryDriver.Shutdown(ctx)
}

// dedupeReleaseLiar rebuilds a released job on its next delivery, only when
// the job first came in through PushIfNotExistsCtx.
type dedupeReleaseLiar struct {
	*queue.MemoryDriver
	mu       sync.Mutex
	reserved map[int64]queue.Job
	deduped  jobSet
	stale    jobSet
}

func (d *dedupeReleaseLiar) PushIfNotExistsCtx(ctx context.Context, job queue.Job, key string, q ...string) error {
	if err := d.MemoryDriver.PushIfNotExistsCtx(ctx, job, key, q...); err != nil {
		return err
	}
	d.deduped.add(job)
	return nil
}

func (d *dedupeReleaseLiar) deliver(job queue.Job, err error) (queue.Job, error) {
	if err != nil || job == nil || !d.stale.has(job) {
		return job, err
	}
	return rebuildJob(job)
}

func (d *dedupeReleaseLiar) PopCtxReserved(ctx context.Context, q string) (queue.Job, queue.ReservationToken, queue.TraceContext, error) {
	job, token, tc, err := d.MemoryDriver.PopCtxReserved(ctx, q)
	if err == nil && job != nil {
		d.mu.Lock()
		if d.reserved == nil {
			d.reserved = make(map[int64]queue.Job)
		}
		d.reserved[token.ID] = job
		d.mu.Unlock()
	}
	job, err = d.deliver(job, err)
	return job, token, tc, err
}

func (d *dedupeReleaseLiar) ReleaseCtx(ctx context.Context, token queue.ReservationToken, delay time.Duration) error {
	if err := d.MemoryDriver.ReleaseCtx(ctx, token, delay); err != nil {
		return err
	}
	d.mu.Lock()
	job := d.reserved[token.ID]
	d.mu.Unlock()
	if job != nil && d.deduped.has(job) {
		d.stale.add(job)
	}
	return nil
}

func (d *dedupeReleaseLiar) PopCtxWithTrace(ctx context.Context, q string) (queue.Job, queue.TraceContext, error) {
	job, tc, err := d.MemoryDriver.PopCtxWithTrace(ctx, q)
	job, err = d.deliver(job, err)
	return job, tc, err
}

func (d *dedupeReleaseLiar) PopCtx(ctx context.Context, q string) (queue.Job, error) {
	job, _, err := d.PopCtxWithTrace(ctx, q)
	return job, err
}

// stalePrivateStateLiar hands back the pushed pointer with its exported
// state as it is now, after putting back the unexported state the job had
// when it was pushed.
type stalePrivateStateLiar struct {
	*queue.MemoryDriver
	mu    sync.Mutex
	lives map[*instanceJob]func() string
}

func (d *stalePrivateStateLiar) snapshot(job queue.Job) {
	ij, ok := job.(*instanceJob)
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lives == nil {
		d.lives = make(map[*instanceJob]func() string)
	}
	d.lives[ij] = ij.live
}

func (d *stalePrivateStateLiar) restore(job queue.Job) {
	ij, ok := job.(*instanceJob)
	if !ok {
		return
	}
	d.mu.Lock()
	live, ok := d.lives[ij]
	d.mu.Unlock()
	if ok {
		ij.live = live
	}
}

func (d *stalePrivateStateLiar) PushCtx(ctx context.Context, job queue.Job, q ...string) error {
	if err := d.MemoryDriver.PushCtx(ctx, job, q...); err != nil {
		return err
	}
	d.snapshot(job)
	return nil
}

func (d *stalePrivateStateLiar) PushDelayedCtx(ctx context.Context, job queue.Job, delay time.Duration, q ...string) error {
	if err := d.MemoryDriver.PushDelayedCtx(ctx, job, delay, q...); err != nil {
		return err
	}
	d.snapshot(job)
	return nil
}

func (d *stalePrivateStateLiar) PushIfNotExistsCtx(ctx context.Context, job queue.Job, key string, q ...string) error {
	if err := d.MemoryDriver.PushIfNotExistsCtx(ctx, job, key, q...); err != nil {
		return err
	}
	d.snapshot(job)
	return nil
}

func (d *stalePrivateStateLiar) PopCtx(ctx context.Context, q string) (queue.Job, error) {
	job, err := d.MemoryDriver.PopCtx(ctx, q)
	d.restore(job)
	return job, err
}

func (d *stalePrivateStateLiar) PopCtxWithTrace(ctx context.Context, q string) (queue.Job, queue.TraceContext, error) {
	job, tc, err := d.MemoryDriver.PopCtxWithTrace(ctx, q)
	d.restore(job)
	return job, tc, err
}

func (d *stalePrivateStateLiar) PopCtxReserved(ctx context.Context, q string) (queue.Job, queue.ReservationToken, queue.TraceContext, error) {
	job, token, tc, err := d.MemoryDriver.PopCtxReserved(ctx, q)
	d.restore(job)
	return job, token, tc, err
}

// boundedHonestDriver keeps the promise and holds at most capacity jobs: a
// push with no room waits until a pop makes room or its context ends. It
// stands for an honest driver that is not the memory driver: bounded, and
// with pushes that can block. Shutdown does not wake a waiting push.
type boundedHonestDriver struct {
	*queue.MemoryDriver
	slots chan struct{}
}

func newBoundedHonestDriver(m *queue.MemoryDriver, capacity int) *boundedHonestDriver {
	return &boundedHonestDriver{MemoryDriver: m, slots: make(chan struct{}, capacity)}
}

func (d *boundedHonestDriver) acquire(ctx context.Context) error {
	select {
	case d.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *boundedHonestDriver) free() {
	select {
	case <-d.slots:
	default:
	}
}

func (d *boundedHonestDriver) PushCtx(ctx context.Context, job queue.Job, q ...string) error {
	if err := d.acquire(ctx); err != nil {
		return err
	}
	if err := d.MemoryDriver.PushCtx(ctx, job, q...); err != nil {
		d.free()
		return err
	}
	return nil
}

func (d *boundedHonestDriver) PushDelayedCtx(ctx context.Context, job queue.Job, delay time.Duration, q ...string) error {
	if err := d.acquire(ctx); err != nil {
		return err
	}
	if err := d.MemoryDriver.PushDelayedCtx(ctx, job, delay, q...); err != nil {
		d.free()
		return err
	}
	return nil
}

func (d *boundedHonestDriver) PushIfNotExistsCtx(ctx context.Context, job queue.Job, key string, q ...string) error {
	if err := d.acquire(ctx); err != nil {
		return err
	}
	if err := d.MemoryDriver.PushIfNotExistsCtx(ctx, job, key, q...); err != nil {
		d.free()
		return err
	}
	return nil
}

func (d *boundedHonestDriver) PopCtx(ctx context.Context, q string) (queue.Job, error) {
	job, err := d.MemoryDriver.PopCtx(ctx, q)
	if job != nil {
		d.free()
	}
	return job, err
}

func (d *boundedHonestDriver) PopCtxWithTrace(ctx context.Context, q string) (queue.Job, queue.TraceContext, error) {
	job, tc, err := d.MemoryDriver.PopCtxWithTrace(ctx, q)
	if job != nil {
		d.free()
	}
	return job, tc, err
}

// AckCtx frees the slot of a reserved job. PopCtxReserved and ReleaseCtx
// keep it: a reserved or released job still belongs to the driver.
func (d *boundedHonestDriver) AckCtx(ctx context.Context, token queue.ReservationToken) error {
	err := d.MemoryDriver.AckCtx(ctx, token)
	if err == nil && !token.IsZero() {
		d.free()
	}
	return err
}

func (d *boundedHonestDriver) FailReservedCtx(ctx context.Context, token queue.ReservationToken, job queue.Job, jobErr error, q string) error {
	err := d.MemoryDriver.FailReservedCtx(ctx, token, job, jobErr, q)
	if !token.IsZero() {
		d.free()
	}
	return err
}

func (d *boundedHonestDriver) Clear(q string) error {
	err := d.MemoryDriver.Clear(q)
	for len(d.slots) > 0 {
		d.free()
	}
	return err
}

// newLiarMemory returns a started memory driver that is shut down when the
// test ends.
func newLiarMemory(t *testing.T) *queue.MemoryDriver {
	t.Helper()
	m := queue.NewMemoryDriver()
	m.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m
}

// liars names each lying driver with its factory.
var liars = []struct {
	name    string
	factory DriverFactory
}{
	{"dedupe rebuilt on a reserving pop", func(t *testing.T) queue.Driver {
		return &dedupeReservedLiar{MemoryDriver: newLiarMemory(t)}
	}},
	{"released job rebuilt on a plain pop", func(t *testing.T) queue.Driver {
		return &releasedPlainPopLiar{MemoryDriver: newLiarMemory(t)}
	}},
	{"marshalled state restored over the pushed value", func(t *testing.T) queue.Driver {
		return &snapshotRestoreLiar{MemoryDriver: newLiarMemory(t)}
	}},
	{"answer flips at shutdown", func(t *testing.T) queue.Driver {
		return &shutdownFlipLiar{MemoryDriver: newLiarMemory(t)}
	}},
	{"deduped job rebuilt after a release", func(t *testing.T) queue.Driver {
		return &dedupeReleaseLiar{MemoryDriver: newLiarMemory(t)}
	}},
	{"unexported state restored over the pushed value", func(t *testing.T) queue.Driver {
		return &stalePrivateStateLiar{MemoryDriver: newLiarMemory(t)}
	}},
}

// instanceViolations runs every clause of the PreservesJobInstance contract
// against the factory's driver, the same list runInstanceContract runs, and
// returns the names of the clauses the driver fails with their errors. Each
// clause has a driver of its own, so the clauses run side by side.
func instanceViolations(t *testing.T, factory DriverFactory) map[string]error {
	t.Helper()
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		failed = make(map[string]error)
	)
	for _, c := range instanceChecks(factory(t)) {
		d := factory(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.run(d); err != nil {
				mu.Lock()
				failed[c.name] = err
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return failed
}

// deliveryCheckNames returns the names of the delivery clauses for a driver
// shaped like the factory's.
func deliveryCheckNames(t *testing.T, factory DriverFactory) []string {
	t.Helper()
	var names []string
	for _, c := range instanceChecks(factory(t)) {
		if c.name != instanceStableCheck {
			names = append(names, c.name)
		}
	}
	return names
}

// cells returns the delivery clause names for entries by exits, in the
// order instanceChecks lists them.
func cells(entries, exits []string) []string {
	var names []string
	for _, entry := range entries {
		for _, exit := range exits {
			names = append(names, entry+"/"+exit)
		}
	}
	return names
}

var (
	allPops        = []string{"PopCtx", "PopCtxWithTrace", "PopCtxReserved"}
	releaseEntries = []string{"PushCtx+ReleaseCtx", "PushDelayedCtx+ReleaseCtx", "PushIfNotExistsCtx+ReleaseCtx"}
	// memoryEntries is every entry of a driver with all three pushes and
	// reservations: each push, then each push followed by each retry.
	memoryEntries = []string{
		"PushCtx", "PushDelayedCtx", "PushIfNotExistsCtx",
		"PushCtx+ReleaseCtx", "PushDelayedCtx+ReleaseCtx", "PushIfNotExistsCtx+ReleaseCtx",
		"PushCtx+Repush", "PushDelayedCtx+Repush", "PushIfNotExistsCtx+Repush",
	}
)

// TestInstanceContract_HonestDrivers holds the drivers that keep the promise
// to every clause, and pins the clauses each one gets: the matrix is read
// off the driver's interfaces, so this is where a missing row or column
// shows. The bounded driver is an honest driver unlike the memory driver:
// it holds a limited number of jobs and its pushes can block. It runs at
// one and at two processors, since a push that fills it must not keep
// Shutdown from starting.
func TestInstanceContract_HonestDrivers(t *testing.T) {
	memory := func(t *testing.T) queue.Driver { return newLiarMemory(t) }
	bounded := func(t *testing.T) queue.Driver { return newBoundedHonestDriver(newLiarMemory(t), 32) }
	fake := func(*testing.T) queue.Driver { return NewFakeQueue() }
	tests := []struct {
		name    string
		factory DriverFactory
		want    []string
	}{
		{"memory", memory, cells(memoryEntries, allPops)},
		{"bounded", bounded, cells(memoryEntries, allPops)},
		{"fake", fake, cells(
			[]string{"PushCtx", "PushDelayedCtx", "PushCtx+Repush", "PushDelayedCtx+Repush"},
			[]string{"PopCtx"},
		)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := deliveryCheckNames(t, tt.factory); !slices.Equal(got, tt.want) {
				t.Fatalf("delivery clauses = %v; want %v", got, tt.want)
			}
			for name, err := range instanceViolations(t, tt.factory) {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
}

// TestInstanceContract_BoundedDriverFullSuite runs the whole driver contract
// against the bounded driver: nothing in it may need a driver with room for
// every push or a push that never waits.
func TestInstanceContract_BoundedDriverFullSuite(t *testing.T) {
	t.Parallel()
	RunDriverContractTests(t, func(t *testing.T) queue.Driver {
		return newBoundedHonestDriver(newLiarMemory(t), 32)
	})
}

// TestInstanceContract_FailsLiars runs the contract against each lying
// driver and requires it to fail exactly the clauses the lie breaks: the
// contract catches the lie, and catches it where it is.
func TestInstanceContract_FailsLiars(t *testing.T) {
	all := func(t *testing.T, factory DriverFactory) []string { return deliveryCheckNames(t, factory) }
	only := func(names ...string) func(*testing.T, DriverFactory) []string {
		return func(*testing.T, DriverFactory) []string { return names }
	}
	want := map[string]struct {
		clauses func(t *testing.T, factory DriverFactory) []string
		reason  string // every failure reports this
	}{
		// A deduped job is rebuilt whenever a reserving pop takes it: the
		// direct cell, the first delivery of every release, and the first
		// delivery of a re-push that pops through the reserving pop.
		"dedupe rebuilt on a reserving pop": {only(append(
			cells([]string{"PushIfNotExistsCtx+ReleaseCtx"}, allPops),
			"PushIfNotExistsCtx/PopCtxReserved", "PushIfNotExistsCtx+Repush/PopCtxReserved")...), "want the pushed value"},
		"released job rebuilt on a plain pop":             {only(cells(releaseEntries, []string{"PopCtx", "PopCtxWithTrace"})...), "want the pushed value"},
		"marshalled state restored over the pushed value": {all, "Sentinel"},
		"answer flips at shutdown":                        {only(instanceStableCheck), "around Shutdown"},
		"deduped job rebuilt after a release":             {only(cells([]string{"PushIfNotExistsCtx+ReleaseCtx"}, allPops)...), "want the pushed value"},
		"unexported state restored over the pushed value": {all, "closure"},
	}
	for _, l := range liars {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			w, ok := want[l.name]
			if !ok {
				t.Fatalf("no expectation for liar %q", l.name)
			}
			wantFailed := slices.Clone(w.clauses(t, l.factory))
			slices.Sort(wantFailed)
			failed := instanceViolations(t, l.factory)
			var got []string
			for name, err := range failed {
				got = append(got, name)
				t.Logf("%s: %v", name, err)
				if !strings.Contains(err.Error(), w.reason) {
					t.Errorf("%s failed with %q; want it to report %q", name, err, w.reason)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, wantFailed) {
				t.Fatalf("contract failed clauses %v; want it to fail %v", got, wantFailed)
			}
		})
	}
}
