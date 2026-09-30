package grpc

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/grpc/internal/callhook"
	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
)

var (
	_ contract.EventDispatcherAware = (*Server)(nil)
	_ contract.ShutdownAware        = (*Server)(nil)
)

// Server wraps a gRPC server with Velocity patterns
type Server struct {
	mu               sync.RWMutex
	port             string
	enableReflection bool

	// own is the server's own work: the goroutines running a Build, a
	// serve loop or a stop, each of which may call user code (a
	// registration, the logger, a listener, the event dispatcher) that
	// calls a stop back. A stop entered from there does not wait on it.
	own drain.Owner

	// cur is the server's current life (see life), nil until the first
	// Build. Guarded by mu.
	cur *life

	// bindNetwork / bindAddress override the default "tcp" + ":"+port listen
	// target so a caller can bind loopback ("tcp","127.0.0.1:50051") or a unix
	// socket ("unix","/run/svc.sock") instead of all interfaces. Empty means the
	// legacy default. providedListener, when non-nil, takes precedence over both
	// and over port: the caller supplies a fully-constructed net.Listener and
	// owns its options. See WithBindAddress and WithListener.
	bindNetwork      string
	bindAddress      string
	providedListener net.Listener
	environment      string

	serverOptions []grpc.ServerOption
	logger        contract.Logger

	// tlsOpted tracks whether the caller supplied transport credentials via
	// WithCreds. Build uses this together with the environment and the
	// GRPC_INSECURE escape hatch to decide whether to refuse a cleartext
	// production start.
	tlsOpted bool

	// Interceptors
	unaryInterceptors  []grpc.UnaryServerInterceptor
	streamInterceptors []grpc.StreamServerInterceptor

	// authConfigured records whether an authentication interceptor has been
	// wired. UseAll sets it when it sees an interceptors pair with IsAuth=true;
	// callers that install auth via the bare Use/UseStream funcs or a custom
	// interceptor signal it explicitly with MarkAuthConfigured. Build reads it
	// to decide whether to warn that the whole service surface is unauthenticated.
	// Guarded by mu.
	authConfigured bool

	// reporter receives the default call lifecycle interceptor's one error report
	// per call: a recovered panic or an internal error. Set via
	// WithReporter.
	reporter contract.Reporter

	// callOptions configure the default call lifecycle interceptor after the
	// server's logger, reporter and event dispatcher. Set via
	// WithCallOptions.
	callOptions []interceptors.CallOption

	// Registration functions to call after server is built
	registrations []RegistrationFunc

	// events holds the optional event dispatcher (SetEventDispatcher)
	// the Server emits events to and applies the failure policy to a
	// failed dispatch (see internal/eventemit). It stores and reads the
	// dispatcher atomically, so framework wiring and event-firing hot
	// paths never race.
	events eventemit.Emitter
}

// ServerOption configures the Server
type ServerOption func(*Server)

// NewServer creates a new gRPC server with the given options. Defaults are
// sourced from environment variables (via LoadConfig) so behaviour matches
// the rest of the framework: GRPC_PORT / GRPC_REFLECTION /
// GRPC_MAX_RECV_SIZE / GRPC_MAX_SEND_SIZE are honoured if set. Explicit
// ServerOption arguments still override the env-derived defaults.
func NewServer(opts ...ServerOption) *Server {
	cfg := LoadConfig()
	s := &Server{
		port:               cfg.ServerPort,
		enableReflection:   cfg.EnableReflection,
		environment:        contract.GetEnv(),
		unaryInterceptors:  make([]grpc.UnaryServerInterceptor, 0),
		streamInterceptors: make([]grpc.StreamServerInterceptor, 0),
		registrations:      make([]RegistrationFunc, 0),
		serverOptions: []grpc.ServerOption{
			grpc.MaxRecvMsgSize(cfg.MaxRecvMsgSize),
			grpc.MaxSendMsgSize(cfg.MaxSendMsgSize),
		},
	}

	for _, opt := range opts {
		opt(s)
	}

	// Without a logger (or with a nil one) the server writes through the
	// framework's standalone fallback logger.
	s.logger = fallbacklog.Resolve(s.logger)
	s.events.UseLogger(func() contract.Logger { return s.logger })

	// Surface any env-parsing diagnostics now that a logger exists, so a
	// non-positive / unparseable / oversize GRPC_MAX_*_SIZE is never silently
	// clamped without the operator knowing.
	for _, w := range cfg.Warnings {
		s.logLine(func(l contract.Logger) { l.Warn(w) })
	}

	return s
}

// WithPort sets the port for the gRPC server
func WithPort(port string) ServerOption {
	return func(s *Server) {
		s.port = port
	}
}

// WithBindAddress overrides where the server listens. By default the server
// binds "tcp" on ":"+port, i.e. all interfaces. Pass ("tcp", "127.0.0.1:50051")
// to bind loopback only, or ("unix", "/run/velvm.sock") for a unix-domain
// socket: the right choice for a control API that must not be reachable off the
// host. When set, it supersedes WithPort for the bind target (WithPort's value
// still labels lifecycle events/logs). WithListener takes precedence over this.
func WithBindAddress(network, address string) ServerOption {
	return func(s *Server) {
		s.bindNetwork = network
		s.bindAddress = address
	}
}

