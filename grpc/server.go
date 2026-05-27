package grpc

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/log"
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

	// tlsOpted tracks whether the caller supplied transport credentials via
	// WithCreds or WithServerOption(grpc.Creds(...)). Build uses this together
	// with the environment and the GRPC_INSECURE escape hatch to decide
	// whether to refuse a cleartext production start.
	tlsOpted bool

	// Interceptors
	unaryInterceptors  []grpc.UnaryServerInterceptor
	streamInterceptors []grpc.StreamServerInterceptor

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
		environment:        os.Getenv("APP_ENV"),
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

// WithMaxRecvMsgSize sets the maximum receive message size
func WithMaxRecvMsgSize(size int) ServerOption {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, grpc.MaxRecvMsgSize(size))
	}
}

// WithMaxSendMsgSize sets the maximum send message size
func WithMaxSendMsgSize(size int) ServerOption {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, grpc.MaxSendMsgSize(size))
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
		insecureOptOut := os.Getenv("GRPC_INSECURE") == "true"
		if s.environment == "production" && !insecureOptOut {
			return fmt.Errorf("velocity/grpc: TLS credentials are required in production. Use WithCreds, or call WithExplicitTLS if you supplied credentials via WithServerOption(grpc.Creds(...)). Set GRPC_INSECURE=true to opt out for a known-internal mTLS mesh")
		}
		if s.environment != "production" {
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

	// Build server options with interceptor chains
	opts := make([]grpc.ServerOption, 0, len(s.serverOptions)+2)
	opts = append(opts, s.serverOptions...)

	if len(s.unaryInterceptors) > 0 {
		opts = append(opts, grpc.ChainUnaryInterceptor(s.unaryInterceptors...))
	}
	if len(s.streamInterceptors) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(s.streamInterceptors...))
	}

	// Create server
	s.grpcServer = grpc.NewServer(opts...)

	// Register all services
	for _, regFunc := range s.registrations {
		regFunc(s.grpcServer)
	}

	// Enable reflection if configured. Hard-fail in production — silently
	// downgrading to "reflection disabled" lets misconfigured deployments ship
	// with a false sense of security (operators think reflection is on).
	if s.enableReflection {
		if s.environment == "production" {
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
	s.mu.Unlock()

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
	s.mu.Unlock()

	// Run through async.GoWithRecover so the recover path flows through
	// the canonical async package while still resetting s.running so the
	// server can be restarted after a crash.
	async.GoWithRecover(func() {
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
	defer s.mu.Unlock()

	if s.grpcServer != nil && s.running {
		s.logger.Info("gRPC server stopping")
		s.grpcServer.Stop()
		s.running = false
	}
}

// GracefulStop gracefully stops the gRPC server
func (s *Server) GracefulStop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.grpcServer != nil && s.running {
		s.logger.Info("gRPC server gracefully stopping")
		s.grpcServer.GracefulStop()
		s.running = false
	}
}

// Shutdown gracefully stops the server with a context deadline
func (s *Server) Shutdown(ctx context.Context) error {
	done := make(chan struct{})

	// Run GracefulStop through async.GoWithRecover. The inner defer
	// close(done) runs in both normal return and panic paths (Go defers
	// fire LIFO before the panic propagates to the wrapper's recover), so
	// the select below always unblocks — no need to close(done) in the
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
// values. Failures from the dispatcher are swallowed: the gRPC request
// path must never fail because of an event sink.
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
	_ = fn(ctx, evt)
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
