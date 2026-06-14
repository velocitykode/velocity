package websocket

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Logger is the minimal logging interface used by the WebSocket server.
// The framework's log.Logger satisfies this shape; the package stays
// decoupled from the log/ package to preserve its leaf status.
type Logger interface {
	Info(msg string, kvs ...any)
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
}

// sanitizeForLog strips control characters and newlines from a string for safe logging.
func sanitizeForLog(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 32 && r != 127 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Server manages WebSocket connections
type Server struct {
	config     Config
	upgrader   *websocket.Upgrader
	clients    map[string]*Client
	groups     map[string]map[string]*Client
	broadcast  chan Message
	register   chan *Client
	unregister chan *Client
	handlers   map[string]MessageHandler
	middleware []Middleware

	// Fan-out handoff. handleBroadcast (on the run loop) snapshots clients and
	// appends a broadcastJob to fanoutQ under fanoutMu, then pings fanoutSig.
	// The append is the only thing the run loop does for a broadcast, so it can
	// never block on a slow or paused fan-out: fanoutQ is unbounded and fanoutMu
	// is held only for the O(1) append/swap, never across a send. fanoutSig is a
	// cap-1 wakeup that coalesces (a buffered token is enough to make the fanout
	// goroutine drain the whole queue), so signalling is non-blocking too. This
	// replaces the earlier bounded `fanout chan broadcastJob`, whose 256-slot
	// buffer let a backed-up fan-out block the run loop's enqueue and starve
	// register/unregister.
	fanoutMu  sync.Mutex
	fanoutQ   []broadcastJob
	fanoutSig chan struct{}

	// fanoutHook, when non-nil, runs at the start of each broadcast fan-out
	// on the fanout goroutine. Test-only seam to hold a fan-out open so
	// concurrent registration on the run loop can be observed; nil (and free)
	// in production. Set before Start so the fanout goroutine observes it
	// without a data race (goroutine creation establishes happens-before).
	fanoutHook func()

	// Callbacks
	onConnect    atomic.Pointer[func(*Client)]
	onDisconnect atomic.Pointer[DisconnectFunc]
	onError      atomic.Pointer[func(*Client, error)]

	// disconnectListeners receives every client disconnect in addition to
	// onDisconnect. Used by adapters (e.g. broadcast/drivers.WebSocketDriver)
	// to purge their own state without contending with the application's
	// single onDisconnect callback. Mutex-protected via s.mu.
	disconnectListeners []DisconnectFunc

	// Stats
	stats Stats

	// activeConns counts live clients plus admitted connections pending
	// registration, so MaxConnections is enforced at admission time.
	activeConns atomic.Int64

	mu      sync.RWMutex
	running bool
	// stopped marks a terminal, one-shot lifecycle: once Shutdown sets it,
	// the server cannot be restarted (stopChan is closed once and never
	// recreated). Start returns ErrServerClosed when stopped. Guarded by s.mu.
	stopped  bool
	stopChan chan struct{}

	// wg tracks the run-loop goroutine and every per-client read/write
	// pump so Shutdown can wait for them to drain within a caller-supplied
	// deadline.
	wg sync.WaitGroup

	// logger is stored in an atomic.Value so it can be read from paths
	// that already hold s.mu (e.g. JoinGroup) without risking deadlock
	// via re-entrant locking.
	logger atomic.Value // holds loggerHolder{Logger}

	// recoveredPanics counts how many times callWithRecover has caught a
	// panic in a single handler dispatch. Exposed via RecoveredPanics for
	// observability and tests. Audit D-04 follow-up.
	recoveredPanics atomic.Uint64
}

// loggerHolder wraps a Logger so atomic.Value stores a single concrete type.
type loggerHolder struct{ Logger }

// broadcastJob carries a broadcast message together with the client snapshot
// taken under s.mu on the run-loop goroutine. The fan-out (the actual sends)
// runs on the dedicated fanout goroutine so the run loop can keep draining
// register/unregister/broadcast while sends proceed.
type broadcastJob struct {
	message Message
	clients []*Client
}

// SetLogger installs a logger for operational events (connects, disconnects,
// rate-limit violations, recovered panics). Nil disables logging. Safe to
// call concurrently.
func (s *Server) SetLogger(l Logger) {
	s.logger.Store(loggerHolder{Logger: l})
}

// log returns the installed logger, or nil when SetLogger has not been called
// (or was called with nil).
func (s *Server) log() Logger {
	v := s.logger.Load()
	if v == nil {
		return nil
	}
	return v.(loggerHolder).Logger
}

// logInfo emits an info-level event when a logger is configured.
func (s *Server) logInfo(msg string, kvs ...any) {
	if l := s.log(); l != nil {
		l.Info(msg, kvs...)
	}
}

// logWarn emits a warn-level event when a logger is configured.
func (s *Server) logWarn(msg string, kvs ...any) {
	if l := s.log(); l != nil {
		l.Warn(msg, kvs...)
	}
}

// logError emits an error-level event when a logger is configured.
func (s *Server) logError(msg string, kvs ...any) {
	if l := s.log(); l != nil {
		l.Error(msg, kvs...)
	}
}

// New creates a new WebSocket server.
//
// The server lifecycle is one-shot: Start runs it, Shutdown stops it
// permanently. A server cannot be restarted after Shutdown - call New for a
// fresh instance. See Start and Shutdown.
func New(config Config) *Server {
	// Set defaults
	if config.MaxConnections == 0 {
		config.MaxConnections = 10000
	}
	if config.ReadBufferSize == 0 {
		config.ReadBufferSize = 1024
	}
	if config.WriteBufferSize == 0 {
		config.WriteBufferSize = 1024
	}
	if config.MaxMessageSize == 0 {
		config.MaxMessageSize = 512 * 1024 // 512KB
	}
	if config.PingInterval == 0 {
		config.PingInterval = 30 * time.Second
	}
	if config.PongTimeout == 0 {
		config.PongTimeout = 60 * time.Second
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = 10 * time.Second
	}
	// Audit D-03: rate limiting must be opt out, not opt in. A zero value
	// (unconfigured) installs the secure default; a negative value is the
	// explicit opt out and is normalised to 0 so the readPump treats it as
	// unlimited.
	if config.MessageRateLimit == 0 {
		config.MessageRateLimit = DefaultMessageRateLimit
	} else if config.MessageRateLimit < 0 {
		config.MessageRateLimit = 0
	}

	s := &Server{
		config:     config,
		clients:    make(map[string]*Client),
		groups:     make(map[string]map[string]*Client),
		broadcast:  make(chan Message, 256),
		fanoutSig:  make(chan struct{}, 1),
		register:   make(chan *Client, 256),
		unregister: make(chan *Client, 256),
		handlers:   make(map[string]MessageHandler),
		stopChan:   make(chan struct{}),
	}

	s.upgrader = &websocket.Upgrader{
		ReadBufferSize:  config.ReadBufferSize,
		WriteBufferSize: config.WriteBufferSize,
		CheckOrigin:     s.checkOrigin,
	}

	return s
}

// Start begins processing WebSocket connections.
//
// The lifecycle is one-shot: once Shutdown has been called, Start returns
// ErrServerClosed and the server stays dead. Use errors.Is(err,
// ErrServerClosed) to detect a restart attempt. A second Start on a still
// running server returns ErrServerAlreadyRunning.
func (s *Server) Start() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrServerClosed
	}
	if s.running {
		s.mu.Unlock()
		return ErrServerAlreadyRunning
	}
	s.running = true
	s.mu.Unlock()

	s.logInfo("WebSocket server starting", "host", s.config.Host, "port", s.config.Port, "path", s.config.Path)

	// Audit D-04: wrap the run loop in async.Go so a panic from any of
	// the handle* dispatchers (nil deref via callback hot-swap, map race,
	// closed-channel send, etc.) is contained instead of crashing the
	// process. async.Go installs a deferred recover and routes through
	// the package panic hook so observers still see the failure.
	s.wg.Add(1)
	async.Go(func() {
		defer s.wg.Done()
		s.run()
	})

	// Dedicated fan-out goroutine. handleBroadcast snapshots clients under
	// s.mu on the run loop, then hands the snapshot here so the actual sends
	// happen off the run-loop dispatch path - the run loop keeps draining
	// register/unregister/broadcast while a fan-out is in flight. Tracked on
	// s.wg so Shutdown waits for it to drain. Wrapped in async.Go for the
	// same process-level panic containment as the run loop.
	s.wg.Add(1)
	async.Go(func() {
		defer s.wg.Done()
		s.fanoutLoop()
	})
	return nil
}

