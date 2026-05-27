package drivers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/websocket"
)

// Logger is the minimal logging interface used by the broadcast WebSocket
// driver. The framework's log.Logger satisfies this interface; keeping the
// contract local keeps broadcast/ free of a log/ dependency.
type Logger interface {
	Info(msg string, kvs ...any)
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
}

// ChannelAuthorizer checks if a WebSocket client is allowed to join a channel.
// Must be set for private- and presence- channels to be accessible.
type ChannelAuthorizer func(client *websocket.Client, channel string) bool

// TokenVerifier checks the HMAC auth token presented by a client when
// subscribing to a private- or presence- channel. The token is produced by
// BroadcastManager.SignAuthToken on the HTTP auth endpoint and forwarded by
// the client on subscribe. Returning false rejects the subscription. When
// the verifier is nil, no token check is performed and the channel authorizer
// alone gates access; install the verifier (e.g. broadcast.BroadcastManager
// .VerifyAuthToken) to enforce audit H-25.
type TokenVerifier func(socketID, channel, token string) bool

// denyAllChannelAuthorizer is the secure default: deny every subscription to
// a private- or presence- channel. Applications must explicitly install an
// authorizer via SetAuthorizer.
func denyAllChannelAuthorizer(client *websocket.Client, channel string) bool {
	return false
}

// WebSocketDriver adapts the existing WebSocket server for broadcasting
type WebSocketDriver struct {
	server         *websocket.Server
	channels       map[string]map[string]*websocket.Client // channel -> socketID -> client
	clientSubs     map[string]map[string]struct{}          // clientID -> channel set (audit D-03)
	authorizer     ChannelAuthorizer
	verifier       TokenVerifier
	mu             sync.RWMutex
	droppedCount   atomic.Uint64
	blockingSendTO time.Duration // 0 means non-blocking (drop on full)
	onDrop         func(clientID, channel, event string)

	// maxChannelsPerClient caps the number of distinct channels a single
	// WebSocket client may subscribe to. Zero falls back to
	// DefaultMaxChannelsPerClient. Audit D-03: without a cap, an
	// unauthenticated client can spam subscribe to unique public channel
	// names and inflate the channels map to unbounded size.
	maxChannelsPerClient int

	// maxChannelNameLength caps the length of a subscribe target channel
	// name. Zero falls back to DefaultMaxChannelNameLength. Audit D-03:
	// without a cap, an attacker can submit megabyte-sized channel names
	// to consume server memory per subscribe.
	maxChannelNameLength int

	// logger is stored via atomic.Value so drop-path logging can read it
	// without contending with the channel-membership lock held by
	// Broadcast/BroadcastExcept.
	logger atomic.Value // holds loggerHolder{Logger}

	// opaqueSeed is a process-local 32-byte random key used to derive opaque
	// per-(channel, socket) identifiers returned by GetClients. The seed is
	// lazy-initialised on first use via opaqueSeedOnce so test fixtures that
	// construct the driver as a bare literal do not crash.
	//
	// Audit M-27: presence channels must NOT leak the raw internal socket ID
	// (a per-connection nonce) to channel peers - it lets one tenant fingerprint
	// every other tenant's connection lifetime and trivially target individual
	// sockets for DoS. The HMAC binds (socket, channel) so the same socket on
	// two channels gets two unlinkable opaque IDs.
	opaqueSeedOnce sync.Once
	opaqueSeed     [32]byte
}

// loggerHolder wraps a Logger so atomic.Value stores a single concrete type.
type loggerHolder struct{ Logger }

// SetLogger installs a logger for operational events (e.g. dropped broadcast
// messages when no onDrop callback is configured). Nil disables logging.
// Safe to call concurrently.
func (d *WebSocketDriver) SetLogger(l Logger) {
	d.logger.Store(loggerHolder{Logger: l})
}

// log returns the installed logger, or nil when SetLogger has not been called.
func (d *WebSocketDriver) log() Logger {
	v := d.logger.Load()
	if v == nil {
		return nil
	}
	return v.(loggerHolder).Logger
}

// DefaultMaxChannelsPerClient is the per-client channel subscription cap
// applied when the driver is constructed without WithMaxChannelsPerClient.
// Audit D-03.
const DefaultMaxChannelsPerClient = 100

// DefaultMaxChannelNameLength is the per-subscribe channel name length cap
// applied when the driver is constructed without WithMaxChannelNameLength.
// Audit D-03.
const DefaultMaxChannelNameLength = 256

