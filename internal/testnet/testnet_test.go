package testnet

import (
	"net"
	"testing"
)

// The listener is open for the test: a dial to its address reaches it.
func TestLoopback_IsOpen(t *testing.T) {
	lis := Loopback(t)
	done := make(chan error, 1)
	go func() {
		c, err := lis.Accept()
		if err == nil {
			_ = c.Close()
		}
		done <- err
	}()
	c, err := net.Dial("tcp", lis.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()
	if err := <-done; err != nil {
		t.Fatalf("accept: %v", err)
	}
}

// Cleanup closes a listener the test did not.
func TestLoopback_ClosedAtCleanup(t *testing.T) {
	var lis net.Listener
	t.Run("open", func(t *testing.T) { lis = Loopback(t) })
	if _, err := lis.Accept(); err == nil {
		t.Fatal("Accept after the subtest's cleanup succeeded, want the closed listener's error")
	}
}
