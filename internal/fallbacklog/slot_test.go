package fallbacklog

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// The zero Slot and a Slot set to nil hold the fallback logger.
func TestSlot_ZeroAndNilHoldTheFallback(t *testing.T) {
	var s Slot
	if _, ok := s.Get().(Logger); !ok {
		t.Fatalf("zero Slot Get = %T, want Logger", s.Get())
	}
	var got contract.Logger
	s.Use(func(l contract.Logger) { got = l })
	if _, ok := got.(Logger); !ok {
		t.Fatalf("zero Slot Use got %T, want Logger", got)
	}
	r := &recorder{}
	s.Set(r)
	if s.Get() != contract.Logger(r) {
		t.Fatalf("Get = %T, want the set logger", s.Get())
	}
	s.Set(nil)
	if _, ok := s.Get().(Logger); !ok {
		t.Fatalf("Get after Set(nil) = %T, want Logger", s.Get())
	}
}

// Set waits for a Use in flight through the logger it replaces, and a Use
// started after Set sees the new logger.
func TestSlot_SetWaitsForUseInFlight(t *testing.T) {
	var s Slot
	old, next := &recorder{}, &recorder{}
	s.Set(old)
	entered, release := make(chan struct{}), make(chan struct{})
	go s.Use(func(contract.Logger) {
		close(entered)
		<-release
	})
	<-entered
	done := make(chan struct{})
	go func() {
		s.Set(next)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Set returned while a Use through the old logger was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	var seen contract.Logger
	s.Use(func(l contract.Logger) { seen = l })
	if seen != contract.Logger(next) {
		t.Errorf("Use during the drain got %T %p, want the new logger", seen, seen)
	}
	close(release)
	<-done
}

// A Use whose callback panics still ends its in-flight count.
func TestSlot_PanickingUseDoesNotBlockSet(t *testing.T) {
	var s Slot
	s.Set(&recorder{})
	func() {
		defer func() { _ = recover() }()
		s.Use(func(contract.Logger) { panic("writer broke") })
	}()
	done := make(chan struct{})
	go func() { s.Set(nil); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Set blocked on a Use that panicked")
	}
}

// Sets racing Uses from many goroutines: once a Set has returned, no Use
// still runs on the logger it replaced.
func TestSlot_ConcurrentSetAndUse(t *testing.T) {
	var s Slot
	type tracked struct {
		recorder
		retired atomic.Bool
		late    atomic.Int32
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s.Use(func(l contract.Logger) {
					if tl, ok := l.(*tracked); ok && tl.retired.Load() {
						tl.late.Add(1)
					}
				})
			}
		}()
	}
	var all []*tracked
	for i := 0; i < 200; i++ {
		n := &tracked{}
		prev, _ := s.Get().(*tracked)
		s.Set(n)
		if prev != nil {
			prev.retired.Store(true)
		}
		all = append(all, n)
	}
	close(stop)
	wg.Wait()
	for _, l := range all {
		if n := l.late.Load(); n != 0 {
			t.Fatalf("a Use ran on a logger %d times after the Set replacing it returned", n)
		}
	}
}
