package websocket

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/velocitykode/velocity/internal/hostile"
)

// pipeListener is an in-memory listener: each dial is a net.Pipe whose
// server end Accept returns. A pipe is unbuffered, so a peer that stops
// reading blocks the server's writePump on its next write, and the
// client's queue then fills with small messages.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
	// last is the server end of the latest dial.
	last atomic.Pointer[writeTracker]
}

// writeTracker is the server end of a pipe; it counts the writes begun,
// so a test can see writePump take a message to a peer that does not
// read: on a pipe, that write cannot return until the peer reads.
type writeTracker struct {
	net.Conn
	begun atomic.Int32
}

func (w *writeTracker) Write(p []byte) (int, error) {
	w.begun.Add(1)
	return w.Conn.Write(p)
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	pipeServer, client := net.Pipe()
	server := &writeTracker{Conn: pipeServer}
	l.last.Store(server)
	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// pipeHost is a started Server served over a pipeListener. It never times
// a connection out, so a peer that stops reading holds its client open.
type pipeHost struct {
	s *Server
	l *pipeListener
}

func newPipeHost(t *testing.T) *pipeHost {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AllowedOrigins = []string{"*"}
	cfg.WriteTimeout = time.Hour
	cfg.PongTimeout = time.Hour
	cfg.PingInterval = time.Hour
	s := New(cfg)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	l := newPipeListener()
	srv := &http.Server{Handler: http.HandlerFunc(s.HandleConnection)}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = s.Shutdown(context.Background())
		_ = srv.Close()
	})
	return &pipeHost{s: s, l: l}
}

// connect dials a peer and returns the server's client for it with the
// peer's connection, after the welcome message.
func (h *pipeHost) connect(t *testing.T) (*Client, *websocket.Conn) {
	t.Helper()
	d := websocket.Dialer{NetDialContext: h.l.dial}
	hdr := http.Header{}
	hdr.Set("Origin", "http://pipe")
	ws, _, err := d.Dial("ws://pipe/", hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	var welcome Message
	if err := ws.ReadJSON(&welcome); err != nil || welcome.Type != "welcome" {
		t.Fatalf("read welcome: %v %q", err, welcome.Type)
	}
	c, ok := h.s.GetClient(welcome.Data.(map[string]interface{})["id"].(string))
	if !ok {
		t.Fatal("the server does not know the client")
	}
	return c, ws
}

// full returns a client whose queue is full and stays full: its peer does
// not read, so writePump blocks writing the first message, and the queue
// then fills behind it.
func (h *pipeHost) full(t *testing.T) (*Client, *websocket.Conn) {
	t.Helper()
	c, ws := h.connect(t)
	w := h.l.last.Load()
	// The peer has read the welcome, so every write begun so far is the
	// welcome's; the next write to begin is the first filler's, which the
	// peer never reads. A write still in progress is not evidence: on one
	// CPU it is the welcome's tail, and writePump then takes a filler off
	// the queue after the queue was seen full.
	welcome := w.begun.Load()
	if err := c.SendMessage(Message{Type: "filler"}); err != nil {
		t.Fatalf("fill: %v", err)
	}
	// Once writePump is blocked writing the filler to the peer, it takes
	// nothing more from the queue, so the queue stays full once it fills.
	hostile.Eventually(t, hostile.Deadline, "writePump blocked on the peer", func() bool {
		return w.begun.Load() > welcome
	})
	for range 10 * cap(c.send) {
		err := c.SendMessage(Message{Type: "filler"})
		if errors.Is(err, ErrSendChannelFull) {
			return c, ws
		}
		if err != nil {
			t.Fatalf("fill: %v", err)
		}
	}
	t.Fatal("the client's queue never filled")
	return nil, nil
}

// parked starts a SendMessageCtx on the full client c and returns where its
// result lands, once the send has registered as waiting.
func parked(t *testing.T, c *Client, ctx context.Context) <-chan error {
	t.Helper()
	res := make(chan error, 1)
	go func() { res <- c.SendMessageCtx(ctx, Message{Type: "parked"}) }()
	hostile.Eventually(t, hostile.Deadline, "the send waiting", func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.done != nil
	})
	return res
}
