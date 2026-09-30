package grpc

import (
	"net"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/hostile"
)

// panickingListener's Accept and Close panic with v.
type panickingListener struct {
	net.Listener
	v any
}

func (l panickingListener) Accept() (net.Conn, error) { panic(l.v) }
func (l panickingListener) Close() error              { panic(l.v) }
func (l panickingListener) Addr() net.Addr            { return &net.TCPAddr{} }

// A listener whose Accept or Close panics with a value whose String,
// Error or Format panics, a nested panic included, returns the error with
// the value as errchain.Unreadable: building the error does not crash the
// serve loop.
func TestServeListener_UnformattablePanicValue(t *testing.T) {
	for name, v := range hostile.Unformattables() {
		t.Run(name, func(t *testing.T) {
			l := newServeListener(panickingListener{v: v}, func() {}, func(func(contract.Logger)) {}, "test")
			var acceptErr, closeErr error
			if p := hostile.Within(t, hostile.Deadline, func() {
				_, acceptErr = l.Accept()
				closeErr = l.Close()
			}); p != nil {
				t.Fatalf("a panic escaped: %v", p)
			}
			for _, err := range []error{acceptErr, closeErr} {
				if text := errchain.Text(err); !strings.Contains(text, "panicked: panic: "+errchain.Unreadable) {
					t.Errorf("error text = %q, want the panic value as Unreadable", text)
				}
			}
		})
	}
}
