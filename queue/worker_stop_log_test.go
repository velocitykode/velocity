package queue

import (
	"context"
	"testing"
	"time"
)

// stopDuringPopDriver is a memory driver whose reserving pop waits for its
// context to end and returns the context's error, as a driver blocked on
// a remote pop does when the worker stops.
type stopDuringPopDriver struct {
	*MemoryDriver
	popping chan struct{}
}

func (d *stopDuringPopDriver) PopCtxReserved(ctx context.Context, _ string) (Job, ReservationToken, TraceContext, error) {
	select {
	case d.popping <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ReservationToken{}, TraceContext{}, ctx.Err()
}

// TestWorker_StopDuringPopLogsNoError asserts a pop that ends because the
// worker was stopped is not logged as a worker error: stopping is not a
// failure.
func TestWorker_StopDuringPopLogsNoError(t *testing.T) {
	logger := &levelLogger{}
	d := &stopDuringPopDriver{MemoryDriver: newStartedMemoryDriver(t), popping: make(chan struct{}, 1)}
	w := NewWorker(d, "stop-log", func(j Job) error { return j.Handle() },
		WithInterval(time.Millisecond), WithWorkerLogger(logger))
	w.Start(context.Background())
	<-d.popping
	w.Stop(context.Background())

	if lines := logger.errorLines(); len(lines) != 0 {
		t.Fatalf("error lines = %d, want 0: %+v", len(lines), lines)
	}
}
