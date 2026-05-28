// Package queuetest provides executable specifications (contract tests) for
// queue.Driver implementations.
//
// Every framework-shipped driver runs through RunDriverContractTests as part
// of CI; third-party drivers are expected to do the same so that breaking
// changes to interface invariants surface before deployment.
//
// Each invariant lives in a separate t.Run sub-test, so a failure pinpoints
// exactly which contract clause is broken instead of cascading through the
// whole runner.
package queuetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/queue"
)

// DriverFactory returns a fresh empty driver per sub-test. The runner does
// not Shutdown the driver; the factory is responsible for any per-test
// cleanup (typically via t.Cleanup).
type DriverFactory func(t *testing.T) queue.Driver

// ContractJob is the canonical job fixture used by the contract runner.
// It implements queue.Job + queue.Identifiable + queue.Batchable so the
// runner can exercise batch and identity-aware invariants.
type ContractJob struct {
	IDValue      string        `json:"id"`
	Payload      string        `json:"payload"`
	BatchIDValue queue.BatchID `json:"batch_id,omitempty"`
	HandledChan  chan struct{} `json:"-"`
	FailedChan   chan error    `json:"-"`
	HandleErr    error         `json:"-"`
}

func (j *ContractJob) Handle() error {
	if j.HandledChan != nil {
		select {
		case j.HandledChan <- struct{}{}:
		default:
		}
	}
	return j.HandleErr
}

func (j *ContractJob) Failed(err error) {
	if j.FailedChan != nil {
		select {
		case j.FailedChan <- err:
		default:
		}
	}
}

func (j *ContractJob) JobID() string               { return j.IDValue }
func (j *ContractJob) GetBatchID() queue.BatchID   { return j.BatchIDValue }
func (j *ContractJob) SetBatchID(id queue.BatchID) { j.BatchIDValue = id }

func init() {
	queue.Register("*queuetest.ContractJob", func(data []byte) (queue.Job, error) {
		j := &ContractJob{}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	})
}

