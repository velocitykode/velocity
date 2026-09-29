package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
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
	logger       contract.Logger

	// building is set while a Build constructs the gateway outside the
	// lock, so a concurrent or re-entrant Build returns ErrBuildInProgress
	// instead of constructing a second one. Guarded by mu.
	building bool

	// drained is made by the stop that ends a running gateway, the one that
	// owns its drain, and closed when that stop's net/http Close or
	// Shutdown returns. An overlapping Shutdown waits on it or its own ctx.
	// Guarded by mu; nil until a stop ended a running gateway.
	drained chan struct{}

	// stops records the goroutines running the gateway's stop line, so a
	// stop called back from there does not wait on it.
	stops stopGuard

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

	// Without a logger (or with a nil one) the gateway writes through the
	// framework's standalone fallback logger.
	g.logger = fallbacklog.Resolve(g.logger)

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

// GatewayWithLogger sets the logger for the HTTP gateway. Without it, or
// with nil, the gateway writes through the framework's standalone fallback
// logger, which writes warnings and errors to standard error.
func GatewayWithLogger(logger contract.Logger) GatewayOption {
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
//
// Build runs application code (the registration handlers, the middleware
// and the logger) without holding the gateway's lock, so that code may
// call the gateway's accessors. A Build called while another one is
// constructing the gateway, concurrently or from that application code,
// returns ErrBuildInProgress at once; a Build after a completed one
// returns nil. A Build that fails publishes nothing, so a later Build
// runs again.
func (g *Gateway) Build(ctx context.Context) error {
	b, err := g.beginBuild()
	if b == nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			g.mu.Lock()
			g.building = false
			g.mu.Unlock()
		}
	}()

	if b.warnInsecure {
		fallbacklog.Write(b.logger, func(l contract.Logger) {
			l.Warn("gRPC gateway dialling upstream with insecure credentials. Configure TLS via GatewayWithTLS or GatewayWithTransportConfig before deploying to production",
				"grpc_endpoint", b.grpcEndpoint,
			)
		})
	}

	// Create mux with options
	mux := runtime.NewServeMux(b.muxOptions...)

	// Register all handlers. Every registration dials through the
	// gatewayPropagation client interceptors, so the gRPC half of a gateway
	// call carries the trace and request id correlateGatewayRequest selected
	// for the HTTP half.
	propagation := gatewayPropagation()
	dialOptions := append(slices.Clip(b.dialOptions),
		grpc.WithChainUnaryInterceptor(propagation.Unary),
		grpc.WithChainStreamInterceptor(propagation.Stream),
	)
	for _, regFunc := range b.registrations {
		if err := regFunc(ctx, mux, b.grpcEndpoint, dialOptions); err != nil {
			return fmt.Errorf("velocity/grpc: failed to register gateway handler: %w", err)
		}
	}

	// Build handler with middleware
	var handler http.Handler = mux
	// Apply middleware in reverse order so first added is outermost
	for i := len(b.middleware) - 1; i >= 0; i-- {
		handler = b.middleware[i](handler)
	}
	// Correlation wraps everything, so application middleware already sees
	// the request id and trace.
	handler = correlateGatewayRequest(handler)

	// Create HTTP server
	server := &http.Server{
		Addr:              ":" + b.port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       b.readTimeout,
		WriteTimeout:      b.writeTimeout,
		IdleTimeout:       b.idleTimeout,
		MaxHeaderBytes:    b.maxHeaderBytes,
	}

	g.mu.Lock()
	g.mux = mux
	g.httpServer = server
	g.building = false
	g.mu.Unlock()
	published = true
	return nil
}

// gatewayBuildPlan is the configuration one Build constructs the gateway
// from, copied under the lock so the construction runs without it.
type gatewayBuildPlan struct {
	logger                                 contract.Logger
	port, grpcEndpoint                     string
	dialOptions                            []grpc.DialOption
	muxOptions                             []runtime.ServeMuxOption
	registrations                          []GatewayRegistrationFunc
	middleware                             []func(http.Handler) http.Handler
	readTimeout, writeTimeout, idleTimeout time.Duration
	maxHeaderBytes                         int
	warnInsecure                           bool
}

