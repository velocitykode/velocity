package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A Shutdown from outside that arrives while a tick is still dispatching
// waits for the tick and the runs it starts: a task the tick dispatches
// after Shutdown began must not run after Shutdown returned.
func TestShutdown_WaitsForATickInProgress(t *testing.T) {
	s := New()
	inBefore, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.Before(func() {
		once.Do(func() {
			close(inBefore)
			<-resume
		})
	})
	var ran atomic.Bool
	s.Named("tick.drain", func() { ran.Store(true) }).Cron("* * * * *")

	stoppedRun := make(chan error, 1)
	go func() { stoppedRun <- s.Run(context.Background()) }()
	<-inBefore

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped <- s.Shutdown(ctx)
	}()
	select {
	case err := <-stopped:
		t.Fatalf("Shutdown returned %v while a tick was still dispatching", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(resume)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown never returned after the tick finished")
	}
	if !ran.Load() {
		t.Fatal("the tick's task had not run when Shutdown returned")
	}
	<-stoppedRun
}

// A tick that starts after Shutdown has stopped a running scheduler
// dispatches nothing: Shutdown may already be waiting, and a count the
// tick took would race its wait.
func TestRunDueJobs_AfterShutdownDispatchesNothing(t *testing.T) {
	s := New()
	var runs atomic.Int32
	s.Named("tick.after", func() { runs.Add(1) }).Cron("* * * * *")
	ran := make(chan error, 1)
	go func() { ran <- s.Run(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	<-ran

	// A new minute key, so the dedup does not hide a dispatch.
	s.jobs[0].mu.Lock()
	s.jobs[0].lastFiredWallMinute = ""
	s.jobs[0].mu.Unlock()
	before := runs.Load()
	s.runDueJobs()
	waitTicks(s)
	if got := runs.Load(); got != before {
		t.Fatalf("a tick after Shutdown dispatched %d runs, want 0", got-before)
	}
}

// Shutdown racing the ticks of a running scheduler from many goroutines:
// run under -race, which reports a count taken concurrently with the
// drain's wait.
func TestShutdown_RacingTicks(t *testing.T) {
	for range 50 {
		s := New()
		s.Named("tick.race", func() {}).Cron("* * * * *")
		ran := make(chan error, 1)
		go func() { ran <- s.Run(context.Background()) }()
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(s.runDueJobs)
		}
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		wg.Wait()
		<-ran
	}
}