// WithListener makes the server serve on a caller-supplied net.Listener instead
// of dialing net.Listen itself. It is the most general bind hook: the caller
// constructs the listener (unix socket with specific permissions, a loopback
// TCP listener, a test listener, etc.) and the server just serves on it. Takes
// precedence over WithBindAddress and WithPort for the bind target. The server
// closes the listener on Stop/GracefulStop as it would its own.
func WithListener(lis net.Listener) ServerOption {
	return func(s *Server) {
		s.providedListener = lis
	}
}

// WithEnvironment sets the deployment environment (e.g., "production", "staging").
// When set to "production", gRPC reflection is automatically disabled for security.
func WithEnvironment(env string) ServerOption {
	return func(s *Server) {
		s.environment = env
	}
}

// WithReflection enables or disables gRPC reflection
func WithReflection(enabled bool) ServerOption {
	return func(s *Server) {
		s.enableReflection = enabled
	}
}

// WithCreds attaches transport credentials to the gRPC server and marks the
// server as having opted into TLS so the production guard in Build does not
// refuse the start. Pass credentials produced via credentials.NewTLS,
// credentials.NewServerTLSFromFile, or any other source.
func WithCreds(creds credentials.TransportCredentials) ServerOption {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, grpc.Creds(creds))
		s.tlsOpted = true
	}
}

// WithMaxRecvMsgSize sets the maximum receive message size. The value is
// sanitized via clampMsgSize: a non-positive size (which grpc-go would read as
// UNLIMITED, removing the message-size DoS guard) falls back to the 4MB
// default, and an oversize value is clamped to the 1 GiB ceiling.
func WithMaxRecvMsgSize(size int) ServerOption {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, grpc.MaxRecvMsgSize(clampMsgSize(size)))
	}
}

// WithMaxSendMsgSize sets the maximum send message size. Sanitized via
// clampMsgSize on the same floor/ceiling as WithMaxRecvMsgSize.
func WithMaxSendMsgSize(size int) ServerOption {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, grpc.MaxSendMsgSize(clampMsgSize(size)))
	}
}

// WithKeepaliveParams sets the server's keepalive parameters
// (google.golang.org/grpc/keepalive): how long a connection may stay idle
// or open, and how often the server pings an idle client. The values are
// handed to grpc-go as given.
func WithKeepaliveParams(params keepalive.ServerParameters) ServerOption {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, grpc.KeepaliveParams(params))
	}
}

// WithKeepaliveEnforcementPolicy sets the server's keepalive enforcement
// policy (google.golang.org/grpc/keepalive): how often a client may ping,
// and whether it may ping without an active call, before the server
// closes the connection. The values are handed to grpc-go as given.
func WithKeepaliveEnforcementPolicy(policy keepalive.EnforcementPolicy) ServerOption {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, grpc.KeepaliveEnforcementPolicy(policy))
	}
}

// WithCallOptions configures the call lifecycle interceptor Build installs by
// default, applied after the server's logger (WithLogger), reporter
// (WithReporter) and event dispatcher (SetEventDispatcher), so an option
// here wins. A non-nil EventDispatcher set here, by
// interceptors.WithEventDispatcher or a CallOption of the caller's own,
// replaces the server's dispatcher for the call's events, and
// interceptors.WithEventDispatcher(nil) turns them off. The request line is off by default: turn it on
// with interceptors.WithRequestLine().
func WithCallOptions(opts ...interceptors.CallOption) ServerOption {
	return func(s *Server) {
		s.callOptions = append(s.callOptions, opts...)
	}
}

// WithReporter sets where the call lifecycle interceptor Build installs reports a
// call's one error: a recovered panic, or else the error the call ended
// with when it is an internal error (codes.Internal or codes.Unknown, as
// the client gets it), with the method named: pass the app's error handler
// (Services.Errors), so it reaches the Reporter chain. The client gets the
// same status either way. Without it, a recovered panic is logged and a
// handler error is not reported.
func WithReporter(reporter contract.Reporter) ServerOption {
	return func(s *Server) {
		s.reporter = reporter
	}
}

// WithLogger sets the logger for the gRPC server and its default call
// interceptor. Without it, or with nil, the server writes through the
// framework's standalone fallback logger, which writes warnings and errors
// to standard error.
func WithLogger(logger contract.Logger) ServerOption {
	return func(s *Server) {
		s.logger = logger
	}
}