// beginBuild runs Build's checks under the lock and, when they pass,
// marks a Build in progress and returns the plan to construct from. It
// returns a nil plan with a nil error when the gateway is already built,
// and with an error when a Build is in progress or a check fails. It
// calls no application code.
func (g *Gateway) beginBuild() (*gatewayBuildPlan, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.mux != nil {
		return nil, nil // Already built
	}
	if g.building {
		return nil, ErrBuildInProgress
	}

	if g.configErr != nil {
		return nil, g.configErr
	}

	// Enforce the production TLS guard before any other validation so the
	// error is unambiguous when an operator forgets to wire credentials.
	warnInsecure := false
	if !g.credsOpted {
		// Routed through contract.IsProductionEnv so "prod" and "staging"
		// are refused alongside "production". A typo'd APP_ENV cannot
		// silently downgrade the gateway to insecure dial credentials.
		if contract.IsProductionEnv(g.environment) {
			return nil, fmt.Errorf("velocity/grpc: gateway TLS credentials are required in production. Use GatewayWithTLS, GatewayWithTransportConfig, or GatewayWithInsecure to opt out for a known-internal mesh")
		}
		warnInsecure = true
		g.dialOptions = []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}
	}

	if g.grpcEndpoint == "" {
		return nil, ErrNoEndpoint
	}

	// Validate endpoint format (must be host:port)
	if _, _, err := net.SplitHostPort(g.grpcEndpoint); err != nil {
		return nil, fmt.Errorf("velocity/grpc: invalid grpc endpoint %q: expected host:port format: %w", g.grpcEndpoint, err)
	}

	g.building = true
	return &gatewayBuildPlan{
		logger:         g.logger,
		port:           g.port,
		grpcEndpoint:   g.grpcEndpoint,
		dialOptions:    slices.Clone(g.dialOptions),
		muxOptions:     slices.Clone(g.muxOptions),
		registrations:  slices.Clone(g.registrations),
		middleware:     slices.Clone(g.middleware),
		readTimeout:    g.readTimeout,
		writeTimeout:   g.writeTimeout,
		idleTimeout:    g.idleTimeout,
		maxHeaderBytes: g.maxHeaderBytes,
		warnInsecure:   warnInsecure,
	}, nil
}

// correlateGatewayRequest gives a gateway request the request id and trace
// its proxied gRPC call carries.
//
// Request id: the caller's X-Request-ID when trace.ValidRequestID accepts
// it, otherwise a generated one. It is stored on the request context and
// echoed on the response, so the HTTP and gRPC halves of the call share
// one id.
//
// Trace: the gateway records no span of its own, so a valid traceparent is
// installed as the context's current span unchanged and the proxied call
// names the HTTP caller's span as the gRPC server's parent, with the
// caller's sampled flag. Without one the context carries no trace and the
// gRPC server starts a root span.
//
// The context is marked as a gateway request, so gatewayPropagation sends
// these carriers in place of any the outgoing metadata already holds.
func correlateGatewayRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := singleHeaderValue(r.Header, trace.RequestIDHeader)
		if !trace.ValidRequestID(id) {
			id = trace.GenerateRequestID()
		}
		ctx = trace.WithRequestID(ctx, id)
		w.Header().Set(trace.RequestIDHeader, id)
		var call gatewayCall
		if parent, ok := trace.ParseTraceparent(singleHeaderValue(r.Header, trace.TraceparentHeader)); ok {
			ctx = trace.WithFullContext(ctx, parent.TraceID, parent.SpanID, "")
			call.parent = parent
		}
		ctx = context.WithValue(ctx, gatewayCallKey{}, call)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// gatewayCallKey is the context key under which correlateGatewayRequest
// marks a gateway request.
type gatewayCallKey struct{}

// gatewayCall is what correlateGatewayRequest accepted from a gateway
// request's headers.
type gatewayCall struct {
	// parent is the inbound traceparent; the zero Parent when the gateway
	// accepted none.
	parent trace.Parent
}

// forwardsParent reports whether ctx's current span is still the inbound
// caller's span, forwarded unchanged: no middleware started a span of its
// own after correlateGatewayRequest.
func (c gatewayCall) forwardsParent(ctx context.Context) bool {
	return c.parent.TraceID != "" &&
		trace.GetTraceID(ctx) == c.parent.TraceID &&
		trace.GetSpanID(ctx) == c.parent.SpanID
}

// gatewayPropagation returns the client interceptors every gateway
// registration dials through: interceptors.Propagation, and on a call made
// while serving a gateway request, the carriers the gateway selected first.
//
// grpc-gateway turns Grpc-Metadata- prefixed HTTP headers (and whatever the
// mux's header matcher and metadata annotators produce) into outgoing
// metadata, and Propagation leaves a key the outgoing metadata already holds
// as it is. On the gateway path those values would otherwise override the
// request id the gateway echoes and the trace it accepted, so gatewayCarriers
// replaces them first. A call through these clients outside a gateway
// request keeps Propagation's rule.
func gatewayPropagation() interceptors.ClientInterceptorPair {
	p := interceptors.Propagation()
	return interceptors.ClientInterceptorPair{
		Unary: func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return p.Unary(gatewayCarriers(ctx), method, req, reply, cc, invoker, opts...)
		},
		Stream: func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return p.Stream(gatewayCarriers(ctx), desc, cc, method, streamer, opts...)
		},
	}
}