// DriverOption configures a WebSocketDriver.
type DriverOption func(*WebSocketDriver)

// WithMaxChannelsPerClient sets the per-client channel subscription cap.
// A value of 0 keeps the default (DefaultMaxChannelsPerClient). A negative
// value disables the cap (not recommended in untrusted multi-tenant
// deployments). Audit D-03.
func WithMaxChannelsPerClient(n int) DriverOption {
	return func(d *WebSocketDriver) {
		d.maxChannelsPerClient = n
	}
}

// WithMaxChannelNameLength sets the per-subscribe channel name length cap.
// A value of 0 keeps the default (DefaultMaxChannelNameLength). A negative
// value disables the cap (not recommended). Audit D-03.
func WithMaxChannelNameLength(n int) DriverOption {
	return func(d *WebSocketDriver) {
		d.maxChannelNameLength = n
	}
}

// WithBlockingSend returns an option that makes Broadcast and BroadcastExcept
// block for up to the given duration when a client's send buffer is full,
// rather than dropping immediately. A zero or negative duration disables
// blocking and restores the drop-on-full default.
func WithBlockingSend(timeout time.Duration) DriverOption {
	return func(d *WebSocketDriver) {
		d.blockingSendTO = timeout
	}
}

// WithOnDrop installs a callback invoked whenever a message is dropped because
// a client's Send buffer was full. Intended for metric/event dispatching; the
// callback must not block the send path.
func WithOnDrop(fn func(clientID, channel, event string)) DriverOption {
	return func(d *WebSocketDriver) {
		d.onDrop = fn
	}
}

// NewWebSocketDriver creates a new WebSocket driver.
// The default authorizer denies all requests to private- and presence-
// channels. Callers must install an authorizer via SetAuthorizer to grant
// access.
func NewWebSocketDriver(config websocket.Config, opts ...DriverOption) *WebSocketDriver {
	driver := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		clientSubs: make(map[string]map[string]struct{}),
		authorizer: denyAllChannelAuthorizer,
	}

	for _, opt := range opts {
		opt(driver)
	}

	// Apply secure defaults for the D-03 caps. A negative value left in
	// place after options run is the explicit opt out.
	if driver.maxChannelsPerClient == 0 {
		driver.maxChannelsPerClient = DefaultMaxChannelsPerClient
	}
	if driver.maxChannelNameLength == 0 {
		driver.maxChannelNameLength = DefaultMaxChannelNameLength
	}

	// Create WebSocket server
	server := websocket.New(config)
	driver.server = server

	// Register channel handlers
	server.On("subscribe", driver.handleSubscribe)
	server.On("unsubscribe", driver.handleUnsubscribe)
	server.On("client-event", driver.handleClientEvent)

	// Audit D-01: purge stale client pointers from the channels map the
	// moment the server unregisters a client. The listener fires BEFORE
	// close(client.Send) so a concurrent Broadcast that snapshotted the
	// pointer can still complete safely without panicking on
	// send-on-closed-channel.
	server.AddOnDisconnect(func(c *websocket.Client) {
		driver.purgeClient(c.ID)
	})

	// Start the server
	server.Start()

	return driver
}

// Broadcast sends an event to channels. If a client's Send buffer is full,
// the message is either dropped (default) or the call blocks for up to
// blockingSendTO (configured via WithBlockingSend). Dropped messages are
// counted and the onDrop callback (if any) is invoked.
//
// Per audit M-28 the fan-out runs in two phases: snapshot the subscriber set
// under the channels-map RLock, release the lock, then iterate the local
// snapshot and write. Holding the RLock across writes lets a single slow
// client gate every concurrent subscribe / unsubscribe / broadcast on the
// affected channel for the full blockingSendTO; the snapshot-then-send
// pattern keeps the lock window O(subscribers) memcpy rather than
// O(subscribers * write timeout).
func (d *WebSocketDriver) Broadcast(channels []string, event string, data interface{}) error {
	targets := d.snapshotTargets(channels, "")
	for _, t := range targets {
		d.sendOrDrop(t.client, t.channel, event, data)
	}
	return nil
}

// BroadcastExcept broadcasts to all except specified socket. Same two-phase
// fan-out as Broadcast - see that method for the lock-hold rationale.
func (d *WebSocketDriver) BroadcastExcept(channels []string, event string, data interface{}, socketID string) error {
	targets := d.snapshotTargets(channels, socketID)
	for _, t := range targets {
		d.sendOrDrop(t.client, t.channel, event, data)
	}
	return nil
}

