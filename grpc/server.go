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
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/log"
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
	environment      string
	running          bool
	serverOptions    []grpc.ServerOption
	logger           log.Logger

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

	// disableDefaultRecovery suppresses the panic-recovery interceptor that
	// Build installs outermost by default. grpc-go does NOT auto-recover
	// interceptor/handler panics, so without this the first panic crashes the
	// serve loop; the default keeps a server alive out of the box. Set via
	// WithoutDefaultRecovery for callers that wire their own outermost recovery.
	disableDefaultRecovery bool

	// Registration functions to call after server is built
	registrations []RegistrationFunc

	// eventDispatcher is optional; when set via SetEventDispatcher, the
	// Server emits events to the framework dispatcher. Guarded by eventMu
	// so framework wiring and event-firing hot paths never race.
	eventMu         sync.RWMutex
	eventDispatcher func(ctx context.Context, event any) error
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

	// Default to console logger if none provided
	if s.logger == nil {
		s.logger, _ = log.NewLogger(log.LogConfig{Driver: "console"})
	}

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

// WithoutDefaultRecovery disables the panic-recovery interceptor that Build
// installs outermost by default. Use it only when you wire your own recovery
// interceptor first in the chain; otherwise an interceptor/handler panic
// crashes the gRPC serve loop (grpc-go does not auto-recover).
func WithoutDefaultRecovery() ServerOption {
	return func(s *Server) {
		s.disableDefaultRecovery = true
	}
}

// WithLogger sets the logger for the gRPC server
func WithLogger(logger log.Logger) ServerOption {
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
// Many interceptors need both variants, so this groups them together.
type InterceptorPair struct {
	Unary  grpc.UnaryServerInterceptor
	Stream grpc.StreamServerInterceptor
}

// UseAll adds both unary and stream interceptor pairs.
// This is convenient for interceptors that have both unary and stream variants.
func (s *Server) UseAll(pairs ...InterceptorPair) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pair := range pairs {
		s.unaryInterceptors = append(s.unaryInterceptors, pair.Unary)
		s.streamInterceptors = append(s.streamInterceptors, pair.Stream)
	}
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
func (s *Server) Build() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.grpcServer != nil {
		return nil // Already built
	}

	if s.logger == nil {
		return fmt.Errorf("velocity/grpc: logger is required. Use WithLogger(...) or accept the default console logger")
	}

	// Enforce the production TLS guard before we start binding sockets so the
	// error is unambiguous when an operator forgets to wire credentials.
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
			return fmt.Errorf("velocity/grpc: TLS credentials are required in production. Use WithCreds, or call WithExplicitTLS if you supplied credentials via WithServerOption(grpc.Creds(...)). Set GRPC_INSECURE=true to opt out for a known-internal mTLS mesh")
		}
		if !isProd {
			s.logger.Warn("gRPC server starting without TLS credentials. Configure WithCreds before deploying to production",
				"port", s.port,
			)
		}
	}

	// Create listener
	lis, err := net.Listen("tcp", ":"+s.port)
	if err != nil {
		return fmt.Errorf("velocity/grpc: failed to listen on port %s: %w", s.port, err)
	}
	s.listener = lis

	// Build server options with interceptor chains. The panic-recovery
	// interceptor is prepended OUTERMOST by default so a panic in any other
	// interceptor (e.g. logging's user-agent handling) or in a handler is
	// converted to codes.Internal instead of crashing the serve loop: grpc-go
	// does not auto-recover interceptor panics. Local slices keep s.* fields
	// unmutated so a second Build (or inspection) sees the configured set.
	opts := make([]grpc.ServerOption, 0, len(s.serverOptions)+2)
	opts = append(opts, s.serverOptions...)

	unary := s.unaryInterceptors
	stream := s.streamInterceptors
	if !s.disableDefaultRecovery {
		rec := interceptors.Recovery(
			interceptors.WithRecoveryLogger(s.logger),
			interceptors.WithRecoveryEventDispatcher(s.eventDispatchFunc()),
		)
		unary = append([]grpc.UnaryServerInterceptor{rec.Unary}, unary...)
		stream = append([]grpc.StreamServerInterceptor{rec.Stream}, stream...)
	}

	if len(unary) > 0 {
		opts = append(opts, grpc.ChainUnaryInterceptor(unary...))
	}
	if len(stream) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(stream...))
	}

	// Create server
	s.grpcServer = grpc.NewServer(opts...)

	// Register all services
	for _, regFunc := range s.registrations {
		regFunc(s.grpcServer)
	}

	// Enable reflection if configured. Hard-fail in production; silently
	// downgrading to "reflection disabled" lets misconfigured deployments ship
	// with a false sense of security (operators think reflection is on).
	if s.enableReflection {
		// Routed through contract.IsProductionEnv so "prod" and "staging"
		// are both refused alongside "production": reflection exposes the
		// full service surface and a typo'd APP_ENV must not silently
		// re-enable it.
		if contract.IsProductionEnv(s.environment) {
			return fmt.Errorf("velocity/grpc: reflection must not be enabled in production (set GRPC_REFLECTION=false or build without WithReflection(true))")
		}
		s.logger.Warn("gRPC reflection is enabled — disable in production (GRPC_REFLECTION=false)")
		reflection.Register(s.grpcServer)
	}

	return nil
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
	s.startTime = time.Now()
	started := &grpcevents.ServerStarted{Port: s.port, StartTime: s.startTime}
	s.mu.Unlock()

	s.dispatchEvent(context.Background(), started)
	s.logger.Info("gRPC server starting", "address", s.listener.Addr().String())
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
	s.startTime = time.Now()
	started := &grpcevents.ServerStarted{Port: s.port, StartTime: s.startTime}
	s.mu.Unlock()

	// Run through async.GoWithRecover so the recover path flows through
	// the canonical async package while still resetting s.running so the
	// server can be restarted after a crash.
	async.GoWithRecover(func() {
		s.dispatchEvent(context.Background(), started)
		s.logger.Info("gRPC server starting", "address", s.listener.Addr().String())
		if err := s.grpcServer.Serve(s.listener); err != nil {
			s.logger.Error("gRPC server error", "error", err)
		}
	}, func(r any) {
		s.logger.Error("gRPC server panic recovered", "error", panicerr.FromRecovered(r))
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	})

	return nil
}