// RunDriverContractTests is the executable specification of the
// [queue.Driver] interface. Pass a factory that returns a fresh, empty driver
// per sub-test.
func RunDriverContractTests(t *testing.T, factory DriverFactory) {
	t.Helper()

	t.Run("PopCtx_EmptyQueue_ReturnsNilNil", func(t *testing.T) {
		d := factory(t)
		job, err := d.PopCtx(context.Background(), "q-empty")
		if err != nil {
			t.Fatalf("expected nil error on empty queue, got %v", err)
		}
		if job != nil {
			t.Fatalf("expected nil job on empty queue, got %T", job)
		}
	})

	t.Run("PushCtx_Then_PopCtx_RoundTripsPayload", func(t *testing.T) {
		d := factory(t)
		in := &ContractJob{IDValue: "roundtrip-1", Payload: "hello"}
		if err := d.PushCtx(context.Background(), in, "q"); err != nil {
			t.Fatalf("push: %v", err)
		}
		out, err := d.PopCtx(context.Background(), "q")
		if err != nil {
			t.Fatalf("pop: %v", err)
		}
		if out == nil {
			t.Fatal("expected popped job, got nil")
		}
		cj, ok := out.(*ContractJob)
		if !ok {
			t.Fatalf("expected *ContractJob, got %T", out)
		}
		if cj.IDValue != "roundtrip-1" || cj.Payload != "hello" {
			t.Fatalf("payload not preserved: %+v", cj)
		}
	})

	t.Run("Size_ReflectsPushedJobs", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			if err := d.PushCtx(ctx, &ContractJob{IDValue: fmt.Sprintf("size-%d", i)}, "q-size"); err != nil {
				t.Fatalf("push: %v", err)
			}
		}
		n, err := d.Size("q-size")
		if err != nil {
			t.Fatalf("size: %v", err)
		}
		if n < 3 {
			// Some drivers (memory) report exact count; reservation-based DB
			// drivers may exclude reserved rows. We pushed 3 and popped 0, so
			// at least 3 must be visible.
			t.Fatalf("expected Size >= 3, got %d", n)
		}
	})

	t.Run("Clear_RemovesAllJobs", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		if err := d.PushCtx(ctx, &ContractJob{IDValue: "clear-1"}, "q-clear"); err != nil {
			t.Fatalf("push: %v", err)
		}
		if err := d.Clear("q-clear"); err != nil {
			t.Fatalf("clear: %v", err)
		}
		n, _ := d.Size("q-clear")
		if n != 0 {
			t.Fatalf("expected size 0 after Clear, got %d", n)
		}
		job, err := d.PopCtx(ctx, "q-clear")
		if err != nil {
			t.Fatalf("pop after clear: %v", err)
		}
		if job != nil {
			t.Fatalf("expected nil after Clear, got %T", job)
		}
	})

	t.Run("PushCtx_CancelledCtx_RejectsBeforeStore", func(t *testing.T) {
		d := factory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := d.PushCtx(ctx, &ContractJob{IDValue: "cancelled-push"}, "q-cancel")
		if err == nil {
			t.Fatal("expected error pushing with cancelled ctx, got nil")
		}
		// Size must reflect that the cancelled push did not land.
		n, _ := d.Size("q-cancel")
		if n != 0 {
			t.Fatalf("cancelled push leaked into queue: size=%d", n)
		}
	})

	t.Run("PopCtx_CancelledCtx_ReturnsError", func(t *testing.T) {
		d := factory(t)
		// Push then immediately cancel ctx; Pop must surface the cancel.
		if err := d.PushCtx(context.Background(), &ContractJob{IDValue: "cancel-pop"}, "q-cancel-pop"); err != nil {
			t.Fatalf("push: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := d.PopCtx(ctx, "q-cancel-pop")
		if err == nil {
			t.Fatal("expected error popping with cancelled ctx, got nil")
		}
	})

	t.Run("PushDelayedCtx_NotReadyYet_NotPoppable", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		if err := d.PushDelayedCtx(ctx, &ContractJob{IDValue: "delayed-1"}, time.Hour, "q-delay"); err != nil {
			t.Fatalf("push delayed: %v", err)
		}
		// PopCtx should not return the not-yet-ready job. The DB driver
		// honors scheduled_at, the memory driver moves delayed jobs only
		// after the worker tick. Either way the visible queue is empty
		// immediately after the push.
		job, err := d.PopCtx(ctx, "q-delay")
		if err != nil {
			t.Fatalf("pop after delayed push: %v", err)
		}
		if job != nil {
			t.Fatalf("expected nil (delayed not ready), got %T", job)
		}
	})

	t.Run("PushCtx_Concurrent_Safe", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		const n = 20
		var wg sync.WaitGroup
		var firstErr atomic.Value
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if err := d.PushCtx(ctx, &ContractJob{IDValue: fmt.Sprintf("concurrent-%d", i)}, "q-concurrent"); err != nil {
					firstErr.CompareAndSwap(nil, err)
				}
			}(i)
		}
		wg.Wait()
		if v := firstErr.Load(); v != nil {
			t.Fatalf("concurrent push: %v", v)
		}
		size, _ := d.Size("q-concurrent")
		if size != int64(n) {
			t.Fatalf("expected size %d after concurrent push, got %d", n, size)
		}
	})

	t.Run("PushCtx_Batchable_PreservesBatchID", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		batchID := queue.BatchID("batch_contract_test")
		in := &ContractJob{IDValue: "with-batch", BatchIDValue: batchID}
		if err := d.PushCtx(ctx, in, "q-batch"); err != nil {
			t.Fatalf("push: %v", err)
		}
		out, err := d.PopCtx(ctx, "q-batch")
		if err != nil {
			t.Fatalf("pop: %v", err)
		}
		bj, ok := out.(queue.Batchable)
		if !ok {
			t.Fatalf("popped job lost Batchable interface: %T", out)
		}
		if bj.GetBatchID() != batchID {
			t.Fatalf("batch id lost across push/pop: want %q, got %q", batchID, bj.GetBatchID())
		}
	})

	t.Run("Failed_AcceptsTerminalFailure", func(t *testing.T) {
		d := factory(t)
		job := &ContractJob{IDValue: "fail-me"}
		err := d.Failed(job, errors.New("boom"), "q-failed")
		if err != nil {
			t.Fatalf("Failed: %v", err)
		}
	})

	t.Run("Shutdown_Idempotent", func(t *testing.T) {
		d := factory(t)
		// First Shutdown.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.Shutdown(ctx); err != nil {
			t.Fatalf("first Shutdown: %v", err)
		}
		// Second Shutdown must not error or panic. Drivers may close once;
		// a second call is a no-op per [contract.ShutdownAware].
		if err := d.Shutdown(ctx); err != nil {
			t.Fatalf("second Shutdown not idempotent: %v", err)
		}
	})

	t.Run("Shutdown_HonorsCtxDeadline", func(t *testing.T) {
		d := factory(t)
		// A deadline already in the past must not hang.
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		// Shutdown either returns nil (driver completes immediately) or
		// ctx.Err(); both are valid. The invariant is "returns promptly".
		done := make(chan error, 1)
		go func() { done <- d.Shutdown(ctx) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Shutdown ignored ctx deadline")
		}
	})
}