// broadcastTarget pairs a snapshot subscriber with the channel under which it
// was selected, so the eventual send still attributes drops/onDrop to the
// originating channel.
type broadcastTarget struct {
	client  *websocket.Client
	channel string
}

// snapshotTargets walks the channels-map under d.mu.RLock and returns the
// flattened list of (client, channel) tuples to receive the broadcast. When
// exceptSocketID is non-empty, the matching socket is skipped at snapshot
// time. The caller then iterates this slice OUTSIDE the lock so a slow
// websocket.Client.Send recipient cannot block concurrent
// subscribe/unsubscribe/broadcast traffic on the same channel.
//
// Stale-pointer safety (audit D-01):
//
//   - Primary defence: NewWebSocketDriver registers a server-side
//     OnDisconnect listener that calls purgeClient, removing the client
//     from every channels map BEFORE the server closes client.Send. So a
//     snapshot taken after disconnect cannot include the dead client.
//   - Defensive defence: sendOrDrop wraps the send in a recover so if a
//     snapshot was taken in the narrow window between OnDisconnect firing
//     and close(client.Send), the eventual `send on closed channel` panic
//     is contained, the dropped count is incremented, and purgeClient is
//     re-invoked synchronously to self-heal.
func (d *WebSocketDriver) snapshotTargets(channels []string, exceptSocketID string) []broadcastTarget {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Pre-size to the sum of channel subscriber counts so the common case
	// avoids a re-allocation. Reading len(d.channels[c]) under RLock is safe.
	total := 0
	for _, c := range channels {
		total += len(d.channels[c])
	}
	if total == 0 {
		return nil
	}

	targets := make([]broadcastTarget, 0, total)
	for _, c := range channels {
		clients, exists := d.channels[c]
		if !exists {
			continue
		}
		for id, client := range clients {
			if exceptSocketID != "" && id == exceptSocketID {
				continue
			}
			targets = append(targets, broadcastTarget{client: client, channel: c})
		}
	}
	return targets
}

// sendOrDrop attempts to deliver a message to client's Send channel. When a
// blocking-send timeout is configured it waits up to that duration; otherwise
// it drops immediately on full buffer. Drops increment droppedCount and
// trigger the onDrop callback (if set).
//
// Audit D-01 defensive guard: a `send on closed channel` panic is the
// symptom of a stale pointer surviving the OnDisconnect window. The primary
// defence (the purgeClient listener installed in NewWebSocketDriver) closes
// that window for every typical disconnect, but a snapshot taken in the
// narrow race between listener-fire and close(Send) would still trigger a
// panic on the send case (a closed channel is "ready" for send, beating the
// default case in the non-blocking select). We recover, count the failure
// as a drop, and re-run purgeClient synchronously so a misbehaving consumer
// or a missed listener cannot leave the map poisoned for subsequent
// broadcasts.
func (d *WebSocketDriver) sendOrDrop(client *websocket.Client, channel, event string, data interface{}) {
	defer func() {
		if r := recover(); r != nil {
			// Count the dropped message and clear the stale pointer.
			d.recordDrop(client.ID, channel, event)
			d.purgeClient(client.ID)
			if logger := d.log(); logger != nil {
				logger.Warn("velocity/broadcast: recovered from send-on-closed-channel; purged client",
					"client_id", client.ID, "channel", channel, "event", event, "panic", fmt.Sprintf("%v", r))
			}
		}
	}()

	msg := websocket.Message{Type: event, Data: data}

	if d.blockingSendTO <= 0 {
		select {
		case client.Send <- msg:
			return
		default:
			d.recordDrop(client.ID, channel, event)
			return
		}
	}

	// Blocking path with timeout. Uses a timer rather than time.After so the
	// underlying resources are released promptly when the send succeeds.
	t := time.NewTimer(d.blockingSendTO)
	defer t.Stop()

	select {
	case client.Send <- msg:
		return
	case <-t.C:
		d.recordDrop(client.ID, channel, event)
	}
}

// purgeClient removes a client from every channel it was subscribed to.
// Used as the OnDisconnect listener registered by NewWebSocketDriver and as
// the self-heal step in sendOrDrop's defensive recover (audit D-01).
//
// Holds the channels-map write lock for the duration of the walk. The walk
// is O(channels) which is bounded by the application's subscription set and
// runs at most once per disconnect, so the lock window stays small.
func (d *WebSocketDriver) purgeClient(clientID string) {
	if clientID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for channel, clients := range d.channels {
		if _, ok := clients[clientID]; ok {
			delete(clients, clientID)
			if len(clients) == 0 {
				delete(d.channels, channel)
			}
		}
	}
	// Drop the per-client subscription bookkeeping so a disconnect frees
	// the D-03 cap budget for any future reconnect of the same ID.
	delete(d.clientSubs, clientID)
}