// Shutdown gracefully stops the server and waits for the run-loop goroutine
// and every per-client read/write pump to drain, bounded by ctx.
//
// It closes the stop channel (which both the run loop and every writePump
// select on) and every live client connection (which unblocks readPump's
// ReadJSON), then waits on the server's WaitGroup. If ctx fires before the
// goroutines finish, Shutdown returns ctx.Err(); otherwise it returns nil.
// Shutdown is safe to call more than once - subsequent calls are no-ops that
// return nil.
//
// Shutdown is terminal: it marks the server stopped so a later Start returns
// ErrServerClosed. The lifecycle is one-shot - create a new Server with New to
// run again.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	// Mark the lifecycle terminal regardless of whether the server ever
	// started: any Shutdown is one-shot, so a later Start must return
	// ErrServerClosed even if Shutdown ran before Start. Close stopChan on the
	// first terminal shutdown - including a shutdown-before-start - so Broadcast
	// always observes a closed stopChan and drops instead of wedging on the
	// undrained buffer. Guard on the prior stopped flag so it is closed exactly
	// once across repeated Shutdown calls.
	alreadyStopped := s.stopped
	wasRunning := s.running
	s.running = false
	s.stopped = true
	if alreadyStopped {
		s.mu.Unlock()
		return nil
	}
	close(s.stopChan)
	s.mu.Unlock()

	if !wasRunning {
		// Never started: no run loop, clients, or pumps to drain. The closed
		// stopChan above is enough to make Broadcast drop.
		return nil
	}

	// Close each live connection so readPump's blocked ReadJSON fails and
	// returns. writePump independently observes stopChan being closed.
	s.mu.RLock()
	for _, client := range s.clients {
		client.Conn.Close()
	}
	s.mu.RUnlock()

	done := make(chan struct{})
	// Not async.Go: trivial WaitGroup waiter, no user code runs here.
	go func() { //safe-goroutine: trivial WaitGroup waiter, no user code runs here
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the main event loop.
//
// Audit D-04 follow-up: every dispatch into handleRegister / handleUnregister
// / handleBroadcast is wrapped in callWithRecover so a single panicking
// handler call does NOT terminate the consumer goroutine. Without this
// inner recover, async.Go's outer wrapper still contains the panic at the
// process level, but the run goroutine exits and the register / unregister
// / broadcast channels back up forever (Start cannot relaunch because
// s.running stays true). The recover keeps the for-loop alive across
// recoverable handler failures (closed-channel sends, callback hot-swap
// nil derefs, transient map races) and counts them via panicCount so
// operators can observe the failure rate.
func (s *Server) run() {
	for {
		select {
		case client := <-s.register:
			s.callWithRecover("handleRegister", func() { s.handleRegister(client) })

		case client := <-s.unregister:
			s.callWithRecover("handleUnregister", func() { s.handleUnregister(client) })

		case message := <-s.broadcast:
			s.callWithRecover("handleBroadcast", func() { s.handleBroadcast(message) })

		case <-s.stopChan:
			return
		}
	}
}

// callWithRecover invokes fn with a deferred recover so a panic in a single
// handler dispatch does not exit the run loop. Recovered panics are logged
// via the Server's configured logger (when present) and counted on
// s.recoveredPanics for observability. The outer async.Go wrapper installed
// by Start remains in place as a final safety net for panics that escape
// this seam (e.g. a panic in the for/select frame itself, vanishingly
// rare in practice).
func (s *Server) callWithRecover(site string, fn func()) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		s.recoveredPanics.Add(1)
		// panicerr.FromRecovered tags the recovered value with the calling
		// goroutine's stack so log readers can locate the panic site.
		s.logError(
			"websocket run-loop panic recovered",
			"site", site,
			"error", panicerr.FromRecovered(r),
		)
	}()
	fn()
}