// Use adds unary interceptors to the server. They run in the order they
// are added, between the two ends of the server's call lifecycle
// interceptor, each contained (interceptors.ContainUnary): Use, UseStream
// and UseAll are the only way to add an interceptor, so correlation,
// panic recovery and the call's events and report are outermost for every
// interceptor and handler. grpc-go runs a few things outside any
// interceptor (its own stats handlers, tap handles, codecs and
// compressors), and a hand-written grpc.MethodDesc.Handler that does not
// call the interceptor it is given bypasses the chain for its method.
func (s *Server) Use(interceptors ...grpc.UnaryServerInterceptor) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unaryInterceptors = append(s.unaryInterceptors, interceptors...)
	return s
}

// UseStream adds stream interceptors to the server, in the order they are
// added, each contained (interceptors.ContainStream); see Use.
func (s *Server) UseStream(interceptors ...grpc.StreamServerInterceptor) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamInterceptors = append(s.streamInterceptors, interceptors...)
	return s
}

// InterceptorPair holds both unary and stream interceptor variants.
// It is an alias for interceptors.InterceptorPair so the pairs returned by
// interceptors.Auth and interceptors.CallLifecycle can be passed straight to UseAll.
type InterceptorPair = interceptors.InterceptorPair

// UseAll adds both unary and stream interceptor pairs, as Use and
// UseStream do. This is convenient for interceptors that have both unary
// and stream variants.
func (s *Server) UseAll(pairs ...InterceptorPair) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pair := range pairs {
		s.unaryInterceptors = append(s.unaryInterceptors, pair.Unary)
		s.streamInterceptors = append(s.streamInterceptors, pair.Stream)
		if pair.IsAuth {
			s.authConfigured = true
		}
	}
	return s
}

// MarkAuthConfigured records that authentication has been wired by hand, e.g.
// when the auth interceptor is installed via the bare Use/UseStream funcs
// (s.Use(auth.Unary)) or by a custom interceptor that UseAll cannot tag. It
// suppresses the unauthenticated-surface warning Build emits when no auth
// interceptor is detected. UseAll(interceptors.Auth(...)) marks the server
// automatically and does not need this call.
func (s *Server) MarkAuthConfigured() *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authConfigured = true
	return s
}

// RegisterService registers a service with the server using a registration function.
// The registration function receives the underlying *grpc.Server.
//
// Example:
//
//	server.RegisterService(func(srv interface{}) {
//	    pb.RegisterMyServiceServer(srv.(*grpc.Server), &myService{})
//	})
func (s *Server) RegisterService(regFunc RegistrationFunc) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registrations = append(s.registrations, regFunc)
	return s
}

// Build constructs the gRPC server with all configured options.
// This is called automatically by Start() if not called explicitly.
// Build returns an error if the logger is nil. A nil logger causes silent
// NPEs later (reflection warning, start/stop messages, panic recovery
// interceptor) so we fail fast.
//
// Build also enforces the production TLS guard: when the environment is
// "production" (APP_ENV=production or WithEnvironment("production")) and no
// transport credentials were attached via WithCreds, Build returns an
// error unless GRPC_INSECURE=true opts the deployment out for a
// known-internal mTLS mesh or a sidecar-terminated mesh. Outside
// production, a missing creds configuration only emits a one-shot warning.
//
// Authentication is opt-in. When services are registered but no auth
// interceptor was detected (via UseAll(interceptors.Auth(...)) or an explicit
// MarkAuthConfigured), Build emits a one-shot warning that all RPCs are served
// unauthenticated. It does not force auth: the start is fail-open with
// visibility so the operator can add an auth interceptor.
//
// Build runs application code (the CallOptions, the registration
// functions, the logger and a caller-supplied listener's Addr) without
// holding the server's lock, so that code may call the server's
// accessors; it reads that listener's address once, here, and the server
// reports that address from then on. A Build called while another one is
// constructing the server, concurrently or from that application code,
// or while a stop releases the listener of a server that never served,
// returns ErrBuildInProgress at once; a Build after a completed one
// returns nil. A stop, called from that application code or anywhere
// else, ends a Build in progress: the Build publishes nothing, stops what
// it constructed, closes the listener it bound or adopted and returns
// grpc.ErrServerStopped (google.golang.org/grpc), and a Shutdown waits
// for that close. A later Build constructs the server afresh.
func (s *Server) Build() error {
	var err error
	s.own.Do(func() { err = s.build() })
	return err
}

