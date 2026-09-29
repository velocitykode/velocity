package drivers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/websocket"
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

// writeTracker is the server end of a pipe; it counts the writes in
// progress, so a test can see writePump blocked on a peer that does not
// read.
type writeTracker struct {
	net.Conn
	writing atomic.Int32
}

func (w *writeTracker) Write(p []byte) (int, error) {
	w.writing.Add(1)
	defer w.writing.Add(-1)
	return w.Conn.Write(p)
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

// clientHost is a running websocket server, served over a pipeListener,
// whose connected clients the tests hand to a driver: every client a test
// uses this way has a real send queue, a writePump draining it to a peer,
// and the real close path. The server never times a connection out, so a
// peer that stops reading holds its client open.
type clientHost struct {
	server *websocket.Server
	l      *pipeListener
	mu     sync.Mutex
	peers  map[*websocket.Client]*gorillaws.Conn
}

func newClientHost(tb testing.TB) *clientHost {
	tb.Helper()
	cfg := websocket.DefaultConfig()
	cfg.AllowedOrigins = []string{"*"}
	cfg.WriteTimeout = time.Hour
	cfg.PongTimeout = time.Hour
	cfg.PingInterval = time.Hour
	s := websocket.New(cfg)
	if err := s.Start(); err != nil {
		tb.Fatalf("Start: %v", err)
	}
	l := &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
	srv := &http.Server{Handler: http.HandlerFunc(s.HandleConnection)}
	go func() { _ = srv.Serve(l) }()
	tb.Cleanup(func() {
		_ = s.Shutdown(context.Background())
		_ = srv.Close()
	})
	return &clientHost{server: s, l: l, peers: map[*websocket.Client]*gorillaws.Conn{}}
}

// connect dials a peer and returns the server's client for it with the
// peer's connection, after the welcome message.
func (h *clientHost) connect(tb testing.TB) (*websocket.Client, *gorillaws.Conn) {
	tb.Helper()
	d := gorillaws.Dialer{NetDialContext: h.l.dial}
	hdr := http.Header{}
	hdr.Set("Origin", "http://pipe")
	ws, _, err := d.Dial("ws://pipe/", hdr)
	if err != nil {
		tb.Fatalf("dial: %v", err)
	}
	tb.Cleanup(func() { _ = ws.Close() })
	var welcome websocket.Message
	if err := ws.ReadJSON(&welcome); err != nil || welcome.Type != "welcome" {
		tb.Fatalf("read welcome: %v %q", err, welcome.Type)
	}
	c, ok := h.server.GetClient(welcome.Data.(map[string]interface{})["id"].(string))
	if !ok {
		tb.Fatal("the server does not know the client")
	}
	h.mu.Lock()
	h.peers[c] = ws
	h.mu.Unlock()
	return c, ws
}

// full returns a client whose queue is full and stays full: its peer does
// not read, so writePump blocks on the pipe writing the first message, and
// the queue fills behind it with small messages.
// The peer's connection is returned so a test can start reading, which
// drains the queue.
func (h *clientHost) full(tb testing.TB) (*websocket.Client, *gorillaws.Conn) {
	tb.Helper()
	c, ws := h.connect(tb)
	w := h.l.last.Load()
	if err := c.SendMessage(websocket.Message{Type: "filler"}); err != nil {
		tb.Fatalf("fill: %v", err)
	}
	// Once writePump is blocked writing to the peer, it takes nothing more
	// from the queue, so the queue stays full once it fills.
	hostile.Eventually(tb, hostile.Deadline, "writePump blocked on the peer", func() bool {
		return w.writing.Load() > 0
	})
	for range 4096 {
		err := c.SendMessage(websocket.Message{Type: "filler"})
		if errors.Is(err, websocket.ErrSendChannelFull) {
			return c, ws
		}
		if err != nil {
			tb.Fatalf("fill: %v", err)
		}
	}
	tb.Fatal("the client's queue never filled")
	return nil, nil
}

// closed returns a client that has disconnected: its peer closed and the
// server ended its send state.
func (h *clientHost) closed(tb testing.TB) *websocket.Client {
	tb.Helper()
	c, ws := h.connect(tb)
	_ = ws.Close()
	hostile.Eventually(tb, hostile.Deadline, "the client closed", func() bool {
		return errors.Is(c.SendMessage(websocket.Message{Type: "probe"}), websocket.ErrClientNotFound)
	})
	return c
}

// drain returns the messages c's peer has been sent and not yet read, in
// order: it enqueues a marker behind them (the queue is FIFO) and reads up
// to it.
func (h *clientHost) drain(tb testing.TB, c *websocket.Client) []websocket.Message {
	tb.Helper()
	h.mu.Lock()
	ws := h.peers[c]
	h.mu.Unlock()
	if ws == nil {
		tb.Fatal("drain: the client was not connected through this host")
	}
	const marker = "velocity-test-drain-marker"
	if err := c.SendMessage(websocket.Message{Type: marker}); err != nil {
		tb.Fatalf("drain marker: %v", err)
	}
	var got []websocket.Message
	for {
		msg := readNext(tb, ws)
		if msg.Type == marker {
			return got
		}
		got = append(got, msg)
	}
}

// discard reads and drops everything the peer is sent until the connection
// ends.
func discard(ws *gorillaws.Conn) {
	go func() {
		for {
			if _, _, err := ws.NextReader(); err != nil {
				return
			}
		}
	}()
}

// readNext reads the peer's next message.
func readNext(tb testing.TB, ws *gorillaws.Conn) websocket.Message {
	tb.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(hostile.Deadline))
	defer ws.SetReadDeadline(time.Time{})
	var msg websocket.Message
	if err := ws.ReadJSON(&msg); err != nil {
		tb.Fatalf("read: %v", err)
	}
	return msg
}

// readType reads from the peer until a message of type typ arrives.
func readType(tb testing.TB, ws *gorillaws.Conn, typ string) websocket.Message {
	tb.Helper()
	for {
		if msg := readNext(tb, ws); msg.Type == typ {
			return msg
		}
	}
}

// client returns a connected client for a test that observes deliveries.
// name is the test's label for it; the client's ID is the server's.
func (h *clientHost) client(tb testing.TB, name string) *websocket.Client {
	tb.Helper()
	c, _ := h.connect(tb)
	return c
}

// connectedClient returns a client connected to a host of its own, for a
// test that needs the client to take sends (a subscribe confirms through
// its queue) but does not read them.
func connectedClient(tb testing.TB) *websocket.Client {
	tb.Helper()
	c, _ := newClientHost(tb).connect(tb)
	return c
}
