package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/velocitykode/velocity/log"
)

// Gateway wraps an HTTP gateway that proxies to a gRPC server
type Gateway struct {
	mu           sync.RWMutex
	mux          *runtime.ServeMux
	httpServer   *http.Server
	port         string
	grpcEndpoint string
	dialOptions  []grpc.DialOption
	running      bool
	logger       log.Logger

	// Handler registration functions to call after gateway is built
	registrations []GatewayRegistrationFunc
	muxOptions    []runtime.ServeMuxOption

	// Middleware
	middleware []func(http.Handler) http.Handler

	// configErr holds any error from transport configuration, surfaced at Build() time
	configErr error
}

// GatewayRegistrationFunc is called to register handlers with the gateway
type GatewayRegistrationFunc func(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) error

// GatewayOption configures the Gateway
type GatewayOption func(*Gateway)

// NewGateway creates a new HTTP gateway with the given options
func NewGateway(opts ...GatewayOption) *Gateway {
	g := &Gateway{
		port:          "8080",
		registrations: make([]GatewayRegistrationFunc, 0),
		muxOptions: []runtime.ServeMuxOption{
			// Use JSON names and emit defaults
			runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
				MarshalOptions: protojson.MarshalOptions{
					UseProtoNames:   false,
					EmitUnpopulated: true,
				},
				UnmarshalOptions: protojson.UnmarshalOptions{
					DiscardUnknown: true,
				},
			}),
		},
		middleware: make([]func(http.Handler) http.Handler, 0),
	}

	for _, opt := range opts {
		opt(g)
	}

	// Default to a basic logger if none was provided
	if g.logger == nil {
		g.logger, _ = log.NewLogger(log.LogConfig{Driver: "console", Config: map[string]interface{}{"level": "info"}})
	}

	// If no transport credentials were configured via options, configure from environment
	if len(g.dialOptions) == 0 {
		opts, err := configureGatewayTransport()
		g.dialOptions = opts
		g.configErr = err
	}

	return g
}

// configureGatewayTransport configures gRPC dial credentials from environment variables.
// Checks GRPC_GATEWAY_TLS_CERT and GRPC_GATEWAY_TLS_KEY for TLS configuration.
// Falls back to insecure credentials only when GRPC_GATEWAY_INSECURE=true is explicitly set.
// Returns an error if neither TLS nor explicit insecure mode is configured.
func configureGatewayTransport() ([]grpc.DialOption, error) {
	certFile := os.Getenv("GRPC_GATEWAY_TLS_CERT")
	keyFile := os.Getenv("GRPC_GATEWAY_TLS_KEY")

	if certFile != "" && keyFile != "" {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		caCert, err := os.ReadFile(certFile)
		if err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(caCert) {
				tlsConfig.RootCAs = pool
			}
		}
		return []grpc.DialOption{
			grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		}, nil
	}

	if os.Getenv("GRPC_GATEWAY_INSECURE") == "true" {
		// Warning logged at gateway Build() time when logger is available
		return []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}, nil
	}

	return nil, fmt.Errorf("gRPC gateway: TLS is required. Set GRPC_GATEWAY_TLS_CERT and GRPC_GATEWAY_TLS_KEY, or set GRPC_GATEWAY_INSECURE=true for local development")
}

// GatewayWithPort sets the port for the HTTP gateway
func GatewayWithPort(port string) GatewayOption {
	return func(g *Gateway) {
		g.port = port
	}
}

// GatewayWithGRPCEndpoint sets the gRPC server endpoint to proxy to
func GatewayWithGRPCEndpoint(endpoint string) GatewayOption {
	return func(g *Gateway) {
		g.grpcEndpoint = endpoint
	}
}

// GatewayWithDialOption adds a gRPC dial option
func GatewayWithDialOption(opt grpc.DialOption) GatewayOption {
	return func(g *Gateway) {
		g.dialOptions = append(g.dialOptions, opt)
	}
}

// GatewayWithMuxOption adds a runtime.ServeMuxOption
func GatewayWithMuxOption(opt runtime.ServeMuxOption) GatewayOption {
	return func(g *Gateway) {
		g.muxOptions = append(g.muxOptions, opt)
	}
}

// GatewayWithLogger sets the logger for the HTTP gateway
func GatewayWithLogger(logger log.Logger) GatewayOption {
	return func(g *Gateway) {
		g.logger = logger
	}
}

// GatewayWithInsecure explicitly configures the gateway to use insecure credentials.
// This should only be used for local development.
func GatewayWithInsecure() GatewayOption {
	return func(g *Gateway) {
		g.dialOptions = []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}
	}
}

// GatewayWithTLS configures the gateway to use TLS credentials for the
// connection to the gRPC server. certFile is the CA certificate used to verify
// the server. If certFile is empty, the system certificate pool is used.
func GatewayWithTLS(certFile string) GatewayOption {
	return func(g *Gateway) {
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
		}

		if certFile != "" {
			caCert, err := os.ReadFile(certFile)
			if err != nil {
				g.configErr = fmt.Errorf("failed to read TLS cert file for gateway: %w", err)
				return
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caCert) {
				g.configErr = fmt.Errorf("failed to parse TLS cert for gateway: %s", certFile)
				return
			}
			tlsConfig.RootCAs = pool
		}

		g.dialOptions = []grpc.DialOption{
			grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		}
	}
}

