package queuetest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// instanceJob is a job whose behaviour lives in state json.Marshal does not
// carry: an unexported closure. Its exported state marshals, so every driver
// accepts the push; only a driver that hands back the pushed value itself
// delivers a job whose closure is still set.
//
// Sentinel is exported state the contract changes after the driver has the
// job: a driver that hands back the pushed pointer but writes marshalled
// state over it first delivers a stale Sentinel.
type instanceJob struct {
	IDValue  string `json:"id"`
	Sentinel int    `json:"sentinel"`
	live     func() string
}

func (j *instanceJob) Handle() error { return nil }
func (*instanceJob) Failed(error)    {}
func (j *instanceJob) JobID() string { return j.IDValue }

func init() {
	queue.RegisterJob(func(data []byte) (*instanceJob, error) {
		j := &instanceJob{}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	})
}

// instanceDeliveryWait bounds the wait for a delayed or released job to
// become poppable. The wait ends as soon as the job is popped; the bound is
// reached only by a driver that never delivers it.
const instanceDeliveryWait = 10 * time.Second

// instanceRetryDelay is the delay of every delayed push and release the
// contract makes.
const instanceRetryDelay = 10 * time.Millisecond

// instanceOverlapPushes is the most jobs the stability check pushes beside
// its readers. It stays under the 20 that PushCtx_Concurrent_Safe pushes, so
// the check needs no more room than the contract already asks for.
const instanceOverlapPushes = 8

// instanceQueue is the queue the delivery checks use.
const instanceQueue = "q-instance"

// instancePop is one way a job comes out of a driver; token is the zero
// token for a pop that does not reserve.
type instancePop struct {
	name string
	pop  func(ctx context.Context, q string) (queue.Job, queue.ReservationToken, error)
}

// instancePops returns every pop the driver offers: PopCtx, and the
// trace-aware and reserving pops when it implements them. The worker picks
// the richest one, so the guarantee has to hold on each.
func instancePops(d queue.Driver) []instancePop {
	pops := []instancePop{{"PopCtx", func(ctx context.Context, q string) (queue.Job, queue.ReservationToken, error) {
		job, err := d.PopCtx(ctx, q)
		return job, queue.ReservationToken{}, err
	}}}
	if td, ok := d.(queue.TraceAwareDriver); ok {
		pops = append(pops, instancePop{"PopCtxWithTrace", func(ctx context.Context, q string) (queue.Job, queue.ReservationToken, error) {
			job, _, err := td.PopCtxWithTrace(ctx, q)
			return job, queue.ReservationToken{}, err
		}})
	}
	if rd, ok := d.(queue.ReservationDriver); ok {
		pops = append(pops, instancePop{"PopCtxReserved", func(ctx context.Context, q string) (queue.Job, queue.ReservationToken, error) {
			job, token, _, err := rd.PopCtxReserved(ctx, q)
			return job, token, err
		}})
	}
	return pops
}

// popWhenReady pops until the driver delivers a job, and fails when none is
// delivered within instanceDeliveryWait.
func (p instancePop) popWhenReady(q string) (queue.Job, queue.ReservationToken, error) {
	ctx, cancel := context.WithTimeout(context.Background(), instanceDeliveryWait)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		job, token, err := p.pop(ctx, q)
		if err != nil {
			return nil, token, fmt.Errorf("%s: %w", p.name, err)
		}
		if job != nil {
			return job, token, nil
		}
		select {
		case <-ctx.Done():
			return nil, token, fmt.Errorf("%s delivered no job within %s", p.name, instanceDeliveryWait)
		case <-tick.C:
		}
	}
}

// instanceProbe is the job one delivery check pushes, with the state the
// delivered value must still have recorded apart from the job.
type instanceProbe struct {
	job   *instanceJob
	name  string
	steps int
	mark  string
}

func newInstanceProbe(name string) *instanceProbe {
	p := &instanceProbe{job: &instanceJob{IDValue: "instance-" + name}, name: name}
	p.setLive()
	return p
}

// setLive gives the job a new closure reporting a marker no earlier closure
// reported, and records the marker.
func (p *instanceProbe) setLive() {
	mark := fmt.Sprintf("%s#%d", p.name, p.steps)
	p.mark = mark
	p.job.live = func() string { return mark }
}

// touch changes the job's state while the driver holds the job: the
// exported Sentinel and the unexported closure both. A driver that puts
// back either as it was at an earlier hand-off delivers a stale one.
func (p *instanceProbe) touch() {
	p.steps++
	p.job.Sentinel = p.steps
	p.setLive()
}