// RunDedupeAwarePusherContract is the contract for the optional
// [queue.DedupeAwarePusher] capability. Skip the test entirely (via t.Skip)
// when a driver does not implement it.
func RunDedupeAwarePusherContract(t *testing.T, factory DriverFactory) {
	t.Helper()

	t.Run("PushIfNotExistsCtx_Duplicate_IsSuccess", func(t *testing.T) {
		d := factory(t)
		dp, ok := d.(queue.DedupeAwarePusher)
		if !ok {
			t.Skip("driver does not implement queue.DedupeAwarePusher")
		}
		ctx := context.Background()
		key := "dedupe-key-1"
		if err := dp.PushIfNotExistsCtx(ctx, &ContractJob{IDValue: "dup-1"}, key, "q-dedupe"); err != nil {
			t.Fatalf("first PushIfNotExistsCtx: %v", err)
		}
		// Second push with the same key MUST be a no-op success, not an error.
		if err := dp.PushIfNotExistsCtx(ctx, &ContractJob{IDValue: "dup-2"}, key, "q-dedupe"); err != nil {
			t.Fatalf("duplicate PushIfNotExistsCtx must be nil error, got %v", err)
		}
		size, _ := d.Size("q-dedupe")
		if size != 1 {
			t.Fatalf("expected exactly 1 row after dup push, got %d", size)
		}
	})

	t.Run("PushIfNotExistsCtx_EmptyKey_FallsThroughToPush", func(t *testing.T) {
		d := factory(t)
		dp, ok := d.(queue.DedupeAwarePusher)
		if !ok {
			t.Skip("driver does not implement queue.DedupeAwarePusher")
		}
		ctx := context.Background()
		// Empty dedupe key must still insert (documented in queue/types.go).
		if err := dp.PushIfNotExistsCtx(ctx, &ContractJob{IDValue: "empty-key"}, "", "q-empty-key"); err != nil {
			t.Fatalf("empty-key PushIfNotExistsCtx: %v", err)
		}
		size, _ := d.Size("q-empty-key")
		if size != 1 {
			t.Fatalf("expected 1 row for empty-key push, got %d", size)
		}
	})
}

