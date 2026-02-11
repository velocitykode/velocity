package grpc

import (
	"context"
	"fmt"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/velocitykode/velocity/pkg/log"
)

// Server wraps a gRPC server with Velocity patterns
type Server struct {
	mu               sync.RWMutex
	grpcServer       *grpc.Server
	listener         net.Listener
	port             string
	enableReflection bool
	running          bool
	serverOptions    []grpc.ServerOption

	// Interceptors
	unaryInterceptors  []grpc.UnaryServerInterceptor
	streamInterceptors []grpc.StreamServerInterceptor

	// Registration functions to call after server is built
	registrations []RegistrationFunc
}

// ServerOption configures the Server
type ServerOption func(*Server)

// NewServer creates a new gRPC server with the given options
func NewServer(opts ...ServerOption) *Server {
	s := &Server{
		port:               "50051",
		enableReflection:   false,
		unaryInterceptors:  make([]grpc.UnaryServerInterceptor, 0),
		streamInterceptors: make([]grpc.StreamServerInterceptor, 0),
		registrations:      make([]RegistrationFunc, 0),
		serverOptions:      make([]grpc.ServerOption, 0),
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// WithPort sets the port for the gRPC server
func WithPort(port string) ServerOption {
	return func(s *Server) {
		s.port = port
	}
}

// WithReflection enables or disables gRPC reflection
func WithReflection(enabled bool) ServerOption {
	return func(s *Server) {
		s.enableReflection = enabled
	}
}

// WithServerOption adds a grpc.ServerOption to the server
func WithServerOption(opt grpc.ServerOption) ServerOption {
	return func(s *Server) {
		s.serverOptions = append(s.serverOptions, opt)
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
func (s *Server) Build() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.grpcServer != nil {
		return nil // Already built
	}

	// Create listener
	lis, err := net.Listen("tcp", ":"+s.port)
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", s.port, err)
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

	// Enable reflection if configured
	if s.enableReflection {
		log.Warn("gRPC reflection is enabled — disable in production (GRPC_REFLECTION=false)")
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

	log.Info("gRPC server starting", "address", s.listener.Addr().String())
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

	go func() {
		log.Info("gRPC server starting", "address", s.listener.Addr().String())
		if err := s.grpcServer.Serve(s.listener); err != nil {
			log.Error("gRPC server error", "error", err)
		}
	}()

	return nil
}

// Stop stops the gRPC server immediately
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.grpcServer != nil && s.running {
		log.Info("gRPC server stopping")
		s.grpcServer.Stop()
		s.running = false
	}
}

// GracefulStop gracefully stops the gRPC server
func (s *Server) GracefulStop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.grpcServer != nil && s.running {
		log.Info("gRPC server gracefully stopping")
		s.grpcServer.GracefulStop()
		s.running = false
	}
}

// Shutdown gracefully stops the server with a context deadline
func (s *Server) Shutdown(ctx context.Context) error {
	done := make(chan struct{})

	go func() {
		s.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.Stop()
		return ctx.Err()
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
