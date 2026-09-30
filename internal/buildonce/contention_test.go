package buildonce

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/goroutine"
	"github.com/velocitykode/velocity/internal/hostile"
)

// countWalks counts the stack walks Do makes for the rest of the test.
func countWalks(t *testing.T) *atomic.Int64 {
	var n atomic.Int64
	prev := inside
	inside = func(names ...string) bool {
		n.Add(1)
		return goroutine.Inside(names...)
	}
	t.Cleanup(func() { inside = prev })
	return &n
}

// A Do that finds no build of its key in progress walks no stack, nested
// builds of other keys included.
func TestDo_UncontendedWalksNoStack(t *testing.T) {
	walks := countWalks(t)
	var g Group[int]
	for range 100 {
		if _, err := g.Do(context.Background(), "k", func() (int, error) {
			return g.Do(context.Background(), "other", func() (int, error) { return 1, nil })
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n := walks.Load(); n != 0 {
		t.Fatalf("uncontended Do walked the stack %d times", n)
	}
}

// A Do from inside a build of another key, whose build is in progress on
// another goroutine, is refused at once: waiting there is how two builds
// that need each other deadlock.
func TestDo_WaitFromInsideAnotherBuildIsRefused(t *testing.T) {
	walks := countWalks(t)
	var g Group[int]
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		_, _ = g.Do(context.Background(), "b", func() (int, error) {
			close(started)
			<-release
			return 2, nil
		})
	}()
	<-started
	hostile.Within(t, hostile.Deadline, func() {
		_, err := g.Do(context.Background(), "a", func() (int, error) {
			return g.Do(context.Background(), "b", func() (int, error) { return 0, nil })
		})
		if err == nil || !strings.Contains(err.Error(), "from inside another build") {
			t.Errorf("wait from inside another build: err = %v, want the refusal", err)
		}
	})
	if walks.Load() != 1 {
		t.Errorf("stack walks = %d, want 1 (the contended Do)", walks.Load())
	}
}

// A contended Do outside any build waits, whatever depth it calls from.
func TestDo_ContendedOutsideABuildWaits(t *testing.T) {
	var g Group[int]
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = g.Do(context.Background(), "k", func() (int, error) {
			close(started)
			<-release
			return 7, nil
		})
	}()
	<-started
	got := make(chan int, 1)
	go deep(200, func() {
		v, err := g.Do(context.Background(), "k", func() (int, error) { return 0, nil })
		if err != nil {
			t.Error(err)
		}
		got <- v
	})
	hostile.Eventually(t, hostile.Deadline, "the waiter joining", func() bool { return g.Joined("k") == 1 })
	close(release)
	if v := <-got; v != 7 {
		t.Fatalf("waiter got %d, want 7", v)
	}
}