func (d *WebSocketDriver) recordDrop(clientID, channel, event string) {
	d.droppedCount.Add(1)
	if d.onDrop != nil {
		d.onDrop(clientID, channel, event)
		return
	}
	if logger := d.log(); logger != nil {
		logger.Warn("velocity/broadcast: dropped message", "client_id", clientID, "channel", channel, "event", event)
	}
}

// DroppedCount returns the total number of messages dropped due to full send
// buffers across the lifetime of the driver. It is safe to call concurrently.
func (d *WebSocketDriver) DroppedCount() uint64 {
	return d.droppedCount.Load()
}

// GetClients returns opaque per-channel identifiers for every socket currently
// subscribed to the given channel.
//
// The returned values are 16-hex-character HMACs of (socketID, channel) under
// a process-local random seed. They are:
//
//   - stable for the lifetime of a subscription (same socket on same channel
//     always hashes to the same opaque value while it stays subscribed)
//   - unlinkable across channels (the same socket on two channels produces
//     two different opaque IDs)
//   - unlinkable across server instances (the seed is regenerated on every
//     process start; there is no persisted secret)
//   - never reversible back to the raw socket ID without the seed
//
// This intentionally diverges from the previous behaviour, which returned the
// raw internal socket ID. Raw socket IDs are per-connection nonces meant to
// stay inside the server, and returning them to channel peers leaked enough
// information to fingerprint connection lifetimes, target individual sockets
// for DoS, and correlate the same user across tenants. See audit M-27.
//
// Pusher-protocol parity: real Pusher exposes a caller-supplied "user_id" plus
// an opaque "info" blob on presence channels, not the connection's socket_id.
// Velocity now lines up with that model. Applications that need to identify
// peers in a presence channel should attach domain identity in the channel
// authorizer / presence-data func rather than rely on this identifier.
func (d *WebSocketDriver) GetClients(channel string) []string {
	d.mu.RLock()
	socketIDs := make([]string, 0, len(d.channels[channel]))
	if clients, exists := d.channels[channel]; exists {
		for id := range clients {
			socketIDs = append(socketIDs, id)
		}
	}
	d.mu.RUnlock()

	if len(socketIDs) == 0 {
		return nil
	}

	seed := d.getOpaqueSeed()
	opaque := make([]string, len(socketIDs))
	for i, id := range socketIDs {
		opaque[i] = computeOpaqueClientID(seed, id, channel)
	}
	return opaque
}

// getOpaqueSeed returns the process-local seed, generating one on first call.
// crypto/rand.Read failure is fatal: without a seed we would either leak raw
// socket IDs or hand out predictable values. Both options violate the contract
// of GetClients, so we propagate by panicking - this is a startup-class
// failure per CLAUDE.md rule 10 (never panic in library code EXCEPT for
// unrecoverable startup configuration).
func (d *WebSocketDriver) getOpaqueSeed() [32]byte {
	d.opaqueSeedOnce.Do(func() {
		if _, err := rand.Read(d.opaqueSeed[:]); err != nil {
			panic(fmt.Sprintf("velocity/broadcast: crypto/rand failed seeding opaque client IDs: %v", err))
		}
	})
	return d.opaqueSeed
}

