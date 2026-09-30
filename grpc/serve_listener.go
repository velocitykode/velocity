package grpc

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// serveListener is the listener a Server's grpc-go server or a Gateway's
// net/http server serves, wrapped so that none of its methods reaches the
// serving library uncontained. grpc-go calls Addr and Close holding its
// own lock, where a panic would leave that lock held and every later stop
// waiting on it, so the address is read once, contained, when the
// listener is wrapped, and Close and Accept are contained. Both libraries
// call Accept only once they have registered the listener, so that a
// stop from then on closes it: the first Accept is the evidence that the
// component takes connections (see Server.serving and Gateway.serving).
type serveListener struct {
	net.Listener

	// addr is the address the listener reported when it was wrapped, and
	// text its string: empty when the listener's Addr panicked.
	addr staticAddr
	text string

	// serving runs once, at the first Accept, before it reaches the
	// listener.
	serving func()
	first   atomic.Bool

	// closed is closed once a Close has returned, whatever it did.
	closed    chan struct{}
	closeOnce sync.Once

	// closePanic is a panic a Close contained, written through logLine
	// later, outside grpc-go's lock (see logClosePanic).
	closePanic atomic.Pointer[error]

	// logLine writes a diagnostic through the component's logger, and
	// component names the component in it ("gRPC", "HTTP gateway").
	logLine   func(func(contract.Logger))
	component string
}

// staticAddr is a net.Addr fixed when the listener was wrapped.
type staticAddr struct{ network, text string }

func (a staticAddr) Network() string { return a.network }
func (a staticAddr) String() string  { return a.text }

// newServeListener wraps lis, reading its address once. A panic in the
// listener's Addr, or in the address's methods, is contained and written
// through logLine, and the listener reports an empty address.
func newServeListener(lis net.Listener, serving func(), logLine func(func(contract.Logger)), component string) *serveListener {
	l := &serveListener{Listener: lis, serving: serving, closed: make(chan struct{}), logLine: logLine, component: component}
	func() {
		defer func() {
			if p := recover(); p != nil {
				logLine(func(lg contract.Logger) {
					lg.Error(l.component+" listener Addr panicked", "error", panicerr.FromRecovered(p))
				})
			}
		}()
		if a := lis.Addr(); a != nil {
			addr := staticAddr{network: a.Network(), text: a.String()}
			l.addr, l.text = addr, addr.text
		}
	}()
	return l
}

// Addr returns the address the listener reported when it was wrapped.
func (l *serveListener) Addr() net.Addr { return l.addr }

// Accept runs serving at the first call, then accepts from the listener.
// A panic in the listener's Accept is returned as an error, which ends
// grpc-go's serve loop.
func (l *serveListener) Accept() (conn net.Conn, err error) {
	if l.first.CompareAndSwap(false, true) {
		l.serving()
	}
	defer func() {
		if p := recover(); p != nil {
			conn, err = nil, fmt.Errorf("velocity/grpc: listener Accept panicked: %w", panicerr.FromRecovered(p))
		}
	}()
	return l.Listener.Accept()
}

// Close closes the listener. A panic in its Close is returned as an error
// and kept for logClosePanic: grpc-go calls Close holding its own lock,
// where the logger, which may call the server back, must not run.
func (l *serveListener) Close() (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("velocity/grpc: listener Close panicked: %w", panicerr.FromRecovered(p))
			l.closePanic.Store(&err)
		}
		l.closeOnce.Do(func() { close(l.closed) })
	}()
	return l.Listener.Close()
}

// logClosePanic writes a panic a Close contained through logLine, once.
// The framework calls it outside every lock after a close.
func (l *serveListener) logClosePanic() {
	if p := l.closePanic.Swap(nil); p != nil {
		l.logLine(func(lg contract.Logger) { lg.Error(l.component+" listener close panicked", "error", *p) })
	}
}