// verify fails unless got is the pushed value itself, with its closure and
// its exported state as last touched.
func (p *instanceProbe) verify(got queue.Job) error {
	ij, ok := got.(*instanceJob)
	if !ok {
		return fmt.Errorf("delivered %T; want the pushed *instanceJob", got)
	}
	if ij != p.job {
		return fmt.Errorf("delivered %p; want the pushed value %p", ij, p.job)
	}
	if ij.live == nil {
		return fmt.Errorf("delivered job lost its unexported closure")
	}
	if mark := ij.live(); mark != p.mark {
		return fmt.Errorf("delivered job's closure reports %q; want %q, the closure set after the driver took the job", mark, p.mark)
	}
	if ij.Sentinel != p.steps {
		return fmt.Errorf("delivered job's Sentinel is %d; want %d, the value set after the driver took the job", ij.Sentinel, p.steps)
	}
	return nil
}

// instanceEntry is one way a job gets into a driver so that its next
// delivery is the one under check. exit is the pop that delivery will come
// through; a retry that has to pop first pops through it too.
type instanceEntry struct {
	name  string
	enter func(ctx context.Context, p *instanceProbe, exit instancePop) error
}

// instancePushes returns every push the driver offers: PushCtx,
// PushDelayedCtx, and PushIfNotExistsCtx when it implements it.
func instancePushes(d queue.Driver) []instanceEntry {
	pushes := []instanceEntry{
		{"PushCtx", func(ctx context.Context, p *instanceProbe, _ instancePop) error {
			return d.PushCtx(ctx, p.job, instanceQueue)
		}},
		{"PushDelayedCtx", func(ctx context.Context, p *instanceProbe, _ instancePop) error {
			return d.PushDelayedCtx(ctx, p.job, instanceRetryDelay, instanceQueue)
		}},
	}
	if dp, ok := d.(queue.DedupeAwarePusher); ok {
		pushes = append(pushes, instanceEntry{"PushIfNotExistsCtx", func(ctx context.Context, p *instanceProbe, _ instancePop) error {
			return dp.PushIfNotExistsCtx(ctx, p.job, "instance-key-"+p.name, instanceQueue)
		}})
	}
	return pushes
}

// instanceEntries returns every way a job gets into the driver: each push
// on its own, and each push followed by each retry the worker makes. The
// retries are a reserving driver's ReleaseCtx, and a second PushDelayedCtx
// of the popped job for a driver that deletes on pop. A retry is named
// after the push it follows, as in "PushDelayedCtx+ReleaseCtx".
func instanceEntries(d queue.Driver) []instanceEntry {
	pushes := instancePushes(d)
	entries := append([]instanceEntry(nil), pushes...)

	rd, reserves := d.(queue.ReservationDriver)
	if reserves {
		var reserve instancePop
		for _, p := range instancePops(d) {
			if p.name == "PopCtxReserved" {
				reserve = p
			}
		}
		for _, push := range pushes {
			entries = append(entries, instanceEntry{push.name + "+ReleaseCtx", func(ctx context.Context, p *instanceProbe, exit instancePop) error {
				if err := push.enter(ctx, p, exit); err != nil {
					return err
				}
				p.touch()
				first, token, err := reserve.popWhenReady(instanceQueue)
				if err != nil {
					return err
				}
				if err := p.verify(first); err != nil {
					return fmt.Errorf("first delivery: %w", err)
				}
				return rd.ReleaseCtx(ctx, token, instanceRetryDelay)
			}})
		}
	}
	for _, push := range pushes {
		entries = append(entries, instanceEntry{push.name + "+Repush", func(ctx context.Context, p *instanceProbe, exit instancePop) error {
			if err := push.enter(ctx, p, exit); err != nil {
				return err
			}
			p.touch()
			first, token, err := exit.popWhenReady(instanceQueue)
			if err != nil {
				return err
			}
			if err := p.verify(first); err != nil {
				return fmt.Errorf("first delivery: %w", err)
			}
			if reserves && !token.IsZero() {
				// Settle the reservation so the re-push is the only copy.
				if err := rd.AckCtx(ctx, token); err != nil {
					return fmt.Errorf("AckCtx: %w", err)
				}
			}
			return d.PushDelayedCtx(ctx, first, instanceRetryDelay, instanceQueue)
		}})
	}
	return entries
}

// instanceCheck is one clause of the PreservesJobInstance contract. run
// takes a fresh driver and returns what the driver got wrong, or nil.
type instanceCheck struct {
	name string
	run  func(d queue.Driver) error
}

// instanceStableCheck is the name of the stability clause.
const instanceStableCheck = "Stable"

// instanceChecks lists the contract's clauses for a driver shaped like d.
//
// The stability clause applies to every driver. The delivery clauses apply
// only to a driver answering true, and are the full matrix of the ways a
// job gets in (instanceEntries) by the ways it comes out (instancePops),
// both read off the interfaces the driver implements, so a new entry or
// exit is covered on every existing path without a new test. A driver
// answering false promises nothing about the delivered value, so it gets no
// delivery clause.
func instanceChecks(d queue.Driver) []instanceCheck {
	checks := []instanceCheck{{instanceStableCheck, checkInstanceStable}}
	if !d.PreservesJobInstance() {
		return checks
	}
	for _, entry := range instanceEntries(d) {
		for _, exit := range instancePops(d) {
			checks = append(checks, instanceCheck{
				name: entry.name + "/" + exit.name,
				run: func(d queue.Driver) error {
					return checkInstanceDelivery(d, entry.name, exit.name)
				},
			})
		}
	}
	return checks
}