// RecoveredPanics returns the number of times a panic has been caught and
// contained - by the run loop's inner recover in a single handler dispatch, or
// by the per-client recover in the broadcast fan-out (sendOrDrop). Exposed for
// observability and tests; safe to call concurrently. Audit D-04 follow-up.
func (s *Server) RecoveredPanics() uint64 {
	return s.recoveredPanics.Load()
}

// HandleConnection upgrades HTTP connection to WebSocket
func (s *Server) HandleConnection(w http.ResponseWriter, r *http.Request) {
	// Reserve pump slots on the WaitGroup while still holding the running
	// check, so a concurrent Shutdown (which acquires the write lock before
	// spawning its wg.Wait goroutine) cannot observe a zero counter and
	// race the Add. The slots are released below if upgrade or auth fails.
	s.mu.RLock()
	if !s.running {
		s.mu.RUnlock()
		http.Error(w, "Server not running", http.StatusServiceUnavailable)
		return
	}
	s.wg.Add(2)
	s.mu.RUnlock()

	n := s.activeConns.Add(1)
	if s.config.MaxConnections > 0 && n > int64(s.config.MaxConnections) {
		s.activeConns.Add(-1)
		s.wg.Add(-2)
		http.Error(w, "Connection limit reached", http.StatusServiceUnavailable)
		return
	}

	// Authenticate before upgrading if an auth function is configured
	if s.config.AuthFunc != nil {
		if err := s.config.AuthFunc(r); err != nil {
			s.activeConns.Add(-1)
			s.wg.Add(-2)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Generate the client ID before upgrading so a randomness failure can
	// refuse the connection with a plain HTTP error instead of issuing a
	// predictable ID (socket IDs bind channel-auth signatures).
	id, err := generateID()
	if err != nil {
		s.activeConns.Add(-1)
		s.wg.Add(-2)
		s.logError("Failed to generate client ID", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Upgrade connection
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.activeConns.Add(-1)
		s.wg.Add(-2)
		s.logError("Failed to upgrade connection", "error", err)
		return
	}

	// Create client
	client := &Client{
		ID:       id,
		Conn:     conn,
		Send:     make(chan Message, 256),
		Server:   s,
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}

	// Register client
	s.register <- client

	// Start client goroutines. Pump slots already reserved on the WaitGroup
	// above so Shutdown can wait for them to drain.
	//
	// Audit D-04: route through async.Go so a panic that escapes the
	// pump's own recover (e.g. inside an onConnect callback that captures
	// client state and panics mid-pump) is contained at the goroutine
	// boundary instead of taking the process down. The internal pump
	// recover stays in place for symptom logging; async.Go is the
	// last-resort net.
	async.Go(func() {
		defer s.wg.Done()
		client.writePump()
	})
	async.Go(func() {
		defer s.wg.Done()
		client.readPump()
	})

	// The connect callback is NOT invoked here. It fires at the end of
	// handleRegister, on the run-loop goroutine, after the client is in
	// s.clients - so a callback that calls JoinGroup/SendToClient no longer
	// races registration with a "client not found" error (B49).

	// Update stats
	atomic.AddInt64(&s.stats.ConnectedClients, 1)
}

// handleRegister adds a new client
func (s *Server) handleRegister(client *Client) {
	s.mu.Lock()
	s.clients[client.ID] = client
	s.mu.Unlock()

	s.logInfo("Client connected", "client_id", client.ID)

	// Send welcome message. Non-blocking: handleRegister runs on the run-loop
	// goroutine, so a blocking send into a full channel would stall the entire
	// server (no register/unregister/broadcast would drain). Mirror the
	// select/default used in handleBroadcast.
	select {
	case client.Send <- Message{
		Type: "welcome",
		Data: map[string]interface{}{
			"id":      client.ID,
			"version": "1.0.0",
		},
	}:
	default:
		s.logWarn("Client send channel full, dropping welcome message", "client_id", client.ID)
	}

	// Fire the connect callback now that the client is registered. Running it
	// here (on the run-loop goroutine, after s.clients is set) lets the
	// callback call JoinGroup/SendToClient without racing registration (B49).
	// Wrapped in a recover so a panicking callback cannot take the run loop
	// down - mirrors invokeDisconnectListener.
	if p := s.onConnect.Load(); p != nil {
		s.invokeConnectListener(*p, client)
	}
}

// invokeConnectListener calls fn(client) under a recover so a panicking
// connect callback is contained. Recovered panics are logged when a logger is
// installed and otherwise swallowed. Mirrors invokeDisconnectListener.
func (s *Server) invokeConnectListener(fn func(*Client), client *Client) {
	defer func() {
		if r := recover(); r != nil {
			s.logError("websocket connect listener panic recovered", "client_id", client.ID, "error", panicerr.FromRecovered(r))
		}
	}()
	fn(client)
}

// handleUnregister removes a client.
//
// Disconnect listeners (and the single onDisconnect callback) fire BEFORE
// close(client.Send) so adapters such as broadcast/drivers.WebSocketDriver
// can purge their own state while client.Send is still a live channel.
// Closing first would let a concurrent Broadcast hit `send on closed channel`
// before the listener could clear the stale pointer (audit D-01).
//
// Each listener runs under its own deferred recover so a single panicking
// listener cannot abort the unregister sequence or take the server-side
// run loop down.
func (s *Server) handleUnregister(client *Client) {
	s.mu.Lock()
	_, present := s.clients[client.ID]
	if present {
		delete(s.clients, client.ID)

		// Remove from all groups. client.Groups is guarded by client.mu
		// (see groups.go JoinGroup/LeaveGroup and client.go IsInGroup), so
		// read it under client.mu even though we hold s.mu. Lock order is
		// s.mu -> client.mu, matching JoinGroup/LeaveGroup; no inversion (B51).
		client.mu.Lock()
		for groupName := range client.Groups {
			if group, ok := s.groups[groupName]; ok {
				delete(group, client.ID)
				if len(group) == 0 {
					delete(s.groups, groupName)
				}
			}
		}
		client.mu.Unlock()
	}
	// Snapshot listener list under the same lock so adapters registered
	// concurrently with disconnect see the current set.
	listeners := make([]DisconnectFunc, len(s.disconnectListeners))
	copy(listeners, s.disconnectListeners)
	s.mu.Unlock()

	if !present {
		// Duplicate unregister - nothing to clean up.
		return
	}

	var onDisconnect DisconnectFunc
	if p := s.onDisconnect.Load(); p != nil {
		onDisconnect = *p
	}

	// Fire listeners BEFORE close(client.Send). Each is invoked under its
	// own recover so a misbehaving listener cannot derail the rest of the
	// teardown sequence (recovered panics are logged but not re-raised).
	for _, fn := range listeners {
		s.invokeDisconnectListener(fn, client)
	}
	if onDisconnect != nil {
		s.invokeDisconnectListener(onDisconnect, client)
	}

	// Now that every listener has had a chance to purge its references,
	// close the Send channel via closeSend, which sets client.closed and
	// closes under client.mu. The broadcast fan-out's sendOrDrop serializes on
	// the same client.mu (via trySend) and observes the closed flag instead of
	// racing the close, so the concurrent-disconnect path is -race clean rather
	// than relying solely on a recover. The listener-driven purge that just ran
	// still clears adapter references. We do not re-acquire s.mu: the client is
	// already removed from s.clients, so no other server-side path can reach
	// Send by name.
	client.closeSend()

	s.logInfo("Client disconnected", "client_id", client.ID)

	// Update stats
	s.activeConns.Add(-1)
	atomic.AddInt64(&s.stats.ConnectedClients, -1)
}

// invokeDisconnectListener calls fn(client) under a recover so a panicking
// listener is contained. Recovered panics are logged when a logger is
// installed and otherwise swallowed.
func (s *Server) invokeDisconnectListener(fn DisconnectFunc, client *Client) {
	defer func() {
		if r := recover(); r != nil {
			s.logError("websocket disconnect listener panic recovered", "client_id", client.ID, "error", panicerr.FromRecovered(r))
		}
	}()
	fn(client)
}

// handleBroadcast snapshots the current clients and hands the fan-out to the
// dedicated fanout goroutine.
//
// Snapshot-then-send (mirrors broadcast/drivers.WebSocketDriver.snapshotTargets):
// copy the client references into a local slice under a brief RLock, release the
// lock, then enqueue the snapshot for the fanout goroutine to send. The snapshot
// stays under s.mu (an O(len(clients)) pointer-copy), but the sends - which are
// O(fan-out) and would otherwise occupy the run-loop goroutine for their whole
// duration - run on the fanout goroutine. That keeps run() free to drain
// register/unregister so a newly connected client queued on s.register is
// registered while the previous broadcast is still being delivered.
//
// The handoff is append-to-queue + non-blocking signal, never a bounded-channel
// send: even if the fanout goroutine is paused or slow and the queue has grown
// past any threshold, the run loop only ever takes fanoutMu for an O(1) append
// and pings the cap-1 fanoutSig with a default fall-through. So the run loop can
// never block here, and register/unregister keep draining regardless of fan-out
// backlog. Delivery is preserved: every enqueued job is drained by fanoutLoop.
func (s *Server) handleBroadcast(message Message) {
	s.mu.RLock()
	clients := make([]*Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.RUnlock()

	s.fanoutMu.Lock()
	s.fanoutQ = append(s.fanoutQ, broadcastJob{message: message, clients: clients})
	s.fanoutMu.Unlock()

	// Non-blocking wakeup. A cap-1 buffered token coalesces multiple enqueues
	// into a single pending signal; fanoutLoop drains the whole queue per wake,
	// so one buffered token suffices and a full buffer means a wake is already
	// pending.
	select {
	case s.fanoutSig <- struct{}{}:
	default:
	}
}

// fanoutLoop drains broadcast snapshots and performs the sends off the
// run-loop goroutine. It wakes on fanoutSig, drains every queued job, then
// blocks again; it exits when stopChan is closed. Each job is wrapped in
// callWithRecover as a backstop for non-send panics (e.g. a panicking
// fanoutHook); the targeted send panic is handled per-client in sendOrDrop.
func (s *Server) fanoutLoop() {
	for {
		select {
		case <-s.fanoutSig:
			s.drainFanout()
		case <-s.stopChan:
			return
		}
	}
}

// drainFanout delivers every queued broadcast job, looping until the queue is
// empty. It swaps the whole pending slice out under fanoutMu (O(1)) and delivers
// the batch with the lock released, so handleBroadcast's concurrent append never
// waits on a send. Re-checking after each batch catches jobs appended while the
// previous batch was in flight.
func (s *Server) drainFanout() {
	for {
		s.fanoutMu.Lock()
		if len(s.fanoutQ) == 0 {
			s.fanoutMu.Unlock()
			return
		}
		batch := s.fanoutQ
		s.fanoutQ = nil
		s.fanoutMu.Unlock()

		for _, job := range batch {
			s.callWithRecover("fanout", func() { s.deliver(job) })
		}
	}
}

// deliver fans a snapshotted broadcast out to every client, sending through
// sendOrDrop so neither a full nor a closed Send channel can abort the loop.
func (s *Server) deliver(job broadcastJob) {
	if hook := s.fanoutHook; hook != nil {
		hook()
	}
	for _, client := range job.clients {
		s.sendOrDrop(client, job.message)
	}
	// MessagesSent is counted once at the actual wire write in writePump, not
	// here at enqueue (which would double-count, and previously also counted
	// clients skipped for a full buffer).
}

// sendOrDrop enqueues message onto client.Send without blocking and without
// letting one stale client abort the rest of the fan-out. It delegates to
// client.trySend, which serializes the send against handleUnregister's
// closeSend on client.mu: when the client unregistered between the snapshot and
// this send (the snapshot and send run on different goroutines - run loop vs
// fanout - so that race is real), trySend observes client.closed under the lock
// and skips the send instead of racing the close. A full buffer is skipped and
// warned. Either way iteration continues so every other snapshot-time client
// still receives the message - preserving the all-snapshot-clients invariant.
//
// The deferred recover remains a backstop for a Send channel closed out-of-band
// (without client.closed set, e.g. a caller that closes Send directly). Such a
// recovered panic is counted on s.recoveredPanics (same counter as the run
// loop) and logged.
func (s *Server) sendOrDrop(client *Client, message Message) {
	defer func() {
		if r := recover(); r != nil {
			s.recoveredPanics.Add(1)
			s.logError(
				"websocket broadcast send recovered",
				"client_id", client.ID,
				"error", panicerr.FromRecovered(r),
			)
		}
	}()
	switch queued, closed := client.trySend(message); {
	case queued:
	case closed:
		// Client unregistered between snapshot and send; drop.
		s.logWarn("Client send channel closed, skipping message", "client_id", client.ID)
	default:
		// Client's send channel is full, skip.
		s.logWarn("Client send channel full, skipping message", "client_id", client.ID)
	}
}

// checkOrigin validates the origin of the connection.
// If no AllowedOrigins are configured, only same-origin requests are accepted:
// the Origin header host (case-insensitive) must match the request Host. A
// missing Origin header is rejected unless AllowEmptyOrigin is explicitly set.
// Use AllowedOrigins: []string{"*"} to allow all non-empty origins.
//
// A missing Origin header is governed solely by AllowEmptyOrigin, ahead of
// both the same-origin and allowlist paths. In particular AllowedOrigins
// []string{"*"} does NOT accept an empty Origin on its own; it still requires
// AllowEmptyOrigin=true (default-to-secure).
func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")

	// Empty Origin is decided up front for every configuration. Browsers
	// always send Origin on WS upgrades, so an empty Origin almost always
	// means a non-browser client. Treat as untrusted unless the application
	// opted in - this overrides even an AllowedOrigins "*" wildcard.
	if origin == "" {
		return s.config.AllowEmptyOrigin
	}

	// If no allowed origins specified, only allow same-origin.
	if len(s.config.AllowedOrigins) == 0 {
		o, err := url.Parse(origin)
		if err != nil || o.Host == "" {
			return false
		}
		// Restrict to HTTP(S) so non-web schemes (chrome-extension://,
		// ftp://, file://, etc.) cannot pass the same-origin gate by
		// merely matching the host. Browser WebSocket upgrades always
		// send an http(s) Origin; everything else is hostile or buggy.
		switch strings.ToLower(o.Scheme) {
		case "http", "https":
		default:
			return false
		}
		// url.Parse on an Origin like "https://example.com" populates
		// o.Host; compare hostnames case-insensitively against r.Host
		// (which carries the request authority for HTTP/1.1 upgrades).
		return strings.EqualFold(o.Host, r.Host)
	}

	// Check if origin is in allowed list
	for _, allowed := range s.config.AllowedOrigins {
		if allowed == "*" {
			return true
		}
		if allowed == origin {
			return true
		}
	}

	return false
}

// On registers a message handler.
// Panics with *contract.RegistrationError if handler is nil.
func (s *Server) On(messageType string, handler MessageHandler) {
	if handler == nil {
		panic(contract.NewRegistrationError("websocket", fmt.Sprintf("nil handler for message type %q", messageType)))
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Apply middleware
	finalHandler := handler
	for i := len(s.middleware) - 1; i >= 0; i-- {
		finalHandler = s.middleware[i](finalHandler)
	}

	s.handlers[messageType] = finalHandler
}

// Use adds middleware.
// Panics with *contract.RegistrationError if middleware is nil.
func (s *Server) Use(middleware Middleware) {
	if middleware == nil {
		panic(contract.NewRegistrationError("websocket", "nil middleware"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.middleware = append(s.middleware, middleware)
}

// OnConnect sets the connect callback.
//
// The callback runs on the run-loop goroutine after registration completes, so
// the client is already in s.clients - JoinGroup, SendToClient, and the other
// lookup-by-ID helpers work from inside it. Because it runs on the run loop, a
// slow callback delays message routing (register/unregister/broadcast) for all
// clients; keep it fast or hand off heavy work to another goroutine. A panic in
// the callback is recovered and logged, not propagated.
func (s *Server) OnConnect(fn func(*Client)) {
	if fn == nil {
		s.onConnect.Store(nil)
		return
	}
	s.onConnect.Store(&fn)
}

// OnDisconnect sets the disconnect callback
func (s *Server) OnDisconnect(fn DisconnectFunc) {
	if fn == nil {
		s.onDisconnect.Store(nil)
		return
	}
	s.onDisconnect.Store(&fn)
}

// AddOnDisconnect appends a disconnect listener that fires alongside the
// single OnDisconnect callback. Listeners are invoked BEFORE client.Send is
// closed so adapters can drop stale references and avoid `send on closed
// channel` panics from concurrent broadcasts (audit D-01).
//
// Multiple listeners may be registered; they fire in registration order.
// A nil fn is rejected to keep the unregister path total. Safe to call
// concurrently.
func (s *Server) AddOnDisconnect(fn DisconnectFunc) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disconnectListeners = append(s.disconnectListeners, fn)
}

// OnError sets the error callback
func (s *Server) OnError(fn func(*Client, error)) {
	if fn == nil {
		s.onError.Store(nil)
		return
	}
	s.onError.Store(&fn)
}

// Broadcast sends a message to all connected clients.
//
// Once the server is shut down the run loop no longer drains the broadcast
// channel, so a plain send would block forever after the 256-slot buffer
// fills. Broadcast selects on stopChan (closed exactly once by Shutdown and
// never reassigned, so the read is race-free) and drops the message instead of
// wedging the caller.
func (s *Server) Broadcast(message Message) {
	select {
	case s.broadcast <- message:
	case <-s.stopChan:
		// Server is stopped; drop rather than block on an undrained channel.
	}
}

// SendToClient sends a message to a specific client
func (s *Server) SendToClient(clientID string, message Message) error {
	s.mu.RLock()
	client, ok := s.clients[clientID]
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("client %s not found: %w", sanitizeForLog(clientID), ErrClientNotFound)
	}

	select {
	case client.Send <- message:
		// Counted once at the wire write in writePump, not here at enqueue.
	default:
		return fmt.Errorf("client %s send channel full: %w", sanitizeForLog(clientID), ErrSendChannelFull)
	}

	return nil
}

// GetStats returns server statistics
func (s *Server) GetStats() Stats {
	return Stats{
		ConnectedClients: atomic.LoadInt64(&s.stats.ConnectedClients),
		MessagesSent:     atomic.LoadInt64(&s.stats.MessagesSent),
		MessagesReceived: atomic.LoadInt64(&s.stats.MessagesReceived),
		BytesSent:        atomic.LoadInt64(&s.stats.BytesSent),
		BytesReceived:    atomic.LoadInt64(&s.stats.BytesReceived),
	}
}

// GetClient returns a client by ID
func (s *Server) GetClient(id string) (*Client, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	client, ok := s.clients[id]
	return client, ok
}

// HandleRaw upgrades the HTTP connection to a WebSocket and returns the raw
// connection together with a release func. Unlike HandleConnection, it does
// NOT register a managed Client, does NOT start readPump/writePump
// goroutines, and does NOT enter the message routing system. The caller owns
// the connection and is responsible for reading, writing, and closing it.
//
// HandleRaw is always gated and enforces the same admission policy as
// HandleConnection, in order: the running lifecycle (ErrServerNotRunning
// before Start, ErrServerClosed after Shutdown), the configured AuthFunc
// (HTTP 401 on reject), and MaxConnections (ErrConnectionLimit on overflow).
// Origin checking applies through the server's shared upgrader and is NOT
// weakened here.
//
// The returned release func decrements the active-connection count and MUST
// be called exactly once when the caller is done with the conn - typically
// deferred next to conn.Close:
//
//	conn, release, err := s.HandleRaw(w, r)
//	if err != nil {
//	    return
//	}
//	defer release()
//	defer conn.Close()
//
// On any error the returned conn is nil and release is a no-op, so callers may
// defer release before checking err if they prefer. release is idempotent.
//
// Escape hatch: to perform an unchecked upgrade (no running gate, no auth, no
// connection limit), construct your own gorilla websocket.Upgrader directly
// and call Upgrade - this method is always gated.
func (s *Server) HandleRaw(w http.ResponseWriter, r *http.Request) (*websocket.Conn, func(), error) {
	noop := func() {}

	// Running gate: mirror HandleConnection. A terminal Shutdown reports
	// ErrServerClosed (one-shot lifecycle); a server that never started
	// reports ErrServerNotRunning.
	s.mu.RLock()
	if s.stopped {
		s.mu.RUnlock()
		http.Error(w, "Server closed", http.StatusServiceUnavailable)
		return nil, noop, ErrServerClosed
	}
	if !s.running {
		s.mu.RUnlock()
		http.Error(w, "Server not running", http.StatusServiceUnavailable)
		return nil, noop, ErrServerNotRunning
	}
	s.mu.RUnlock()

	// Authenticate before upgrading if an auth function is configured.
	if s.config.AuthFunc != nil {
		if err := s.config.AuthFunc(r); err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return nil, noop, fmt.Errorf("websocket auth: %w", err)
		}
	}

	// Reserve a connection slot, enforcing MaxConnections with rollback. Do
	// NOT touch s.wg: no pumps run for a raw conn, so there is nothing for
	// Shutdown to drain.
	n := s.activeConns.Add(1)
	if s.config.MaxConnections > 0 && n > int64(s.config.MaxConnections) {
		s.activeConns.Add(-1)
		http.Error(w, "Connection limit reached", http.StatusServiceUnavailable)
		return nil, noop, ErrConnectionLimit
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.activeConns.Add(-1)
		return nil, noop, fmt.Errorf("websocket upgrade: %w", err)
	}

	// release rolls the activeConns reservation back when the caller is done.
	// sync.Once keeps it idempotent so a double-deferred release cannot drive
	// the count negative and silently inflate the available headroom.
	var once sync.Once
	release := func() {
		once.Do(func() { s.activeConns.Add(-1) })
	}
	return conn, release, nil
}

// GetClients returns all connected clients
func (s *Server) GetClients() map[string]*Client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clients := make(map[string]*Client)
	for id, client := range s.clients {
		clients[id] = client
	}
	return clients
}
