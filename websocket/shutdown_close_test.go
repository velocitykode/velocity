package websocket

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// closeHookConn is a hijacked connection whose Close runs onClose first,
// as a connection a ResponseWriter wrapper supplies can.
type closeHookConn struct {
	net.Conn
	onClose func()
}

func (c *closeHookConn) Close() error {
	c.onClose()
	return c.Conn.Close()
}

// hijackHook wraps a ResponseWriter so the connection it hijacks closes
// through onClose.
type hijackHook struct {
	http.ResponseWriter
	onClose func()
}

func (h hijackHook) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(h.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	return &closeHookConn{Conn: conn, onClose: h.onClose}, rw, nil
}

// serveWithCloseHook starts s behind a test server whose connections run
// onClose when the server closes them, connects one client, and waits
// until the server has registered it.
func serveWithCloseHook(t *testing.T, s *Server, onClose func()) {
	t.Helper()
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.HandleConnection(hijackHook{ResponseWriter: w, onClose: onClose}, r)
	}))
	t.Cleanup(ts.Close)
	ws := dialClient(t, ts.URL)
	t.Cleanup(func() { _ = ws.Close() })
	hostile.Eventually(t, hostile.Deadline, "the client registered", func() bool {
		return len(s.GetClients()) == 1
	})
}

// A connection whose Close blocks does not hold Shutdown past its ctx: the
// connections are closed by the drain, not under the server's lock on the
// caller's goroutine.
func TestShutdown_BlockingConnectionCloseHonoursCtx(t *testing.T) {
	s := New(DefaultConfig())
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	entered := make(chan struct{})
	var enteredOnce sync.Once
	serveWithCloseHook(t, s, func() {
		enteredOnce.Do(func() { close(entered) })
		<-release
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = s.Shutdown(ctx) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v, want the deadline while a connection's Close blocks", err)
	}
	<-entered
	once.Do(func() { close(release) })
	hostile.Within(t, hostile.Deadline, func() { err = s.Shutdown(context.Background()) })
	if err != nil {
		t.Fatalf("Shutdown after the Close returned = %v", err)
	}
}

// A connection whose Close calls Shutdown is refused at once instead of
// deadlocking on the server's lock, and the outer Shutdown completes.
func TestShutdown_ConnectionCloseThatShutsDownIsRefused(t *testing.T) {
	s := New(DefaultConfig())
	var inner atomic.Pointer[error]
	var once sync.Once
	serveWithCloseHook(t, s, func() {
		once.Do(func() {
			err := s.Shutdown(context.Background())
			inner.Store(&err)
		})
	})
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = s.Shutdown(context.Background()) })
	if err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	got := inner.Load()
	if got == nil {
		t.Fatal("the connection's Close never ran")
	}
	if !errors.Is(*got, contract.ErrStopFromOwnWork) {
		t.Fatalf("Shutdown from the connection's Close = %v, want ErrStopFromOwnWork", *got)
	}
}