// checkInstanceDelivery puts a job into d through the named entry and takes
// it out through the named pop. The delivered value must be the pushed one:
// the same pointer, its unexported closure working, and its exported state
// as last changed, which happens after every step that hands the job to the
// driver.
func checkInstanceDelivery(d queue.Driver, entryName, exitName string) error {
	var entry instanceEntry
	for _, e := range instanceEntries(d) {
		if e.name == entryName {
			entry = e
		}
	}
	var exit instancePop
	for _, p := range instancePops(d) {
		if p.name == exitName {
			exit = p
		}
	}
	if entry.enter == nil || exit.pop == nil {
		return fmt.Errorf("driver no longer offers %s or %s", entryName, exitName)
	}
	probe := newInstanceProbe(entryName + "-" + exitName)
	if err := entry.enter(context.Background(), probe, exit); err != nil {
		return fmt.Errorf("%s: %w", entryName, err)
	}
	probe.touch()
	got, _, err := exit.popWhenReady(instanceQueue)
	if err != nil {
		return err
	}
	return probe.verify(got)
}

// checkInstanceStable holds the driver to one answer for its whole
// lifetime: between its operations, while pushes and a Shutdown run beside
// the readers, and after Shutdown has returned. It calls Shutdown once.
func checkInstanceStable(d queue.Driver) error {
	ctx := context.Background()
	want := d.PreservesJobInstance()
	check := func(when string) error {
		if got := d.PreservesJobInstance(); got != want {
			return fmt.Errorf("PreservesJobInstance %s = %v; first answer was %v", when, got, want)
		}
		return nil
	}
	const q = "q-stable"
	if err := d.PushCtx(ctx, &ContractJob{IDValue: "stable-1"}, q); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	if err := check("after a push"); err != nil {
		return err
	}
	if err := d.PushDelayedCtx(ctx, &ContractJob{IDValue: "stable-2"}, time.Hour, q); err != nil {
		return fmt.Errorf("push delayed: %w", err)
	}
	if err := check("after a delayed push"); err != nil {
		return err
	}
	if _, err := d.PopCtx(ctx, q); err != nil {
		return fmt.Errorf("pop: %w", err)
	}
	if err := check("after a pop"); err != nil {
		return err
	}
	if err := d.Clear(q); err != nil {
		return fmt.Errorf("clear: %w", err)
	}
	if err := check("after a clear"); err != nil {
		return err
	}

	// The overlap asks of the driver no more than the rest of the contract
	// does. It pushes at most instanceOverlapPushes jobs, fewer than
	// PushCtx_Concurrent_Safe, so a bounded driver has room. Every push
	// carries a context that ends when Shutdown has returned, so a push
	// that waits (for room, or on a driver that is shutting down) is
	// released before the goroutines are joined. Shutdown starts once every
	// reader has read once and every pusher is about to push; it does not
	// wait for a push to return.
	const readers, pushers = 6, 2
	pushCtx, cancelPushes := context.WithTimeout(ctx, instanceDeliveryWait)
	defer cancelPushes()
	var (
		wg, started sync.WaitGroup
		changed     atomic.Int64
		stop        = make(chan struct{})
	)
	read := func() {
		if d.PreservesJobInstance() != want {
			changed.Add(1)
		}
	}
	wg.Add(readers + pushers)
	started.Add(readers + pushers)
	for range readers {
		go func() {
			defer wg.Done()
			read()
			started.Done()
			for {
				select {
				case <-stop:
					// Once more, after Shutdown has returned.
					read()
					return
				default:
					read()
				}
			}
		}()
	}
	for range pushers {
		go func() {
			defer wg.Done()
			started.Done()
			for range instanceOverlapPushes / pushers {
				// A push may be refused or cut short; only the answer
				// is under check.
				if d.PushCtx(pushCtx, &ContractJob{IDValue: "stable-overlap"}, q) != nil {
					return
				}
			}
		}()
	}
	started.Wait()
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	shutdownErr := d.Shutdown(shutdownCtx)
	cancel()
	cancelPushes()
	close(stop)
	wg.Wait()
	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	if n := changed.Load(); n != 0 {
		return fmt.Errorf("PreservesJobInstance changed for %d concurrent read(s) around Shutdown; first answer was %v", n, want)
	}
	return check("after Shutdown")
}

// runInstanceContract is the contract of [queue.Driver.PreservesJobInstance]:
// one sub-test for each clause instanceChecks lists for the driver.
func runInstanceContract(t *testing.T, factory DriverFactory) {
	t.Helper()
	t.Run("PreservesJobInstance", func(t *testing.T) {
		for _, c := range instanceChecks(factory(t)) {
			t.Run(c.name, func(t *testing.T) {
				if err := c.run(factory(t)); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
}