// computeOpaqueClientID returns the first 8 bytes (16 hex chars) of
// HMAC-SHA256(seed, socketID + 0x00 + channel). The NUL separator prevents
// the (alice, room) collision against (alic, eroom) etc. We truncate to 16
// hex chars to keep the wire payload small; the 64-bit space is still
// astronomically resistant to collision within a single channel (subscriber
// counts are bounded by Server.MaxConnections; 64 bits dwarfs any plausible
// presence list).
func computeOpaqueClientID(seed [32]byte, socketID, channel string) string {
	mac := hmac.New(sha256.New, seed[:])
	mac.Write([]byte(socketID))
	mac.Write([]byte{0x00})
	mac.Write([]byte(channel))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// ErrChannelLimit is returned by Subscribe (and surfaced by handleSubscribe)
// when a client tries to subscribe to more channels than the per-connection
// cap allows. Audit D-03.
var ErrChannelLimit = fmt.Errorf("velocity/broadcast: channel subscription limit reached")

// ErrChannelNameTooLong is returned by Subscribe (and surfaced by
// handleSubscribe) when the channel name length exceeds the configured cap.
// Audit D-03 follow-up: this guard lives inside Subscribe so direct
// callers (programmatic subscribe paths, anything that bypasses the WS
// message handler) cannot route past the length check.
var ErrChannelNameTooLong = fmt.Errorf("velocity/broadcast: channel name exceeds configured length cap")

// Subscribe adds a client to a channel. Enforces both the per-client
// channel cap (WithMaxChannelsPerClient) and the per-name length cap
// (WithMaxChannelNameLength). Audit D-03.
//
// Returns ErrChannelNameTooLong if channel exceeds the length cap.
// Returns ErrChannelLimit if adding the channel would exceed the per-client
// cap; idempotent for re-subscribes (already-subscribed channels do not
// count against the cap a second time).
func (d *WebSocketDriver) Subscribe(channel string, client *websocket.Client) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Audit D-03 follow-up: gate channel-name length here so direct
	// callers of Subscribe share the same protection that handleSubscribe
	// applies to WS-message-driven subscribes. Without this, an internal
	// path that bypasses handleSubscribe could still inflate clientSubs /
	// channels with megabyte-sized channel names.
	if d.maxChannelNameLength > 0 && len(channel) > d.maxChannelNameLength {
		return ErrChannelNameTooLong
	}

	// Lazy-init the per-client bookkeeping map so test fixtures that build
	// the driver as a bare struct literal (skipping NewWebSocketDriver) do
	// not nil-panic. Same pattern as opaqueSeedOnce.
	if d.clientSubs == nil {
		d.clientSubs = make(map[string]map[string]struct{})
	}

	// Initialise the per-client set if needed so a re-subscribe to an
	// existing channel is a no-op rather than a cap hit.
	subs, ok := d.clientSubs[client.ID]
	if !ok {
		subs = make(map[string]struct{})
		d.clientSubs[client.ID] = subs
	}

	if _, already := subs[channel]; !already {
		// Enforce cap only on new memberships. A negative cap disables
		// enforcement (caller opt out).
		if d.maxChannelsPerClient > 0 && len(subs) >= d.maxChannelsPerClient {
			// Drop the empty bookkeeping entry created above so we do
			// not leak it for a client that never lands a subscription.
			if len(subs) == 0 {
				delete(d.clientSubs, client.ID)
			}
			return ErrChannelLimit
		}
		subs[channel] = struct{}{}
	}

	if d.channels[channel] == nil {
		d.channels[channel] = make(map[string]*websocket.Client)
	}

	d.channels[channel][client.ID] = client
	return nil
}

// Unsubscribe removes a client from a channel
func (d *WebSocketDriver) Unsubscribe(channel string, clientID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if clients, exists := d.channels[channel]; exists {
		delete(clients, clientID)

		// Clean up empty channels
		if len(clients) == 0 {
			delete(d.channels, channel)
		}
	}

	// Drop from the per-client subscription set so the D-03 cap reflects
	// only currently-held memberships.
	if subs, ok := d.clientSubs[clientID]; ok {
		delete(subs, channel)
		if len(subs) == 0 {
			delete(d.clientSubs, clientID)
		}
	}

	return nil
}