// build is Build, run as the server's own work.
func (s *Server) build() error {
	c, b, err := s.beginBuild()
	if c == nil {
		return err
	}
	var srv *grpc.Server
	var lis *serveListener
	published := false
	defer func() {
		if !published {
			s.abortBuild(c, srv, lis, b.ownsListener)
		}
	}()

	if b.warnTLS {
		fallbacklog.Write(b.logger, func(l contract.Logger) {
			l.Warn("gRPC server starting without TLS credentials. Configure WithCreds before deploying to production",
				"port", b.port,
			)
		})
	}

	// Create the listener (or adopt a caller-supplied one). No fallible
	// check follows this point: past here Build runs to completion, so the
	// listener and the grpc-go server are published together or not at all.
	raw, err := newListener(b.providedListener, b.bindNetwork, b.bindAddress, b.port)
	if err != nil {
		return err
	}
	lis = newServeListener(raw, func() { s.serving(c) }, s.logLine, "gRPC")

	calls := s.defaultCallLifecycle(b.logger, b.reporter, b.callOptions)
	opts := make([]grpc.ServerOption, 0, len(b.serverOptions)+2)
	opts = append(opts, b.serverOptions...)
	opts = append(opts, chains(c.run, calls, b.unaryInterceptors, b.streamInterceptors)...)

	srv = grpc.NewServer(opts...)
	for _, regFunc := range b.registrations {
		regFunc(srv)
	}
	if b.enableReflection {
		reflection.Register(srv)
	}

	s.mu.Lock()
	if c.run.Stopping() {
		// A stop began while this Build constructed the server: publish
		// nothing, and the deferred abort releases what it made.
		s.mu.Unlock()
		return grpc.ErrServerStopped
	}
	c.srv, c.lis, c.building = srv, lis, false
	s.mu.Unlock()
	c.run.Release()
	published = true

	// Warn (fail-open with visibility) when a service surface is exposed with
	// no authentication interceptor wired. gRPC auth is opt-in: a server that
	// registers services without an auth interceptor serves every RPC
	// unauthenticated, and nothing else surfaces that. We only warn when there
	// is something to protect (at least one registered service) and no auth was
	// detected via UseAll(interceptors.Auth(...)) or MarkAuthConfigured. The
	// warning fires once per Build; Build is idempotent (early-returns when the
	// server is already built) so it never repeats for a given server.
	if len(b.registrations) > 0 && !b.authConfigured {
		fallbacklog.Write(b.logger, func(l contract.Logger) {
			l.Warn("gRPC server is serving all RPCs unauthenticated: no auth interceptor detected. Add one via UseAll(interceptors.Auth(...)), or call MarkAuthConfigured() if you wired auth by hand",
				"services", len(b.registrations),
				"port", b.port,
			)
		})
	}

	// The production hard-fail on reflection already ran in beginBuild, so
	// here reflection is known to be non-production: just warn.
	if b.enableReflection {
		fallbacklog.Write(b.logger, func(l contract.Logger) {
			l.Warn("gRPC reflection is enabled - disable in production (GRPC_REFLECTION=false)")
		})
	}

	return nil
}

// chains returns the server options that install the server's interceptor
// chains (see interceptorChains). It is the only place interceptors reach
// grpc-go, and no ServerOption can carry one, so admission and the call
// lifecycle (correlation, recovery, request line, events and the one error
// report) are the outermost interceptors of every call.
func chains(run *drain.Run, calls interceptors.InterceptorPair, unary []grpc.UnaryServerInterceptor, stream []grpc.StreamServerInterceptor) []grpc.ServerOption {
	u, st := interceptorChains(run, calls, unary, stream)
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(u...), grpc.ChainStreamInterceptor(st...)}
}

// interceptorChains returns the unary and stream chains of a server
// serving in run: the call lifecycle pair calls, first and last, the
// first behind admission into run (see admission), and between them each
// of unary and stream wrapped in interceptors.ContainUnary or
// interceptors.ContainStream, in registration order.
func interceptorChains(run *drain.Run, calls interceptors.InterceptorPair, unary []grpc.UnaryServerInterceptor, stream []grpc.StreamServerInterceptor) ([]grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor) {
	first := admission(run, calls)
	u := make([]grpc.UnaryServerInterceptor, 0, len(unary)+2)
	u = append(u, first.Unary)
	for _, ic := range unary {
		u = append(u, interceptors.ContainUnary(ic))
	}
	u = append(u, calls.Unary)
	st := make([]grpc.StreamServerInterceptor, 0, len(stream)+2)
	st = append(st, first.Stream)
	for _, ic := range stream {
		st = append(st, interceptors.ContainStream(ic))
	}
	st = append(st, calls.Stream)
	return u, st
}

// life is one construction of the server, from the Build that makes it to
// the stop that ends it: the grpc-go server and listener that Build made,
// the run they serve in, and whether and when it served. Every unit of
// its work is admitted into run: the Build, the serve loop and each call
// (see admission). A life a stop ended before it served is discarded, and
// the next Build starts another; a life that served stays the server's
// last, since grpc-go cannot serve a stopped server again. Its fields are
// guarded by the server's mu.
type life struct {
	run *drain.Run

	// building is set while the Build that makes this life runs.
	building bool
	// srv and lis are what the Build published, nil until it has.
	srv *grpc.Server
	lis *serveListener
	// served is set once a Start admitted the serve loop into run.
	served bool
	// running is set, with startTime, at the serve loop's first Accept.
	running   bool
	startTime time.Time
	// discarded is set once a life that never served has released what
	// its Build made: the next Build starts another.
	discarded bool

	// started is closed after the first Accept published the start, and
	// serveDone once Serve returned, with serveErr.
	started   chan struct{}
	serveDone chan struct{}
	serveErr  error
}

