package queue

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Start and Stop may race: a Stop that lands while Start is publishing the
// worker's context sees either no start (a no-op) or the whole start, so
// it never reads a half-published cancel, and a Stop after both returned
// waits for every pump Start spawned. Run under -race.
func TestWorker_StartAndStopRace(t *testing.T) {
	for range 200 {
		w := NewWorker(NewMemoryDriver(), "lifecycle-race", func(Job) error { return nil },
			WithConcurrency(4), WithInterval(time.Millisecond), WithWorkerLogger(nullLogger{}))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); w.Start(context.Background()) }()
		go func() { defer wg.Done(); _ = w.Stop() }()
		wg.Wait()
		_ = w.Stop()
	}
}
