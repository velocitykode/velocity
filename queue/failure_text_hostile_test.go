package queue

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A failed job's error is user code: its Error runs when the memory driver
// records the failure. An Error that panics, blocks, or calls back into
// the driver must not leave the driver locked.
func TestMemoryDriver_FailureRecordWithHostileError(t *testing.T) {
	record := map[string]func(m *MemoryDriver, err error){
		"FailedCtx": func(m *MemoryDriver, err error) {
			_ = m.FailedCtx(context.Background(), &testBatchJob{}, err, "q")
		},
		"FailReservedCtx": func(m *MemoryDriver, err error) {
			if pushErr := m.PushCtx(context.Background(), &testBatchJob{}, "q"); pushErr != nil {
				t.Errorf("PushCtx: %v", pushErr)
			}
			job, token, _, popErr := m.PopCtxReserved(context.Background(), "q")
			if popErr != nil || job == nil {
				t.Errorf("PopCtxReserved = %v, %v", job, popErr)
				return
			}
			_ = m.FailReservedCtx(context.Background(), token, job, err, "q")
		},
	}
	for _, mode := range hostile.Modes() {
		for name, fail := range record {
			t.Run(mode.String()+"/"+name, func(t *testing.T) {
				fallbacklogtest.Capture(t)
				m := NewMemoryDriver()
				t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
				size := func() { _, _ = m.Size("q") }
				code := hostile.New(t, mode, size)
				err := hostile.NewValue(code, "job failed")
				if mode == hostile.Block {
					go fail(m, err)
					if !code.AwaitEntered(t) {
						return
					}
					hostile.Within(t, hostile.Deadline, size)
				} else {
					hostile.Within(t, hostile.Deadline, func() { fail(m, err) })
				}
				code.Release()
				code.Disarm()
				hostile.Within(t, hostile.Deadline, func() {
					fail(m, err)
					size()
				})
			})
		}
	}
}

// The batch repository takes a failed job's error text when it records a
// failure; an Error that panics, blocks, or reads the batch must not leave
// the batch locked or its counters moved without the failure.
func TestBatchRepository_IncrementFailureWithHostileError(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			r := NewInMemoryBatchRepository()
			t.Cleanup(func() { _ = r.Close() })
			b := &Batch{id: newBatchID(), totalJobs: 2, queue: "default"}
			b.pendingJobs.Store(2)
			if err := r.Save(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			read := func() { _ = b.lastErrorSnapshot() }
			code := hostile.New(t, mode, read)
			err := hostile.NewValue(code, "job failed")
			fail := func() { _, _, _ = r.IncrementFailure(context.Background(), b.id, err) }
			if mode == hostile.Block {
				go fail()
				if !code.AwaitEntered(t) {
					return
				}
				hostile.Within(t, hostile.Deadline, read)
			} else {
				hostile.Within(t, hostile.Deadline, fail)
			}
			code.Release()
			code.Disarm()
			hostile.Within(t, hostile.Deadline, func() {
				fail()
				read()
			})
			if mode == hostile.Panic && b.FailedJobs() != 1 {
				t.Fatalf("FailedJobs = %d after a panicked and a clean failure, want 1", b.FailedJobs())
			}
		})
	}
}