// Stop stops the gRPC server immediately
func (s *Server) Stop() {
	s.mu.Lock()
	var stopped *grpcevents.ServerStopped
	if s.grpcServer != nil && s.running {
		s.logger.Info("gRPC server stopping")
		s.grpcServer.Stop()
		s.running = false
		stopped = s.stoppedEventLocked()
	}
	s.mu.Unlock()

	if stopped != nil {
		s.dispatchEvent(context.Background(), stopped)
	}
}

// GracefulStop gracefully stops the gRPC server
func (s *Server) GracefulStop() {
	s.mu.Lock()
	var stopped *grpcevents.ServerStopped
	if s.grpcServer != nil && s.running {
		s.logger.Info("gRPC server gracefully stopping")
		s.grpcServer.GracefulStop()
		s.running = false
		stopped = s.stoppedEventLocked()
	}
	s.mu.Unlock()

	if stopped != nil {
		s.dispatchEvent(context.Background(), stopped)
	}
}

// stoppedEventLocked builds the ServerStopped event for the current uptime
// and clears startTime so a subsequent stop path (e.g. Shutdown delegating
// to GracefulStop, or the Shutdown timeout falling back to Stop) emits
// nothing. Returns nil when no start was recorded. Caller must hold s.mu;
// the event is dispatched after the lock is released so a listener that
// calls back into the Server cannot deadlock.
func (s *Server) stoppedEventLocked() *grpcevents.ServerStopped {
	if s.startTime.IsZero() {
		return nil
	}
	now := time.Now()
	evt := &grpcevents.ServerStopped{
		Port:     s.port,
		StopTime: now,
		Duration: now.Sub(s.startTime),
	}
	s.startTime = time.Time{}
	return evt
}

// Shutdown gracefully stops the server with a context deadline
func (s *Server) Shutdown(ctx context.Context) error {
	done := make(chan struct{})

	// Run GracefulStop through async.GoWithRecover. The inner defer
	// close(done) runs in both normal return and panic paths (Go defers
	// fire LIFO before the panic propagates to the wrapper's recover), so
	// the select below always unblocks; no need to close(done) in the
	// recover callback, which would double-close.
	async.GoWithRecover(func() {
		defer close(done)
		s.GracefulStop()
	}, func(r any) {
		s.logger.Error("gRPC graceful stop panic recovered", "error", panicerr.FromRecovered(r))
	})

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.Stop()
		return ctx.Err()
	}
}

// SetEventDispatcher wires an event dispatcher into the Server. Safe to
// call before or after Start/StartAsync; mutex-protected so framework
// bootstrap can re-wire the dispatcher without racing the request path.
//
// Passing a nil fn clears the dispatcher and reverts the Server to a
// no-op emission state.
func (s *Server) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	s.eventMu.Lock()
	s.eventDispatcher = fn
	s.eventMu.Unlock()
}

// dispatchEvent fires an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped
// values. Failures from the dispatcher, errors and panics alike, are
// swallowed: the gRPC request path must never fail because of an event sink.
func (s *Server) dispatchEvent(ctx context.Context, evt any) {
	s.eventMu.RLock()
	fn := s.eventDispatcher
	s.eventMu.RUnlock()
	if fn == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() { _ = recover() }()
	_ = fn(ctx, evt)
}

// eventDispatchFunc adapts the Server's dispatcher to the interceptors
// packages' grpcevents.EventDispatchFunc. The returned func reads the
// dispatcher at call time, so SetEventDispatcher wiring after Build still
// takes effect, and it always returns nil: an interceptor must never fail
// a request because of an event-sink error.
func (s *Server) eventDispatchFunc() grpcevents.EventDispatchFunc {
	return func(ctx context.Context, event any) error {
		s.dispatchEvent(ctx, event)
		return nil
	}
}

// Address returns the address the server is listening on.
// Returns empty string if server hasn't been built yet.
func (s *Server) Address() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.listener != nil {
		return s.listener.Addr().String()
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