// handleSubscribe handles channel subscription requests
func (d *WebSocketDriver) handleSubscribe(client *websocket.Client, msg websocket.Message) error {
	data, ok := msg.Data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid subscribe data")
	}

	channel, ok := data["channel"].(string)
	if !ok {
		return fmt.Errorf("channel not specified")
	}

	// Audit D-03: reject oversized channel names BEFORE consulting the
	// authorizer or touching any map. Without this gate an attacker can
	// submit megabyte-sized channel strings on the unauthenticated public
	// path and force the server to copy them into clientSubs / channels.
	//
	// Subscribe enforces the same cap (D-03 follow-up); this early reject
	// is retained so the returned error carries the configured cap value
	// for nicer diagnostics. Wrap ErrChannelNameTooLong so callers can
	// errors.Is the sentinel regardless of which seam rejected.
	if d.maxChannelNameLength > 0 && len(channel) > d.maxChannelNameLength {
		return fmt.Errorf("%w: %d characters", ErrChannelNameTooLong, d.maxChannelNameLength)
	}

	// Authorize private and presence channels. The default authorizer is
	// deny-all, so a missing setup fails closed rather than silently allowing.
	if strings.HasPrefix(channel, "private-") || strings.HasPrefix(channel, "presence-") {
		d.mu.RLock()
		auth := d.authorizer
		verify := d.verifier
		d.mu.RUnlock()
		if auth == nil {
			auth = denyAllChannelAuthorizer
		}
		if !auth(client, channel) {
			return fmt.Errorf("velocity/broadcast: unauthorized to subscribe to channel %s", channel)
		}

		// When a token verifier is installed (audit H-25 wiring), the
		// inbound subscribe message MUST carry an "auth" string produced
		// by BroadcastManager.Auth and the HMAC must verify against the
		// (socketID, channel) pair. VerifyAuthToken does the constant-time
		// comparison; we just gate on its bool result.
		if verify != nil {
			token, _ := data["auth"].(string)
			if token == "" || !verify(client.ID, channel, token) {
				return fmt.Errorf("velocity/broadcast: invalid auth token for channel %s", channel)
			}
		}
	}

	// Add client to channel
	if err := d.Subscribe(channel, client); err != nil {
		return err
	}

	// Send subscription confirmation
	return client.SendJSON("subscription_succeeded", map[string]interface{}{
		"channel": channel,
	})
}

// handleUnsubscribe handles channel unsubscription requests
func (d *WebSocketDriver) handleUnsubscribe(client *websocket.Client, msg websocket.Message) error {
	data, ok := msg.Data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid unsubscribe data")
	}

	channel, ok := data["channel"].(string)
	if !ok {
		return fmt.Errorf("channel not specified")
	}

	// Remove client from channel
	if err := d.Unsubscribe(channel, client.ID); err != nil {
		return err
	}

	// Send unsubscription confirmation
	return client.SendJSON("unsubscription_succeeded", map[string]interface{}{
		"channel": channel,
	})
}

// handleClientEvent handles client-to-client events.
//
// Per audit H-26, client events (a.k.a. "whisper" in the Pusher protocol)
// are restricted to private- and presence- channels, and the sender must be
// a current subscriber of the channel. Anything else lets an unrelated
// connection forge events on channels it has not joined.
func (d *WebSocketDriver) handleClientEvent(client *websocket.Client, msg websocket.Message) error {
	data, ok := msg.Data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid client event data")
	}

	channel, ok := data["channel"].(string)
	if !ok {
		return fmt.Errorf("channel not specified")
	}

	event, ok := data["event"].(string)
	if !ok {
		return fmt.Errorf("event not specified")
	}

	// Reject client events on public channels: Pusher's "client events" rule
	// only permits them on private/presence channels.
	if !strings.HasPrefix(channel, "private-") && !strings.HasPrefix(channel, "presence-") {
		return fmt.Errorf("velocity/broadcast: client events only allowed on private/presence channels")
	}

	// The sender must be a current subscriber of the channel. Reading the
	// channel membership map under d.mu.RLock pairs with the writes in
	// Subscribe / Unsubscribe.
	d.mu.RLock()
	_, member := d.channels[channel][client.ID]
	d.mu.RUnlock()
	if !member {
		return fmt.Errorf("velocity/broadcast: not a member of %s", channel)
	}

	// Broadcast to channel except sender
	return d.BroadcastExcept([]string{channel}, "client-"+event, data["data"], client.ID)
}

// SetAuthorizer sets the channel authorizer for private/presence channels.
// Without an authorizer, subscriptions to private- and presence- channels are rejected.
func (d *WebSocketDriver) SetAuthorizer(fn ChannelAuthorizer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.authorizer = fn
}

// SetTokenVerifier installs an HMAC token verifier consulted on every
// subscribe to a private- or presence- channel. When non-nil, the inbound
// subscribe message must carry an "auth" field whose value verifies against
// (client.ID, channel). Pass nil to disable.
//
// The parameter is the bare func signature (not the local TokenVerifier
// named type) so *WebSocketDriver structurally satisfies
// broadcast.TokenVerifierSetter and BroadcastManager.SetAuthSecret can
// auto-wire BroadcastManager.VerifyAuthToken here without an explicit
// caller-side cast. See audit H-25 / followup F1.
func (d *WebSocketDriver) SetTokenVerifier(fn func(socketID, channel, token string) bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.verifier = TokenVerifier(fn)
}

// GetServer returns the underlying WebSocket server
func (d *WebSocketDriver) GetServer() *websocket.Server {
	return d.server
}