// gatewayCarriers returns ctx with the outgoing traceparent and x-request-id
// metadata set to exactly the carriers ctx holds (see trace.Propagate), when
// ctx is a gateway request's. Any other value under those keys is removed,
// including when ctx holds no carrier for a key because the gateway rejected
// the inbound header. A traceparent naming the caller's span unchanged keeps
// the caller's sampled flag: W3C Trace Context lets a participant change
// the flag only when it sends a span of its own. Other contexts are returned
// as they are.
func gatewayCarriers(ctx context.Context) context.Context {
	call, ok := ctx.Value(gatewayCallKey{}).(gatewayCall)
	if !ok {
		return ctx
	}
	md, _ := metadata.FromOutgoingContext(ctx) // a copy
	if md == nil {
		md = metadata.MD{}
	}
	md.Delete(trace.TraceparentHeader)
	md.Delete(trace.RequestIDHeader)
	trace.Propagate(ctx, func(name, value string) {
		if name == trace.TraceparentHeader && call.forwardsParent(ctx) {
			value, _ = trace.FormatTraceparent(call.parent)
		}
		md.Set(name, value)
	})
	return metadata.NewOutgoingContext(ctx, md)
}

// singleHeaderValue returns the one value h holds for key, or the empty
// string when it holds none or several: a repeated carrier is ambiguous and
// treated as absent.
func singleHeaderValue(h http.Header, key string) string {
	values := h.Values(key)
	if len(values) != 1 {
		return ""
	}
	return values[0]
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

	g.logStarting()
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
		g.logStarting()
		if err := g.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fallbacklog.Write(g.logger, func(l contract.Logger) { l.Error("HTTP gateway error", "error", err) })
		}
	}, func(r any) {
		fallbacklog.Write(g.logger, func(l contract.Logger) { l.Error("HTTP gateway panic recovered", "error", panicerr.FromRecovered(r)) })
		g.mu.Lock()
		g.running = false
		g.mu.Unlock()
	})

	return nil
}

// Stop stops the HTTP gateway immediately. It changes the gateway's state
// under its lock, then logs and closes the server without it, so the
// logger may call the gateway's accessors. During a Shutdown it closes the
// gateway, cutting the requests that Shutdown is draining.
func (g *Gateway) Stop() {
	server, owner, drained := g.beginStop()
	switch {
	case owner:
		defer close(drained)
		g.stops.run(func() {
			fallbacklog.Write(g.logger, func(l contract.Logger) { l.Info("HTTP gateway stopping") })
		})
		_ = server.Close()
	case server != nil:
		_ = server.Close()
	}
}

// Shutdown gracefully shuts down the HTTP gateway: it stops accepting
// requests and waits for those in flight to finish until ctx is done. At
// the deadline it returns the ctx error and closes the gateway, cutting
// the requests still in flight. A Shutdown that overlaps one already
// draining waits for that drain the same way, so a nil return always
// means the requests in flight have finished.
//
// A Shutdown called from the gateway's own stop line cannot wait on the
// stop it runs in: it returns an error wrapping http.ErrServerClosed at
// once. A Shutdown called from a request handler waits for that handler
// until its ctx is done.
func (g *Gateway) Shutdown(ctx context.Context) error {
	nested := g.stops.nested()
	server, owner, drained := g.beginStop()
	if server == nil {
		return nil
	}
	var drainErr error
	if owner {
		g.stops.run(func() {
			fallbacklog.Write(g.logger, func(l contract.Logger) { l.Info("HTTP gateway gracefully shutting down") })
		})
		// The drain outlives this caller's ctx, so an overlapping Shutdown
		// with a later deadline still waits for it to end.
		async.Go(func() {
			defer close(drained)
			drainErr = server.Shutdown(context.Background())
		})
	} else if nested && !closed(drained) {
		return errGatewayShutdownNested
	}
	if err := awaitStop(ctx, drained, func() { _ = server.Close() }); err != nil {
		return err
	}
	return drainErr // read after drained closed, which its write precedes
}

// errGatewayShutdownNested is what a Shutdown called from the gateway's own
// stop line returns: it cannot wait on the stop it runs in.
var errGatewayShutdownNested = fmt.Errorf("velocity/grpc: Shutdown called from inside a stop of this gateway: %w", http.ErrServerClosed)

// beginStop records a stop under the lock. A running gateway stops
// running, and this stop owns its drain: server is returned with owner
// set and a fresh drained. A gateway a stop already ended returns server
// and that stop's drained, for a Shutdown to wait on and a Stop to force.
// Otherwise server is nil. It calls no application code.
func (g *Gateway) beginStop() (server *http.Server, owner bool, drained chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.httpServer != nil && g.running:
		g.running = false
		g.drained = make(chan struct{})
		return g.httpServer, true, g.drained
	case g.httpServer != nil && g.drained != nil:
		return g.httpServer, false, g.drained
	}
	return nil, false, nil
}

// logStarting writes the starting line through fallbacklog.Write, so a
// panicking logger never skips the serve that follows it.
func (g *Gateway) logStarting() {
	fallbacklog.Write(g.logger, func(l contract.Logger) {
		l.Info("HTTP gateway starting",
			"address", g.httpServer.Addr,
			"grpc_endpoint", g.grpcEndpoint,
		)
	})
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