// buildPlan is the configuration one Build constructs the server from,
// copied under the lock so the construction runs without it.
type buildPlan struct {
	logger                   contract.Logger
	reporter                 contract.Reporter
	port                     string
	bindNetwork, bindAddress string
	providedListener         net.Listener
	serverOptions            []grpc.ServerOption
	unaryInterceptors        []grpc.UnaryServerInterceptor
	streamInterceptors       []grpc.StreamServerInterceptor
	callOptions              []interceptors.CallOption
	registrations            []RegistrationFunc
	enableReflection         bool
	authConfigured           bool
	warnTLS                  bool

	// ownsListener is set when Build binds the listener itself rather
	// than adopting a caller-supplied one. It is a flag, not a comparison
	// of the two listeners, because comparing interfaces panics for a
	// listener whose dynamic value is not comparable.
	ownsListener bool
}

// beginBuild runs Build's checks under the lock and, when they pass,
// starts a new life with the Build admitted into its run, and returns it
// with the plan to construct from. It returns a nil life with a nil error
// when the server is already built, and with an error when a Build is in
// progress, a stop still releases the listener of a server that never
// served, or a check fails. It calls no application code.
func (s *Server) beginBuild() (*life, *buildPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if c := s.cur; c != nil && !c.discarded {
		if c.building || (!c.served && c.run.Stopping()) {
			return nil, nil, ErrBuildInProgress
		}
		return nil, nil, nil // Already built
	}

	if s.logger == nil {
		return nil, nil, fmt.Errorf("velocity/grpc: logger is required. Build the server with NewServer, which defaults to the standalone fallback logger, or use WithLogger(...)")
	}

	// Enforce the production TLS guard before we start binding sockets so the
	// error is unambiguous when an operator forgets to wire credentials.
	warnTLS := false
	if !s.tlsOpted {
		// final: do not rename. GRPC_INSECURE is the 1.0 surface name for
		// the production TLS opt-out; downstream operators may already key
		// off it.
		//
		// "production", "prod", and "staging" all fold into the locked-down
		// branch via contract.IsProductionEnv so a typo'd APP_ENV cannot
		// silently bypass the TLS requirement.
		insecureOptOut := os.Getenv("GRPC_INSECURE") == "true"
		isProd := contract.IsProductionEnv(s.environment)
		if isProd && !insecureOptOut {
			return nil, nil, fmt.Errorf("velocity/grpc: TLS credentials are required in production. Use WithCreds, or set GRPC_INSECURE=true to opt out for a known-internal mTLS mesh")
		}
		warnTLS = !isProd
	}

	// Reflection-in-production is a hard failure. Validate it BEFORE binding the
	// socket so every fallible check returns before a listener is opened.
	if s.enableReflection && contract.IsProductionEnv(s.environment) {
		return nil, nil, fmt.Errorf("velocity/grpc: reflection must not be enabled in production (set GRPC_REFLECTION=false or build without WithReflection(true))")
	}

	c := &life{
		run:       s.own.NewRun(),
		building:  true,
		started:   make(chan struct{}),
		serveDone: make(chan struct{}),
	}
	c.run.Admit() // a new run admits: the Build is its first unit
	s.cur = c
	return c, &buildPlan{
		logger:             s.logger,
		reporter:           s.reporter,
		port:               s.port,
		bindNetwork:        s.bindNetwork,
		bindAddress:        s.bindAddress,
		providedListener:   s.providedListener,
		ownsListener:       s.providedListener == nil,
		serverOptions:      append([]grpc.ServerOption(nil), s.serverOptions...),
		unaryInterceptors:  append([]grpc.UnaryServerInterceptor(nil), s.unaryInterceptors...),
		streamInterceptors: append([]grpc.StreamServerInterceptor(nil), s.streamInterceptors...),
		callOptions:        append([]interceptors.CallOption(nil), s.callOptions...),
		registrations:      append([]RegistrationFunc(nil), s.registrations...),
		enableReflection:   s.enableReflection,
		authConfigured:     s.authConfigured,
		warnTLS:            warnTLS,
	}, nil
}

// abortBuild ends the Build of c without publishing it (its listener
// failed to bind, its application code panicked, or a stop began during
// it): it stops srv and closes a listener the Build bound itself. A
// caller-supplied listener stays open for the next Build, unless a stop
// began, which closes it as a stop closes the listener of a built server.
//
// The Build holds its unit of c's run until every close it owes has
// ended, so a stop's drain, and a Shutdown awaiting it, waits for them,
// and a Build started from a listener's Close, or racing it, returns
// ErrBuildInProgress instead of adopting a listener about to be closed.
// Whether a stop began is read under the lock at the end, so a stop that
// begins during the owned close is still honoured. The closes are
// contained (see serveListener.Close). When no stop began, the Build
// ends c's run itself.
func (s *Server) abortBuild(c *life, srv *grpc.Server, lis *serveListener, ownsListener bool) {
	if srv != nil {
		srv.Stop()
	}
	if lis != nil && ownsListener {
		_ = lis.Close()
	}
	suppliedClosed := false
	for {
		s.mu.Lock()
		if lis != nil && !ownsListener && !suppliedClosed && c.run.Stopping() {
			s.mu.Unlock()
			suppliedClosed = true
			_ = lis.Close()
			continue
		}
		c.building, c.discarded = false, true
		s.mu.Unlock()
		break
	}
	if lis != nil {
		lis.logClosePanic()
	}
	owner := c.run.Close()
	c.run.Release()
	if owner {
		c.run.Finish(nil)
	}
}

