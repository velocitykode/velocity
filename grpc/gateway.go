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

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/log"
)

// Conservative defaults for the internet-facing HTTP gateway's http.Server.
// Without full timeouts a client can drip a request body (Slowloris-style) or
// hold idle/slow-write connections to exhaust goroutines and connections, and
// the downstream gRPC MaxRecvMsgSize does not bound how long a connection is
// held at the gateway. These are secure-by-default and overridable via the
// GatewayWith* options below.
const (
	defaultGatewayReadTimeout    = 30 * time.Second
	defaultGatewayWriteTimeout   = 60 * time.Second
	defaultGatewayIdleTimeout    = 120 * time.Second
	defaultGatewayMaxHeaderBytes = 1 << 20 // 1 MiB
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

	// HTTP server timeout/header bounds applied to httpServer in Build().
	// Defaulted in NewGateway() to the conservative package constants so a
	// zero-option Gateway is secure by default; overridable via GatewayWith*.
	readTimeout    time.Duration
	writeTimeout   time.Duration
	idleTimeout    time.Duration
	maxHeaderBytes int

	// environment is the deployment environment (e.g., "production", "staging").
	// When set to "production", Build() refuses to start without explicitly
	// configured transport credentials. Defaults to APP_ENV at construction.
	environment string

	// credsOpted tracks whether the caller supplied transport credentials via
	// a Gateway*With* option that sets dial credentials. The production guard
	// in Build() uses this to decide whether to refuse the cleartext default.
	credsOpted bool

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

// NewGateway creates a new HTTP gateway with the given options. Defaults
// are sourced from environment variables (via LoadConfig) so behaviour
// matches the rest of the framework: GATEWAY_PORT and GRPC_ENDPOINT are
// honoured if set. Explicit GatewayOption arguments still override the
// env-derived defaults.
func NewGateway(opts ...GatewayOption) *Gateway {
	cfg := LoadConfig()
	g := &Gateway{
		port:           cfg.GatewayPort,
		grpcEndpoint:   cfg.GRPCEndpoint,
		environment:    contract.GetEnv(),
		readTimeout:    defaultGatewayReadTimeout,
		writeTimeout:   defaultGatewayWriteTimeout,
		idleTimeout:    defaultGatewayIdleTimeout,
		maxHeaderBytes: defaultGatewayMaxHeaderBytes,
		registrations:  make([]GatewayRegistrationFunc, 0),
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

	return g
}

// GatewayTransportConfig holds TLS configuration for the gateway's connection
// to the gRPC server.
//
// TLSCert and TLSKey form the client identity used for mutual TLS via
// tls.LoadX509KeyPair. CACert (optional) pins the server's CA; when empty,
// the system root CA pool is used to verify the server.
type GatewayTransportConfig struct {
	// TLSCert is the path to the client certificate PEM file (mTLS client identity).
	TLSCert string
	// TLSKey is the path to the client private key PEM file (mTLS client identity).
	TLSKey string
	// CACert is the optional path to a CA certificate PEM file used to verify
	// the upstream gRPC server. When empty, the system root CA pool is used.
	CACert string
	// Insecure disables TLS. Only use for local development.
	Insecure bool
}

// GatewayWithTransportConfig configures the gateway transport from a typed config.
// If both TLSCert and TLSKey are set, TLS is enabled and the cert/key pair is
// loaded as the client identity for mutual TLS. If CACert is set, it is loaded
// as the trust anchor for verifying the upstream gRPC server. If Insecure is
// true, insecure credentials are used. Returns an error at Build() time if
// neither is configured, or if any referenced file cannot be read or parsed.
func GatewayWithTransportConfig(cfg GatewayTransportConfig) GatewayOption {
	return func(g *Gateway) {
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

			// Load the client cert and key for mTLS. Any error here is a hard
			// failure: silently dialling without a client cert is a regression.
			clientCert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
			if err != nil {
				g.configErr = fmt.Errorf("velocity/grpc: failed to load gateway client cert/key (%s, %s): %w", cfg.TLSCert, cfg.TLSKey, err)
				return
			}
			tlsConfig.Certificates = []tls.Certificate{clientCert}

			// If a CA cert is provided, pin it. Any error here is a hard
			// failure: silently falling back to system roots when an operator
			// asked for a private CA is the trap that I-01 is fixing.
			if cfg.CACert != "" {
				caCert, err := os.ReadFile(cfg.CACert)
				if err != nil {
					g.configErr = fmt.Errorf("velocity/grpc: failed to read gateway CA cert %q: %w", cfg.CACert, err)
					return
				}
				pool := x509.NewCertPool()
				if !pool.AppendCertsFromPEM(caCert) {
					g.configErr = fmt.Errorf("velocity/grpc: failed to parse gateway CA cert %q (no valid PEM blocks)", cfg.CACert)
					return
				}
				tlsConfig.RootCAs = pool
			}

			g.dialOptions = []grpc.DialOption{
				grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
			}
			g.credsOpted = true
			return
		}

		if cfg.Insecure {
			g.dialOptions = []grpc.DialOption{
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			}
			g.credsOpted = true
			return
		}

		g.configErr = fmt.Errorf("velocity/grpc: gateway TLS is required. Set TLSCert and TLSKey, or set Insecure=true for local development")
	}
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

// GatewayWithDialOption adds a gRPC dial option. Callers that pass a transport
// credentials option through this hook should also set the environment
// explicitly so the production guard in Build() does not refuse the start.
func GatewayWithDialOption(opt grpc.DialOption) GatewayOption {
	return func(g *Gateway) {
		g.dialOptions = append(g.dialOptions, opt)
		// We cannot inspect grpc.DialOption to know if it set credentials, so
		// any caller using this hook is treated as having opted into managing
		// transport credentials themselves.
		g.credsOpted = true
	}
}

// GatewayWithEnvironment sets the deployment environment (e.g., "production",
// "staging"). When set to "production", Build() refuses to start without
// explicitly configured transport credentials.
func GatewayWithEnvironment(env string) GatewayOption {
	return func(g *Gateway) {
		g.environment = env
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

// GatewayWithReadTimeout sets the maximum duration for reading the entire
// request, including the body, on the gateway's HTTP server. Operators running
// the gateway behind an L7 proxy that already enforces request timeouts may
// relax this.
func GatewayWithReadTimeout(d time.Duration) GatewayOption {
	return func(g *Gateway) {
		g.readTimeout = d
	}
}

// GatewayWithWriteTimeout sets the maximum duration before timing out writes of
// the response on the gateway's HTTP server. Operators behind an L7 proxy that
// enforces its own response timeouts may relax this.
func GatewayWithWriteTimeout(d time.Duration) GatewayOption {
	return func(g *Gateway) {
		g.writeTimeout = d
	}
}

// GatewayWithIdleTimeout sets the maximum time to wait for the next request on a
// keep-alive connection on the gateway's HTTP server. Operators behind an L7
// proxy that manages connection reuse may relax this.
func GatewayWithIdleTimeout(d time.Duration) GatewayOption {
	return func(g *Gateway) {
		g.idleTimeout = d
	}
}

// GatewayWithMaxHeaderBytes sets the maximum number of bytes the gateway's HTTP
// server will read parsing request headers. Operators behind an L7 proxy that
// already bounds header size may relax this.
func GatewayWithMaxHeaderBytes(n int) GatewayOption {
	return func(g *Gateway) {
		g.maxHeaderBytes = n
	}
}

// GatewayWithInsecure explicitly configures the gateway to use insecure credentials.
// This should only be used for local development.
func GatewayWithInsecure() GatewayOption {
	return func(g *Gateway) {
		g.dialOptions = []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}
		g.credsOpted = true
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
				g.configErr = fmt.Errorf("velocity/grpc: failed to read tls cert file for gateway: %w", err)
				return
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caCert) {
				g.configErr = fmt.Errorf("velocity/grpc: failed to parse tls cert for gateway: %s", certFile)
				return
			}
			tlsConfig.RootCAs = pool
		}

		g.dialOptions = []grpc.DialOption{
			grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		}
		g.credsOpted = true
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
//
// Build refuses to start in production (APP_ENV=production or
// GatewayWithEnvironment("production")) when no transport credentials have
// been configured. Outside production, an unconfigured gateway defaults to
// insecure credentials and emits a one-shot warning. Operators that
// deliberately run a cleartext mesh must opt in via GatewayWithInsecure().
func (g *Gateway) Build(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.mux != nil {
		return nil // Already built
	}

	if g.configErr != nil {
		return g.configErr
	}

	// Enforce the production TLS guard before any other validation so the
	// error is unambiguous when an operator forgets to wire credentials.
	if !g.credsOpted {
		// Routed through contract.IsProductionEnv so "prod" and "staging"
		// are refused alongside "production". A typo'd APP_ENV cannot
		// silently downgrade the gateway to insecure dial credentials.
		if contract.IsProductionEnv(g.environment) {
			return fmt.Errorf("velocity/grpc: gateway TLS credentials are required in production. Use GatewayWithTLS, GatewayWithTransportConfig, or GatewayWithInsecure to opt out for a known-internal mesh")
		}
		g.logger.Warn("gRPC gateway dialling upstream with insecure credentials. Configure TLS via GatewayWithTLS or GatewayWithTransportConfig before deploying to production",
			"grpc_endpoint", g.grpcEndpoint,
		)
		g.dialOptions = []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}
	}

	if g.grpcEndpoint == "" {
		return ErrNoEndpoint
	}

	// Validate endpoint format (must be host:port)
	if _, _, err := net.SplitHostPort(g.grpcEndpoint); err != nil {
		return fmt.Errorf("velocity/grpc: invalid grpc endpoint %q: expected host:port format: %w", g.grpcEndpoint, err)
	}

	// Create mux with options
	g.mux = runtime.NewServeMux(g.muxOptions...)

	// Register all handlers
	for _, regFunc := range g.registrations {
		if err := regFunc(ctx, g.mux, g.grpcEndpoint, g.dialOptions); err != nil {
			return fmt.Errorf("velocity/grpc: failed to register gateway handler: %w", err)
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
		ReadTimeout:       g.readTimeout,
		WriteTimeout:      g.writeTimeout,
		IdleTimeout:       g.idleTimeout,
		MaxHeaderBytes:    g.maxHeaderBytes,
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

	// Run through async.GoWithRecover so the recover path flows through
	// the canonical async package (and trips the forbidigo rule only if
	// someone regresses to `go func`). The custom recovery handler resets
	// the running flag so the gateway can be restarted after a crash.
	async.GoWithRecover(func() {
		g.logger.Info("HTTP gateway starting",
			"address", g.httpServer.Addr,
			"grpc_endpoint", g.grpcEndpoint,
		)
		if err := g.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			g.logger.Error("HTTP gateway error", "error", err)
		}
	}, func(r any) {
		g.logger.Error("HTTP gateway panic recovered", "error", panicerr.FromRecovered(r))
		g.mu.Lock()
		g.running = false
		g.mu.Unlock()
	})

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