// Use adds HTTP middleware to the gateway.
// Middleware is applied in the order added (outermost first).
func (g *Gateway) Use(middleware ...func(http.Handler) http.Handler) *Gateway {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.middleware = append(g.middleware, middleware...)
	return g
}

// RegisterHandler registers a gRPC-Gateway handler with the gateway.
// The handler function should match the pattern generated by grpc-gateway:
//
//	gateway.RegisterHandler(pb.RegisterMyServiceHandlerFromEndpoint)
func (g *Gateway) RegisterHandler(handler GatewayRegistrationFunc) *Gateway {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.registrations = append(g.registrations, handler)
	return g
}

// Build constructs the HTTP gateway with all configured handlers.
// This is called automatically by Start() if not called explicitly.
func (g *Gateway) Build(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.mux != nil {
		return nil // Already built
	}

	if g.configErr != nil {
		return g.configErr
	}

	if g.grpcEndpoint == "" {
		return ErrNoEndpoint
	}

	// Validate endpoint format (must be host:port)
	if _, _, err := net.SplitHostPort(g.grpcEndpoint); err != nil {
		return fmt.Errorf("invalid gRPC endpoint %q: expected host:port format: %w", g.grpcEndpoint, err)
	}

	// Create mux with options
	g.mux = runtime.NewServeMux(g.muxOptions...)

	// Register all handlers
	for _, regFunc := range g.registrations {
		if err := regFunc(ctx, g.mux, g.grpcEndpoint, g.dialOptions); err != nil {
			return fmt.Errorf("failed to register gateway handler: %w", err)
		}
	}

	// Build handler with middleware
	var handler http.Handler = g.mux
	// Apply middleware in reverse order so first added is outermost
	for i := len(g.middleware) - 1; i >= 0; i-- {
		handler = g.middleware[i](handler)
	}

	// Create HTTP server
	g.httpServer = &http.Server{
		Addr:              ":" + g.port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return nil
}

// Start builds (if not already built) and starts the HTTP gateway.
// This method blocks until the server is stopped.
func (g *Gateway) Start() error {
	ctx := context.Background()
	return g.StartWithContext(ctx)
}

// StartWithContext builds and starts the HTTP gateway with a context.
func (g *Gateway) StartWithContext(ctx context.Context) error {
	if err := g.Build(ctx); err != nil {
		return err
	}

	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return ErrServerAlreadyRunning
	}
	g.running = true
	g.mu.Unlock()

	g.logger.Info("HTTP gateway starting",
		"address", g.httpServer.Addr,
		"grpc_endpoint", g.grpcEndpoint,
	)
	return g.httpServer.ListenAndServe()
}

// StartAsync builds and starts the HTTP gateway in a goroutine.
// Returns immediately. Use Stop() or Shutdown() to stop the gateway.
func (g *Gateway) StartAsync() error {
	ctx := context.Background()
	return g.StartAsyncWithContext(ctx)
}

// StartAsyncWithContext builds and starts the HTTP gateway in a goroutine with a context.
func (g *Gateway) StartAsyncWithContext(ctx context.Context) error {
	if err := g.Build(ctx); err != nil {
		return err
	}

	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return ErrServerAlreadyRunning
	}
	g.running = true
	g.mu.Unlock()

	go func() {
		g.logger.Info("HTTP gateway starting",
			"address", g.httpServer.Addr,
			"grpc_endpoint", g.grpcEndpoint,
		)
		if err := g.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			g.logger.Error("HTTP gateway error", "error", err)
		}
	}()

	return nil
}

// Stop stops the HTTP gateway immediately
func (g *Gateway) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.httpServer != nil && g.running {
		g.logger.Info("HTTP gateway stopping")
		g.httpServer.Close()
		g.running = false
	}
}

// Shutdown gracefully shuts down the HTTP gateway
func (g *Gateway) Shutdown(ctx context.Context) error {
	g.mu.Lock()
	if g.httpServer == nil || !g.running {
		g.mu.Unlock()
		return nil
	}
	server := g.httpServer
	g.running = false
	g.mu.Unlock()

	g.logger.Info("HTTP gateway gracefully shutting down")
	return server.Shutdown(ctx)
}

// Address returns the address the gateway is listening on
func (g *Gateway) Address() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.httpServer != nil {
		return g.httpServer.Addr
	}
	return ""
}

// Port returns the configured port
func (g *Gateway) Port() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.port
}

// GRPCEndpoint returns the configured gRPC endpoint
func (g *Gateway) GRPCEndpoint() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.grpcEndpoint
}

// IsRunning returns true if the gateway is currently running
func (g *Gateway) IsRunning() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.running
}

// Mux returns the underlying runtime.ServeMux.
// Returns nil if the gateway hasn't been built yet.
func (g *Gateway) Mux() *runtime.ServeMux {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.mux
}