// newListener resolves the bind target set by the options, in precedence order:
// a caller-supplied listener (WithListener) wins; else an explicit
// network+address (WithBindAddress); else the legacy default of "tcp" on
// ":"+port (all interfaces).
func newListener(provided net.Listener, network, address, port string) (net.Listener, error) {
	if provided != nil {
		return provided, nil
	}
	if network == "" {
		network = "tcp"
	}
	if address == "" {
		address = ":" + port
	}
	lis, err := net.Listen(network, address)
	if err != nil {
		return nil, errchain.Errorf("velocity/grpc: failed to listen on %s %s: %w", network, address, err)
	}
	return lis, nil
}

// Start builds (if not already built) and starts the gRPC server.
// This method blocks until the server is stopped. A server a stop has
// ended does not start again: Start returns grpc.ErrServerStopped
// (google.golang.org/grpc), as it does when a stop took the built server
// between Build and serving (from a warning Build writes, say), and
// ErrBuildInProgress while a rebuild runs. A serve loop that fails (the
// listener's Accept returned an error or panicked) stops the server, and
// Start returns that failure.
func (s *Server) Start() error {
	c, err := s.admitServe()
	if err != nil {
		return err
	}
	s.own.Do(func() { err = s.serve(c, false) })
	return err
}

// StartAsync builds and starts the gRPC server on a goroutine of its own,
// and returns once the server is taking connections: grpc-go has
// registered the listener and entered its first Accept, the server
// reports running, the starting line is written and ServerStarted is
// dispatched. From then on any stop closes the listener. When grpc-go
// refuses to serve because a stop came first, StartAsync returns
// grpc.ErrServerStopped (google.golang.org/grpc) and the server never
// reports running. Like Start, it returns grpc.ErrServerStopped for a
// server a stop has ended. A serve loop that fails later stops the
// server. Use Stop, GracefulStop or Shutdown to stop it.
func (s *Server) StartAsync() error {
	c, err := s.admitServe()
	if err != nil {
		return err
	}
	s.own.Go(func() { _ = s.serve(c, true) })
	select {
	case <-c.started:
		return nil
	case <-c.serveDone:
	}
	if drain.Closed(c.started) {
		return nil
	}
	return c.serveErr
}

// admitServe builds the server if it is not built and admits a serve loop
// into the current life's run, returning the life to serve. It refuses a
// server that serves already, one a stop has ended, and one a rebuild is
// constructing.
func (s *Server) admitServe() (*life, error) {
	if err := s.Build(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cur
	switch {
	case c == nil || c.discarded:
		// A stop took the built server after Build returned.
		return nil, grpc.ErrServerStopped
	case c.building:
		return nil, ErrBuildInProgress
	case c.served && !c.run.Stopping():
		return nil, ErrServerAlreadyRunning
	case c.srv == nil || !c.run.Admit():
		// A stop ended this server; grpc-go cannot serve it again.
		return nil, grpc.ErrServerStopped
	}
	c.served = true
	return c, nil
}

// serve runs c's serve loop, as the server's own work, and releases its
// unit of c's run when Serve returns. A loop that failed (its listener's
// Accept returned an error or panicked, or grpc-go panicked) stops the
// server: grpc-go returns from Serve but keeps the connections it
// accepted, and the stop closes them. A Serve that a stop ended returns
// nil or grpc.ErrServerStopped and needs nothing. logFailure writes the
// failure as an error line, for StartAsync, whose caller has returned.
func (s *Server) serve(c *life, logFailure bool) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errchain.Errorf("velocity/grpc: serve loop panicked: %w", panicerr.FromRecovered(p))
		}
		c.serveErr = err
		close(c.serveDone)
		c.run.Release()
		if err == nil || errchain.Is(err, grpc.ErrServerStopped) {
			return
		}
		if logFailure {
			s.logLine(func(l contract.Logger) { l.Error("gRPC server error", "error", err) })
		}
		s.own.Signal(c.run, s.stopWork(c, false))
	}()
	return c.srv.Serve(c.lis)
}

// serving runs at the first Accept of c's serve loop. grpc-go calls it
// only once Serve has registered the listener, so from here on a stop
// closes it: the server is taking connections. It publishes that before
// StartAsync returns: the server reports running, ServerStarted is
// dispatched and the starting line written, on the serve loop's
// goroutine, as the server's own work.
func (s *Server) serving(c *life) {
	defer close(c.started)
	s.mu.Lock()
	c.running = true
	c.startTime = time.Now()
	start, port, addr := c.startTime, s.port, c.lis.text
	s.mu.Unlock()
	s.events.EmitBuilt(context.Background(), func() any {
		return &grpcevents.ServerStarted{
			EventMeta: contract.EventMeta{Context: context.Background(), At: start},
			Port:      port,
		}
	})
	s.logLine(func(l contract.Logger) { l.Info("gRPC server starting", "address", addr) })
}

