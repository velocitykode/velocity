package testnet

import (
	"net"
	"testing"
)

// Loopback returns a TCP listener on an ephemeral loopback port, open
// until the test ends: it is closed at cleanup unless the code it was
// handed to closed it first.
func Loopback(t testing.TB) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	return lis
}
