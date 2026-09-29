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
	"google.golang.org/grpc/reflection"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/grpc/internal/callhook"
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
	grpcServer       *grpc.Server
	listener         net.Listener
	port             string
	enableReflection bool

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
	running          bool

	// served records that the listener was handed to grpc-go's Serve (Start or
	// StartAsync ran). Once true, the serve goroutine owns the listener and may
	// read it without the lock, so Stop/GracefulStop must never write s.listener.
	// It stays true across a stop so a second Stop cannot mistake a just-stopped
	// server for a built-but-never-served one and race that read. Distinct from
	// running, which toggles off on stop.
	served bool

	// drained is made by the stop that ends a running server, the one that
	// owns its drain, and closed when grpc-go's stop for that server
	// returns. A GracefulStop or Shutdown that overlaps waits on it, so it
	// never reports success before the drain it overlaps has finished.
	// Guarded by mu; nil until a stop ended a running server.
	drained chan struct{}

	// build is the Build constructing the server outside the lock, nil
	// when none is. A concurrent or re-entrant Build returns
	// ErrBuildInProgress instead of constructing a second one, and a stop
	// marks it stopped so it publishes nothing. Guarded by mu.
	build *buildPlan

	serverOptions []grpc.ServerOption
	logger        contract.Logger

	// startTime records when the server last started serving; zero when the
	// server has not started or has already emitted its ServerStopped event.
	// Guarded by mu like the running flag. The zero check is what keeps a
	// stop-without-start silent and prevents Shutdown (which delegates to
	// GracefulStop) from double-emitting ServerStopped.
	startTime time.Time

	// tlsOpted tracks whether the caller supplied transport credentials via
	// WithCreds or WithServerOption(grpc.Creds(...)). Build uses this together
	// with the environment and the GRPC_INSECURE escape hatch to decide
	// whether to refuse a cleartext production start.
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

	// disableDefaultCallLifecycle suppresses the call lifecycle interceptor
	// (interceptors.CallLifecycle) that Build installs at both ends of the chain by
	// default. grpc-go does NOT auto-recover interceptor/handler panics, so
	// without it the first panic crashes the serve loop; the default keeps a
	// server alive out of the box. Set via WithoutDefaultCallLifecycle
	// for callers that install their own.
	disableDefaultCallLifecycle bool

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
		s.logger.Warn(w)
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

// WithServerOption adds a grpc.ServerOption to the server.
//
// If the option carries transport credentials (e.g., grpc.Creds(...)), the
// production TLS guard in Build cannot detect that fact: grpc.ServerOption is
// an opaque interface whose concrete type lives behind unexported wrappers in
// google.golang.org/grpc. Callers that route credentials through this hook
// must also call WithExplicitTLS() so the guard recognises the opt-in.
// Prefer WithCreds for new code; it both attaches the credentials and marks
// the server as TLS-configured in a single step.
func WithServerOption(opt grpc.ServerOption) ServerOption {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, opt)
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