// Stop stops the gRPC server immediately: it begins the server's stop
// (see Shutdown), stops grpc-go, which closes every connection and so
// cancels the contexts of the calls in flight, and returns once the
// server has stopped accepting, its listener closed. It does not wait for
// the handlers in flight to return: ServerStopped is dispatched, and a
// Shutdown returns, once they have. A server built but never served
// releases its listener the same way, so it does not leak its socket;
// during a Build, Stop ends that Build (see Build) and returns at once.
//
// Stop runs grpc-go's stop as the server's own work, so a caller-supplied
// listener's Close, which grpc-go calls holding its own lock, may call
// the server's accessors and stops. A Stop called from the server's own
// work (a registration, the logger's lines, the event dispatcher's
// ServerStarted or ServerStopped, a caller-supplied listener's Close)
// begins the stop and stops grpc-go on a goroutine of its own, and
// returns without waiting.
func (s *Server) Stop() {
	nested := s.own.Nested()
	c, srv, lis := s.signal(false)
	if srv == nil {
		return
	}
	if nested {
		s.own.Go(srv.Stop)
		return
	}
	s.own.Do(srv.Stop)
	awaitClosed(lis, c.run.Finished())
}

// GracefulStop begins a graceful stop of the gRPC server (see Shutdown)
// and returns once the server has stopped accepting, its listener closed,
// before the drain ends: the calls in flight run to completion on their
// own, and ServerStopped is dispatched once, when they have. Call
// Shutdown(ctx) to wait for the drain, bounded by ctx. So
// `GracefulStop(); db.Close()` closes the database while calls may still
// be running; use Shutdown(ctx) before tearing down what the handlers
// use. It never waits on the handlers, so a handler may call it. A call
// that reaches the server after the stop began is refused with
// codes.Unavailable ("server is stopping") instead of running. Like
// Stop, it releases the listener of a server built but never served, and
// during a Build it ends that Build and returns at once. Called from the
// server's own work (see Stop), it begins the stop and returns without
// waiting.
func (s *Server) GracefulStop() {
	nested := s.own.Nested()
	c, _, lis := s.signal(true)
	if lis != nil && !nested {
		awaitClosed(lis, c.run.Finished())
	}
}

// signal begins the stop of the server's current life, when it has one
// and no stop began it, and returns that life with what its Build
// published: nil when there is no life, or the Build has not published.
func (s *Server) signal(graceful bool) (*life, *grpc.Server, *serveListener) {
	s.mu.RLock()
	c := s.cur
	s.mu.RUnlock()
	if c == nil {
		return nil, nil, nil
	}
	s.own.Signal(c.run, s.stopWork(c, graceful))
	// Read after the signal: a Build that publishes from here on saw the
	// stop, and publishes nothing.
	s.mu.RLock()
	defer s.mu.RUnlock()
	return c, c.srv, c.lis
}

// awaitClosed waits until lis is closed, or finished, the channel of the
// run it serves in, is closed (a finished run closed it).
func awaitClosed(lis *serveListener, finished <-chan struct{}) {
	select {
	case <-lis.closed:
	case <-finished:
	}
}

// stopWork returns the stop of c, which the first stop of c's run runs as
// the server's own work, on a goroutine of its own (drain.Owner.Signal):
//
//  1. the stopping line, for a server that started;
//  2. the transport stop: grpc-go's GracefulStop (graceful) or Stop for a
//     server that served, which closes its listener; for one built but
//     never served, grpc-go's Stop and the close of the listener, which
//     grpc-go never took;
//  3. the wait for c's run to go idle: the Build, the serve loop and
//     every admitted call have returned;
//  4. ServerStopped, for a server that started.
//
// The run finishes when the work returns, so a Shutdown returns nil only
// after all four. A stop that begins during a Build finds nothing
// published: the Build, seeing the stop, releases what it made itself,
// within its unit of the run.
func (s *Server) stopWork(c *life, graceful bool) func() error {
	return func() error {
		s.mu.RLock()
		srv, lis, served, running := c.srv, c.lis, c.served, c.running
		s.mu.RUnlock()
		if running {
			line := "gRPC server stopping"
			if graceful {
				line = "gRPC server gracefully stopping"
			}
			s.logLine(func(l contract.Logger) { l.Info(line) })
		}
		switch {
		case srv == nil:
		case served && graceful:
			srv.GracefulStop()
		case served:
			srv.Stop()
		default:
			srv.Stop()
			_ = lis.Close()
		}
		<-c.run.Idle()
		if lis != nil {
			lis.logClosePanic()
		}
		s.mu.Lock()
		if !c.served {
			c.discarded = true
		}
		start, port := c.startTime, s.port
		s.mu.Unlock()
		if !start.IsZero() {
			s.events.EmitBuilt(context.Background(), func() any {
				now := time.Now()
				return &grpcevents.ServerStopped{
					EventMeta: contract.EventMeta{Context: context.Background(), At: now},
					Port:      port,
					Duration:  now.Sub(start),
				}
			})
		}
		return nil
	}
}