// RunReservationDriverContract is the contract for the optional
// [queue.ReservationDriver] capability. Drivers that delete on pop
// (memory, redis) need only implement [queue.Driver]; the DB driver
// must also pass this runner.
func RunReservationDriverContract(t *testing.T, factory DriverFactory) {
	t.Helper()

	t.Run("PopCtxReserved_EmptyQueue_ZeroToken", func(t *testing.T) {
		d := factory(t)
		rd, ok := d.(queue.ReservationDriver)
		if !ok {
			t.Skip("driver does not implement queue.ReservationDriver")
		}
		job, token, _, err := rd.PopCtxReserved(context.Background(), "q-empty")
		if err != nil {
			t.Fatalf("PopCtxReserved on empty queue: want nil err, got %v", err)
		}
		if job != nil {
			t.Fatalf("expected nil job on empty queue, got %T", job)
		}
		if !token.IsZero() {
			t.Fatalf("expected zero token on empty queue, got %+v", token)
		}
	})

	t.Run("PopCtxReserved_IncrementsAttempts", func(t *testing.T) {
		d := factory(t)
		rd, ok := d.(queue.ReservationDriver)
		if !ok {
			t.Skip("driver does not implement queue.ReservationDriver")
		}
		ctx := context.Background()
		if err := d.PushCtx(ctx, &ContractJob{IDValue: "attempts"}, "q-attempts"); err != nil {
			t.Fatalf("push: %v", err)
		}
		_, token, _, err := rd.PopCtxReserved(ctx, "q-attempts")
		if err != nil {
			t.Fatalf("PopCtxReserved: %v", err)
		}
		if token.IsZero() {
			t.Fatalf("expected non-zero token after reserving a row")
		}
		// First pop must observe Attempts == 1 (the persisted post-increment value).
		if token.Attempts != 1 {
			t.Fatalf("expected Attempts==1 on first reserve, got %d", token.Attempts)
		}
	})

	t.Run("AckCtx_ZeroToken_IsNoop", func(t *testing.T) {
		d := factory(t)
		rd, ok := d.(queue.ReservationDriver)
		if !ok {
			t.Skip("driver does not implement queue.ReservationDriver")
		}
		if err := rd.AckCtx(context.Background(), queue.ReservationToken{}); err != nil {
			t.Fatalf("AckCtx with zero token must be no-op, got %v", err)
		}
	})

	t.Run("PopCtxReserved_ReclaimsExpiredLease", func(t *testing.T) {
		d := factory(t)
		rd, ok := d.(queue.ReservationDriver)
		if !ok {
			t.Skip("driver does not implement queue.ReservationDriver")
		}
		// Drivers that expose SetRetryAfter can shrink the lease for
		// the test; skip if the driver does not (we'd otherwise wait
		// the production default of 90s).
		ra, ok := d.(interface{ SetRetryAfter(time.Duration) })
		if !ok {
			t.Skip("driver does not expose SetRetryAfter")
		}
		ra.SetRetryAfter(50 * time.Millisecond)

		ctx := context.Background()
		if err := d.PushCtx(ctx, &ContractJob{IDValue: "lease-reclaim"}, "q-lease"); err != nil {
			t.Fatalf("push: %v", err)
		}
		_, token1, _, err := rd.PopCtxReserved(ctx, "q-lease")
		if err != nil {
			t.Fatalf("first reserve: %v", err)
		}
		if token1.IsZero() {
			t.Fatal("expected non-zero token on first reserve")
		}
		// Worker dies; lease expires.
		time.Sleep(150 * time.Millisecond)

		job2, token2, _, err := rd.PopCtxReserved(ctx, "q-lease")
		if err != nil {
			t.Fatalf("second reserve (reclaim): %v", err)
		}
		if job2 == nil {
			t.Fatal("expected reclaimed job after lease expired, got nil")
		}
		if token2.IsZero() {
			t.Fatal("expected non-zero token on reclaim")
		}
		// Attempts on the second reservation must reflect the persisted
		// post-increment value (the second attempt of this row).
		if token2.Attempts != 2 {
			t.Fatalf("expected Attempts==2 on reclaim, got %d", token2.Attempts)
		}
	})

	t.Run("ReleaseCtx_RequeuesWithBackoff", func(t *testing.T) {
		d := factory(t)
		rd, ok := d.(queue.ReservationDriver)
		if !ok {
			t.Skip("driver does not implement queue.ReservationDriver")
		}
		ctx := context.Background()
		if err := d.PushCtx(ctx, &ContractJob{IDValue: "release-me"}, "q-rel"); err != nil {
			t.Fatalf("push: %v", err)
		}
		_, token, _, err := rd.PopCtxReserved(ctx, "q-rel")
		if err != nil {
			t.Fatalf("PopCtxReserved: %v", err)
		}
		if err := rd.ReleaseCtx(ctx, token, time.Hour); err != nil {
			t.Fatalf("ReleaseCtx: %v", err)
		}
		// Backoff in the future: pop should not return the job.
		job, _, _, err := rd.PopCtxReserved(ctx, "q-rel")
		if err != nil {
			t.Fatalf("PopCtxReserved after release: %v", err)
		}
		if job != nil {
			t.Fatalf("expected nil job after Release with future backoff, got %T", job)
		}
	})
}

// _ keeps contract as a live import so downstream drivers that errors.Is
// against the hoisted sentinel from their own driver-side test continue to
// compile in lockstep with the runner.
var _ = contract.ErrJobNotFound