// WithExplicitTLS marks the server as having opted into TLS without attaching
// any credentials itself. It is the escape hatch for callers that route
// credentials via WithServerOption(grpc.Creds(...)) or any other path the
// production guard cannot inspect (e.g., a custom grpc.ServerOption wrapper).
// Without this option, the production guard in Build refuses to start a
// server whose TLS configuration it cannot see, even when the caller has
// configured TLS correctly.
func WithExplicitTLS() ServerOption {
	return func(s *Server) {
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

// WithoutDefaultCallLifecycle disables the call lifecycle interceptor
// (interceptors.CallLifecycle) that Build installs by default at both ends of the
// chain. Use it only when you install interceptors.CallLifecycle yourself, first
// and last in the chain, with every interceptor between them wrapped in
// interceptors.ContainUnary or interceptors.ContainStream (the server
// then installs Use, UseStream and UseAll interceptors as given, without
// wrapping them); otherwise the calls are not correlated, observed or
// reported, and an interceptor/handler panic crashes the gRPC serve loop
// (grpc-go does not auto-recover).
func WithoutDefaultCallLifecycle() ServerOption {
	return func(s *Server) {
		s.disableDefaultCallLifecycle = true
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

// Use adds unary interceptors to the server.
// Interceptors are executed in the order they are added.
func (s *Server) Use(interceptors ...grpc.UnaryServerInterceptor) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unaryInterceptors = append(s.unaryInterceptors, interceptors...)
	return s
}

// UseStream adds stream interceptors to the server.
// Interceptors are executed in the order they are added.
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

// UseAll adds both unary and stream interceptor pairs.
// This is convenient for interceptors that have both unary and stream variants.
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
// transport credentials were attached via WithCreds (or signalled via
// WithExplicitTLS for legacy WithServerOption(grpc.Creds(...)) callers),
// Build returns an error unless GRPC_INSECURE=true opts the deployment out
// for a known-internal mTLS mesh or a sidecar-terminated mesh. Outside
// production, a missing creds configuration only emits a one-shot warning.
//
// Authentication is opt-in. When services are registered but no auth
// interceptor was detected (via UseAll(interceptors.Auth(...)) or an explicit
// MarkAuthConfigured), Build emits a one-shot warning that all RPCs are served
// unauthenticated. It does not force auth: the start is fail-open with
// visibility so the operator can add an auth interceptor.
//
// Build runs application code (the CallOptions, the registration
// functions and the logger) without holding the server's lock, so that
// code may call the server's accessors. A Build called while another one
// is constructing the server, concurrently or from that application code,
// returns ErrBuildInProgress at once; a Build after a completed one
// returns nil. A Build that a Stop, GracefulStop or Shutdown ran during
// publishes nothing, closes the listener it bound or adopted and returns
// grpc.ErrServerStopped (google.golang.org/grpc); a later Build
// constructs the server afresh.
func (s *Server) Build() error {
	b, err := s.beginBuild()
	if b == nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			s.abortBuild(b)
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
	// listener and grpcServer are published together or not at all.
	lis, err := newListener(b.providedListener, b.bindNetwork, b.bindAddress, b.port)
	if err != nil {
		return err
	}
	b.listener = lis

	// Build server options with interceptor chains. The call lifecycle interceptor
	// (interceptors.CallLifecycle) runs at both ends by default. The first
	// occurrence owns the call: it correlates it, and when the call ends,
	// however it ends (a handler or interceptor panic included), it writes
	// the request line (when enabled), dispatches the terminal events and
	// makes the one error report, all under the call's one span and
	// request id. The last occurrence contains a handler panic on the
	// goroutine that runs the handler, which an interceptor may have
	// started, and each user interceptor is wrapped in ContainUnary or
	// ContainStream, which contains its panic on whatever goroutine runs
	// it. grpc-go does not auto-recover interceptor panics.
	opts := make([]grpc.ServerOption, 0, len(b.serverOptions)+2)
	opts = append(opts, b.serverOptions...)

	var unary []grpc.UnaryServerInterceptor
	var stream []grpc.StreamServerInterceptor
	if b.disableDefaultCallLifecycle {
		unary = append(unary, b.unaryInterceptors...)
		stream = append(stream, b.streamInterceptors...)
	} else {
		calls := s.defaultCallLifecycle(b.logger, b.reporter, b.callOptions)
		unary = append(unary, calls.Unary)
		for _, ic := range b.unaryInterceptors {
			unary = append(unary, interceptors.ContainUnary(ic))
		}
		unary = append(unary, calls.Unary)
		stream = append(stream, calls.Stream)
		for _, ic := range b.streamInterceptors {
			stream = append(stream, interceptors.ContainStream(ic))
		}
		stream = append(stream, calls.Stream)
	}

	if len(unary) > 0 {
		opts = append(opts, grpc.ChainUnaryInterceptor(unary...))
	}
	if len(stream) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(stream...))
	}

	srv := grpc.NewServer(opts...)
	for _, regFunc := range b.registrations {
		regFunc(srv)
	}
	if b.enableReflection {
		reflection.Register(srv)
	}

	s.mu.Lock()
	if b.stopped {
		// A stop ran while this Build constructed the server: publish
		// nothing, and the deferred abort releases the listener.
		s.mu.Unlock()
		srv.Stop()
		return grpc.ErrServerStopped
	}
	s.grpcServer = srv
	s.listener = lis
	s.build = nil
	s.mu.Unlock()
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

// buildPlan is the configuration one Build constructs the server from,
// copied under the lock so the construction runs without it.
type buildPlan struct {
	logger                      contract.Logger
	reporter                    contract.Reporter
	port                        string
	bindNetwork, bindAddress    string
	providedListener            net.Listener
	serverOptions               []grpc.ServerOption
	unaryInterceptors           []grpc.UnaryServerInterceptor
	streamInterceptors          []grpc.StreamServerInterceptor
	callOptions                 []interceptors.CallOption
	registrations               []RegistrationFunc
	disableDefaultCallLifecycle bool
	enableReflection            bool
	authConfigured              bool
	warnTLS                     bool

	// listener is the listener this Build bound or adopted, once it has.
	listener net.Listener
	// ownsListener is set when Build binds the listener itself rather
	// than adopting a caller-supplied one. It is a flag, not a comparison
	// of the two listeners, because comparing interfaces panics for a
	// listener whose dynamic value is not comparable.
	ownsListener bool
	// stopped is set by a stop that ran while this Build was in progress.
	// Guarded by the server's mu.
	stopped bool
}

// beginBuild runs Build's checks under the lock and, when they pass,
// marks a Build in progress and returns the plan to construct from. It
// returns a nil plan with a nil error when the server is already built,
// and with an error when a Build is in progress or a check fails. It
// calls no application code.
func (s *Server) beginBuild() (*buildPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.grpcServer != nil {
		return nil, nil // Already built
	}
	if s.build != nil {
		return nil, ErrBuildInProgress
	}

	if s.logger == nil {
		return nil, fmt.Errorf("velocity/grpc: logger is required. Build the server with NewServer, which defaults to the standalone fallback logger, or use WithLogger(...)")
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
			return nil, fmt.Errorf("velocity/grpc: TLS credentials are required in production. Use WithCreds, or call WithExplicitTLS if you supplied credentials via WithServerOption(grpc.Creds(...)). Set GRPC_INSECURE=true to opt out for a known-internal mTLS mesh")
		}
		warnTLS = !isProd
	}

	// Reflection-in-production is a hard failure. Validate it BEFORE binding the
	// socket so every fallible check returns before a listener is opened.
	if s.enableReflection && contract.IsProductionEnv(s.environment) {
		return nil, fmt.Errorf("velocity/grpc: reflection must not be enabled in production (set GRPC_REFLECTION=false or build without WithReflection(true))")
	}

	s.build = &buildPlan{
		logger:                      s.logger,
		reporter:                    s.reporter,
		port:                        s.port,
		bindNetwork:                 s.bindNetwork,
		bindAddress:                 s.bindAddress,
		providedListener:            s.providedListener,
		ownsListener:                s.providedListener == nil,
		serverOptions:               append([]grpc.ServerOption(nil), s.serverOptions...),
		unaryInterceptors:           append([]grpc.UnaryServerInterceptor(nil), s.unaryInterceptors...),
		streamInterceptors:          append([]grpc.StreamServerInterceptor(nil), s.streamInterceptors...),
		callOptions:                 append([]interceptors.CallOption(nil), s.callOptions...),
		registrations:               append([]RegistrationFunc(nil), s.registrations...),
		disableDefaultCallLifecycle: s.disableDefaultCallLifecycle,
		enableReflection:            s.enableReflection,
		authConfigured:              s.authConfigured,
		warnTLS:                     warnTLS,
	}
	return s.build, nil
}

// abortBuild ends the Build b without publishing a server (its listener
// failed to bind, its application code panicked, or a stop ran during
// it): it closes a listener b bound itself, and clears the Build in
// progress so a later Build can run. A caller-supplied listener stays
// open for the next Build, unless a stop ran during b, which closes it as
// a stop closes the listener of a built server. The clear is deferred so
// it runs whatever the close does, after the close, so a retried Build
// never finds the port still bound.
func (s *Server) abortBuild(b *buildPlan) {
	stopped := false
	defer func() {
		if stopped && b.listener != nil && !b.ownsListener {
			s.closeListener(b.listener)
		}
	}()
	defer func() {
		s.mu.Lock()
		if s.build == b {
			s.build = nil
		}
		stopped = b.stopped
		s.mu.Unlock()
	}()
	if b.listener != nil && b.ownsListener {
		s.closeListener(b.listener)
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
		return nil, fmt.Errorf("velocity/grpc: failed to listen on %s %s: %w", network, address, err)
	}
	return lis, nil
}

// Start builds (if not already built) and starts the gRPC server.
// This method blocks until the server is stopped.
func (s *Server) Start() error {
	if err := s.Build(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrServerAlreadyRunning
	}
	s.running = true
	s.served = true
	s.startTime = time.Now()
	started := s.serverStartedLocked()
	s.mu.Unlock()

	if started != nil {
		s.dispatchEvent(context.Background(), started)
	}
	s.logStarting()
	return s.grpcServer.Serve(s.listener)
}

// StartAsync builds and starts the gRPC server in a goroutine.
// Returns immediately. Use Stop() or GracefulStop() to stop the server.
func (s *Server) StartAsync() error {
	if err := s.Build(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrServerAlreadyRunning
	}
	s.running = true
	s.served = true
	s.startTime = time.Now()
	started := s.serverStartedLocked()
	s.mu.Unlock()

	// Run through async.GoWithRecover so the recover path flows through
	// the canonical async package while still resetting s.running so the
	// server can be restarted after a crash.
	async.GoWithRecover(func() {
		if started != nil {
			s.dispatchEvent(context.Background(), started)
		}
		s.logStarting()
		if err := s.grpcServer.Serve(s.listener); err != nil {
			s.logLine(func(l contract.Logger) { l.Error("gRPC server error", "error", err) })
		}
	}, func(r any) {
		s.logLine(func(l contract.Logger) { l.Error("gRPC server panic recovered", "error", panicerr.FromRecovered(r)) })
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	})

	return nil
}

// Stop stops the gRPC server immediately. It also releases a listener that was
// bound by Build but never served (Build succeeded, Start was never called, or
// the caller abandoned the server), so a built-but-unstarted server does not
// leak its socket. During a GracefulStop it closes the connections the drain
// still holds, which cancels their calls' contexts; once the drain waits
// only on handlers, grpc-go holds Stop until they return.
//
// Stop changes the server's state under its lock and then stops grpc-go,
// closes the listener and logs without it, so a call still in flight, a
// caller-supplied listener or the logger may call the server's accessors.
func (s *Server) Stop() {
	st := s.beginStop(true)
	if st.log {
		s.logLine(func(l contract.Logger) { l.Info("gRPC server stopping") })
	}
	if st.owner {
		s.stopTransport(st, (*grpc.Server).Stop)
	} else if st.srv != nil {
		st.srv.Stop()
	}
	s.endStop(st)
}

// GracefulStop gracefully stops the gRPC server: it waits for the calls in
// flight to finish. A GracefulStop that overlaps another graceful stop or
// a Shutdown waits for that drain to finish. Like Stop, it also releases
// a listener bound by Build but never served, so a built-but-unstarted
// server does not leak its socket. The wait runs without the server's
// lock, so those calls may call the server's accessors, and a Stop may
// interrupt it.
func (s *Server) GracefulStop() {
	st := s.beginStop(false)
	if st.log {
		s.logLine(func(l contract.Logger) { l.Info("gRPC server gracefully stopping") })
	}
	switch {
	case st.owner:
		s.stopTransport(st, (*grpc.Server).GracefulStop)
	case st.drained != nil:
		<-st.drained
	}
	s.endStop(st)
}

// stopPlan is what one Stop or GracefulStop does after it released the
// lock.
type stopPlan struct {
	// srv is the grpc-go server to stop, or for a stop that overlaps the
	// owner's drain, to force at a deadline; nil when there is none.
	srv *grpc.Server
	// owner is set when this stop ended a running server: it runs grpc-go's
	// stop and closes drained when that returns.
	owner bool
	// drained is closed when the owning stop's grpc-go stop returned; nil
	// when no stop ended a running server.
	drained chan struct{}
	// log is set when this stop ended a running server.
	log bool
	// start is when the server this stop ended started, zero when it
	// emits no ServerStopped event.
	start time.Time
	// unserved is the listener of a built but never served server, to
	// close.
	unserved net.Listener
	// port labels the ServerStopped event.
	port string
}

// beginStop records a stop under the lock and returns what to do after
// it. A running server stops running, and this stop owns its drain; force
// (Stop) stops a server that was served even when it no longer runs, so
// it reaches a GracefulStop in progress (grpc-go accepts Stop during
// GracefulStop, and a repeated Stop is a no-op), and a graceful stop that
// overlaps the owner's waits on its drain. A built but never served
// server gives up its listener, and grpcServer is reset so it never
// outlives that listener, or a later Build() early-returns and Start()
// panics on a nil listener.
// It calls no application code.
func (s *Server) beginStop(force bool) stopPlan {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := stopPlan{port: s.port}
	if s.build != nil {
		// A Build in progress publishes nothing once it sees this, and
		// releases its listener itself.
		s.build.stopped = true
	}
	switch {
	case s.grpcServer != nil && s.running:
		// grpc-go closes the serving listener. Do NOT touch s.listener here: the
		// StartAsync serve goroutine reads it without the lock, so writing it
		// would race that read.
		st.srv, st.log, st.owner = s.grpcServer, true, true
		s.running = false
		st.start = s.startTime
		s.startTime = time.Time{}
		s.drained = make(chan struct{})
		st.drained = s.drained
	case s.grpcServer != nil && s.served:
		// A stop already ended this server: force reaches its drain, and a
		// graceful stop waits on it.
		st.srv, st.drained = s.grpcServer, s.drained
		if !force && st.drained == nil {
			st.srv = nil
		}
	case !s.served && s.listener != nil:
		// Built but never served (Start/StartAsync never ran): grpc-go never took
		// ownership of this listener, so the bound socket leaks until exit unless
		// closed here. Gated on !served, not merely !running, so a second Stop
		// after a running server stopped does NOT enter here and race the serve
		// goroutine's unlocked read of s.listener.
		st.unserved = s.listener
		s.listener = nil
		s.grpcServer = nil
	}
	return st
}

// stopTransport runs the owning stop's grpc-go stop and then marks the
// drain finished for every stop that overlaps it.
func (s *Server) stopTransport(st stopPlan, stop func(*grpc.Server)) {
	defer close(st.drained)
	stop(st.srv)
}

// endStop finishes the stop st after grpc-go stopped: it closes an unserved
// listener and dispatches ServerStopped for a stop that ended a running
// server, with its uptime, once (startTime was cleared under the lock, so
// Shutdown delegating to GracefulStop, or its deadline falling back to
// Stop, emits no second event).
func (s *Server) endStop(st stopPlan) {
	if st.unserved != nil {
		s.closeListener(st.unserved)
	}
	if st.start.IsZero() || !s.events.Installed() {
		return
	}
	now := time.Now()
	s.dispatchEvent(context.Background(), &grpcevents.ServerStopped{
		EventMeta: contract.EventMeta{Context: context.Background(), At: now},
		Port:      st.port,
		Duration:  now.Sub(st.start),
	})
}

// logLine writes one Start, Build or stop diagnostic through the
// server's logger with fallbacklog.Write: a logger that panics falls back
// and never skips the state change, teardown or event the line precedes.
func (s *Server) logLine(write func(contract.Logger)) {
	fallbacklog.Write(s.logger, write)
}

// logStarting writes the starting line with the address being served.
// The address comes from the listener, which may be the caller's, so it
// is read inside the contained write.
func (s *Server) logStarting() {
	s.logLine(func(l contract.Logger) { l.Info("gRPC server starting", "address", s.listener.Addr().String()) })
}

// closeListener closes lis, which may be the caller's listener: a panic in
// its Close is contained and logged, so the stop or aborted Build that
// closes it still finishes.
func (s *Server) closeListener(lis net.Listener) {
	defer func() {
		if p := recover(); p != nil {
			s.logLine(func(l contract.Logger) { l.Error("gRPC listener close panicked", "error", panicerr.FromRecovered(p)) })
		}
	}()
	_ = lis.Close()
}

// serverStartedLocked builds the ServerStarted event for the start just
// recorded, or returns nil when no event dispatcher is installed. Caller
// must hold s.mu.
func (s *Server) serverStartedLocked() *grpcevents.ServerStarted {
	if !s.events.Installed() {
		return nil
	}
	return &grpcevents.ServerStarted{
		EventMeta: contract.EventMeta{Context: context.Background(), At: s.startTime},
		Port:      s.port,
	}
}

// Shutdown gracefully stops the server, waiting for the calls in flight
// until ctx is done. At the deadline it returns the ctx error and forces
// the stop; a handler that ignores its context may still run after
// Shutdown returns. A Shutdown that overlaps a stop already draining the
// server waits for that drain the same way, so a nil return always means
// the calls in flight have finished.
//
// Shutdown records the stop, logs it and dispatches ServerStopped itself,
// once, before it returns, whichever stop ends the server. The goroutines
// it leaves behind at the deadline (the graceful drain and the forced
// stop, which grpc-go holds behind a drain waiting on handlers) touch only
// the grpc-go server, so nothing the caller tears down next is used after
// Shutdown returns.
func (s *Server) Shutdown(ctx context.Context) error {
	st := s.beginStop(false)
	if st.log {
		s.logLine(func(l contract.Logger) { l.Info("gRPC server gracefully stopping") })
	}
	if st.owner {
		async.Go(func() { s.stopTransport(st, (*grpc.Server).GracefulStop) })
	}
	var err error
	if st.drained != nil {
		select {
		case <-st.drained:
		case <-ctx.Done():
			select {
			case <-st.drained:
			default:
				async.Go(st.srv.Stop)
				err = ctx.Err()
			}
		}
	}
	s.endStop(st)
	return err
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

// dispatchEvent fires an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped
// values. A failed dispatch, an error or a panic, goes to the failure
// policy (counted, its event's first failure logged through the Server's
// logger) and never reaches the caller: the gRPC request path must never
// fail because of an event sink.
func (s *Server) dispatchEvent(ctx context.Context, evt any) {
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if p := recover(); p != nil {
			s.events.Fail(ctx, panicerr.FromRecovered(p), evt)
		}
	}()
	s.events.Emit(ctx, evt)
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

// Address returns the address the server is listening on.
// Returns empty string if server hasn't been built yet.
func (s *Server) Address() string {
	s.mu.RLock()
	lis := s.listener
	s.mu.RUnlock()

	// A caller-supplied listener's Addr runs without the lock.
	if lis != nil {
		return lis.Addr().String()
	}
	return ""
}

// Port returns the configured port
func (s *Server) Port() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.port
}

// IsRunning returns true if the server is currently running
func (s *Server) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// GRPCServer returns the underlying *grpc.Server.
// Returns nil if the server hasn't been built yet.
func (s *Server) GRPCServer() *grpc.Server {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.grpcServer
}
