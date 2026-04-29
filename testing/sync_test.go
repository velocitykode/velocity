package testsync

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestEventually_Succeeds(t *testing.T) {
	var done atomic.Bool
	go func() {
		time.Sleep(30 * time.Millisecond)
		done.Store(true)
	}()
	Eventually(t, done.Load, time.Second, "goroutine should flip done")
}

func TestEventually_Fails(t *testing.T) {
	mock := &mockT{}
	Eventually(mock, func() bool { return false }, 20*time.Millisecond, "never")
	if !mock.failed {
		t.Fatal("expected Eventually to fail")
	}
}

func TestEventuallyEqual_Succeeds(t *testing.T) {
	var n atomic.Int32
	go func() {
		time.Sleep(30 * time.Millisecond)
		n.Store(5)
	}()
	EventuallyEqual(t, n.Load, int32(5), time.Second, "counter")
}

func TestEventuallyEqual_Fails(t *testing.T) {
	mock := &mockT{}
	EventuallyEqual(mock, func() int32 { return 0 }, int32(5), 20*time.Millisecond, "counter")
	if !mock.failed {
		t.Fatal("expected EventuallyEqual to fail")
	}
}

type mockT struct {
	testing.TB
	failed bool
}

func (m *mockT) Helper()                           {}
func (m *mockT) Fatalf(format string, args ...any) { m.failed = true }
