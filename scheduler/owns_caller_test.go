package scheduler

import (
	"context"
	"sync"
	"testing"
)

// OwnsCaller is true on the scheduler's own work (a task) and false
// elsewhere, and a second scheduler does not own the first one's task.
func TestScheduler_OwnsCaller(t *testing.T) {
	s, other := New(), New()
	var own, others bool
	var once sync.Once
	ran := make(chan struct{})
	s.Named("owns", func() {
		once.Do(func() {
			own, others = s.OwnsCaller(), other.OwnsCaller()
			close(ran)
		})
	}).Cron("* * * * *")
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	<-ran
	if !own || others {
		t.Fatalf("in a task: s.OwnsCaller = %v, other.OwnsCaller = %v; want true, false", own, others)
	}
	if s.OwnsCaller() {
		t.Fatal("OwnsCaller = true on the test goroutine")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	<-done
}
