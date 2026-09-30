package ports

import (
	"net"
	"strconv"
	"testing"
)

func loopback(t *testing.T) net.Listener {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return lis
}

// freePort releases the port it returns.
func freePort(t *testing.T) string {
	lis := loopback(t) // want
	_, port, _ := net.SplitHostPort(lis.Addr().String())
	_ = lis.Close()
	return port
}

// Inline: the port is read, the listener closed, the port handed on.
func TestInline(t *testing.T) {
	l, err := net.Listen("tcp", ":0") // want
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	serve(strconv.Itoa(port))
}

// Held open for the whole test so the bind fails: not reported.
func TestHeld(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	serve(strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
}

// Closed in a Cleanup: held for the test, not reported.
func TestCleanup(t *testing.T) {
	ln := loopback(t)
	t.Cleanup(func() { _ = ln.Close() })
	serve(ln.Addr().String())
}

// Handed to the server itself: not reported.
func TestServeOnListener(t *testing.T) {
	ln := loopback(t)
	dial(ln.Addr().String())
	serveOn(ln)
	ln.Close()
}

// Closed on purpose without reading its address: not reported.
func TestClosedListener(t *testing.T) {
	ln := loopback(t)
	_ = ln.Close()
	serveOn(ln)
}

// In a closure: the variable is the closure's own.
func TestClosure(t *testing.T) {
	run := func() string {
		probe, _ := net.Listen("tcp", "127.0.0.1:0") // want
		addr := probe.Addr().String()
		probe.Close()
		return addr
	}
	serve(run())
}

func serve(string)         {}
func serveOn(net.Listener) {}
func dial(string)          {}
