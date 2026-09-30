//go:build unix

package velocity

import (
	"errors"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/internal/testnet"
)

// closedWithin reports whether lis is closed: a closed listener's Accept
// fails at once with net.ErrClosed, an open one waits out the deadline.
func closedWithin(t *testing.T, lis net.Listener, d time.Duration) bool {
	t.Helper()
	_ = lis.(*net.TCPListener).SetDeadline(time.Now().Add(d))
	c, err := lis.Accept()
	if err == nil {
		_ = c.Close()
	}
	return errors.Is(err, net.ErrClosed)
}

// Serve serves HTTP on the listener WithListener hands it, and the
// shutdown closes that listener as it would one the app bound itself.
func TestWithListener_ServesOnItAndShutdownClosesIt(t *testing.T) {
	lis := testnet.Loopback(t)
	a, err := NewTestApp(WithListener(lis), WithPort("1"))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- a.serveHTTP() }()

	served := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		resp, err := http.Get("http://" + lis.Addr().String() + "/no-such-route")
		if err == nil {
			_ = resp.Body.Close()
			served = resp.StatusCode == http.StatusNotFound
			break
		}
	}
	if !served {
		t.Fatal("the app never answered on the listener handed to WithListener")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("syscall.Kill: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("serveHTTP = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveHTTP did not return after SIGTERM")
	}
	if !closedWithin(t, lis, time.Second) {
		t.Error("the listener is still open after the shutdown")
	}
}

// A Serve whose bootstrap fails never served on the listener: it is left
// open for the caller.
func TestWithListener_FailedStartLeavesItOpen(t *testing.T) {
	lis := testnet.Loopback(t)
	a, err := NewTestApp(WithListener(lis))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(&shutdownRecorder{startErr: errors.New("chain start kaboom")})
	})
	if err := a.serveHTTP(); err == nil {
		t.Fatal("serveHTTP succeeded, want the bootstrap error")
	}
	if closedWithin(t, lis, 20*time.Millisecond) {
		t.Error("a failed start closed the caller's listener")
	}
}