// logLine writes one Start, Build or stop diagnostic through the
// server's logger with fallbacklog.Write: a logger that panics falls back
// and never skips the state change, teardown or event the line precedes.
func (s *Server) logLine(write func(contract.Logger)) {
	fallbacklog.Write(s.logger, write)
}

// Shutdown gracefully stops the server, or joins the stop already under
// way, and waits for it until ctx is done. ctx is its only bound:
// Shutdown(context.Background()) waits as long as the calls take, by the
// caller's choice.
//
// Every stop of the server (Stop, GracefulStop, Shutdown, and a failed
// serve loop) is one stop of the server's current run, which the first
// of them begins: the server stops accepting, a call that reaches it
// after is refused with codes.Unavailable ("server is stopping"), and
// the stop waits for the Build in progress, the serve loop and every call
// admitted before it began. Shutdown returns nil only then, and so does
// every Shutdown, overlapping or later, of the same run. At ctx it
// returns the ctx error and forces the stop, once however many Shutdowns
// time out: grpc-go's Stop closes the connections, which cancels the
// calls' contexts; a handler that ignores its context may still run after
// Shutdown returns, and the stop, its line and ServerStopped go on on
// their own goroutine and end when it returns. Shutdown never runs user
// code on its own goroutine, so a logger, listener or event listener that
// blocks cannot hold it past ctx.
//
// A Shutdown called from the server's own work (a registration, the
// logger's lines, the event dispatcher's ServerStarted or ServerStopped,
// a caller-supplied listener's Close) cannot wait on the work it runs in:
// it begins the stop and returns at once an error wrapping
// contract.ErrStopFromOwnWork and grpc.ErrServerStopped
// (google.golang.org/grpc), or nil when the stop has finished.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.RLock()
	c := s.cur
	s.mu.RUnlock()
	if c == nil {
		return nil
	}
	work := s.stopWork(c, true)
	if s.own.Nested() {
		s.own.Signal(c.run, work)
		err := s.own.Stop(ctx, c.run, nil, nil)
		if errchain.Is(err, contract.ErrStopFromOwnWork) {
			err = errchain.Errorf("velocity/grpc: %w: %w", err, grpc.ErrServerStopped)
		}
		return err
	}
	return s.own.Stop(ctx, c.run, work, func() {
		s.mu.RLock()
		srv := c.srv
		s.mu.RUnlock()
		if srv != nil {
			srv.Stop()
		}
	})
}

// SetEventDispatcher wires an event dispatcher into the Server. Safe to
// call before or after Start/StartAsync; mutex-protected so framework
// bootstrap can re-wire the dispatcher without racing the request path.
//
// Passing a nil fn clears the dispatcher and reverts the Server to a
// no-op emission state.
func (s *Server) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	s.events.Set(fn)
}

// defaultCallLifecycle builds the call lifecycle interceptor Build installs
// by default: it logs through logger, reports to reporter and dispatches
// its events through the Server's own emitter, so it builds an event only
// while a dispatcher is installed (SetEventDispatcher, before or after
// Build) and a failed dispatch meets the Server's one failure policy;
// callOptions apply last, so one of them wins.
func (s *Server) defaultCallLifecycle(logger contract.Logger, reporter contract.Reporter, callOptions []interceptors.CallOption) interceptors.InterceptorPair {
	opts := append([]interceptors.CallOption{
		interceptors.WithLogger(logger),
		callhook.WithEmitter(&s.events).(interceptors.CallOption),
		interceptors.WithReporter(reporter),
	}, callOptions...)
	return interceptors.CallLifecycle(opts...)
}

// publishedLocked returns the current life when its Build published it and a
// stop has not taken it before it served, nil otherwise. Caller holds
// s.mu.
func (s *Server) publishedLocked() *life {
	c := s.cur
	if c == nil || c.srv == nil || (!c.served && c.run.Stopping()) {
		return nil
	}
	return c
}

// Address returns the address the server is listening on, as the
// listener reported it when Build bound or adopted it.
// Returns empty string if server hasn't been built yet.
func (s *Server) Address() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c := s.publishedLocked(); c != nil {
		return c.lis.text
	}
	return ""
}

// Port returns the configured port
func (s *Server) Port() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.port
}

// IsRunning reports whether the server is taking connections: it has
// entered its first Accept and no stop has begun.
func (s *Server) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cur
	return c != nil && c.running && !c.run.Stopping()
}

// GRPCServer returns the underlying *grpc.Server.
// Returns nil if the server hasn't been built yet.
func (s *Server) GRPCServer() *grpc.Server {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c := s.publishedLocked(); c != nil {
		return c.srv
	}
	return nil
}
