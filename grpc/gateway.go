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

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/internal/errchain"
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
	port         string
	grpcEndpoint string
	dialOptions  []grpc.DialOption
	logger       contract.Logger

	// own is the gateway's own work: the goroutines running a Build, a
	// serve loop or a stop, each of which may call user code (a
	// registration, a middleware constructor, the logger) that calls a
	// stop back. A stop entered from there does not wait on it.
	own drain.Owner

	// cur is the gateway's current life (see gatewayLife), nil until the
	// first Build. Guarded by mu.
	cur *gatewayLife

	// HTTP server timeout/header bounds applied to the http.Server Build makes.
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

	// providedListener is the caller's listener (GatewayWithListener) a
	// Start serves on instead of binding the port, nil when there is none.
	providedListener net.Listener
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

// GatewayWithListener makes the gateway serve on a caller-supplied
// net.Listener instead of binding its port: a Start serves on lis, and the
// gateway reports lis's address (read once, when the Start takes it). It
// takes precedence over GatewayWithPort for the bind target. From the
// Start that serves on it the listener is the gateway's, closed when the
// gateway stops as it would close its own; a gateway stopped before any
// Start never took it and leaves it open.
func GatewayWithListener(lis net.Listener) GatewayOption {
	return func(g *Gateway) {
		g.providedListener = lis
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
// runs again. A stop, called from that application code or anywhere
// else, ends a Build in progress: the Build publishes nothing and
// returns http.ErrServerClosed, so a Start that called it returns that
// too instead of serving past the stop; a Shutdown waits for the Build
// to return. A later Build constructs the gateway afresh.
func (g *Gateway) Build(ctx context.Context) error {
	var err error
	g.own.Do(func() { err = g.build(ctx) })
	return err
}

// build is Build, run as the gateway's own work.
func (g *Gateway) build(ctx context.Context) error {
	c, b, err := g.beginBuild()
	if c == nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			g.abortBuild(c)
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
	// the request id and trace, and admission wraps correlation, so no
	// request runs any of it once the stop began.
	handler = admitRequests(c.run, correlateGatewayRequest(handler))

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
	if c.run.Stopping() {
		// A stop began while this Build constructed the gateway: publish
		// nothing.
		g.mu.Unlock()
		return http.ErrServerClosed
	}
	c.mux, c.srv, c.building = mux, server, false
	g.mu.Unlock()
	c.run.Release()
	published = true
	return nil
}

// gatewayLife is one construction of the gateway, from the Build that
// makes it to the stop that ends it: the mux and net/http server that
// Build made, the listener a Start bound, the run they serve in, and
// whether it served. Every unit of its work is admitted into run: the
// Build, the serve loop and each request (see admitRequests). A life a
// stop ended before it served is discarded, and the next Build starts
// another; a life that served stays the gateway's last, since net/http
// cannot serve a closed server again. Its fields are guarded by the
// gateway's mu.
type gatewayLife struct {
	run *drain.Run

	// building is set while the Build that makes this life runs.
	building bool
	// mux and srv are what the Build published, nil until it has.
	mux *runtime.ServeMux
	srv *http.Server
	// lis is the listener a Start bound, nil until it has.
	lis *serveListener
	// served is set while a Start has the serve loop admitted into run.
	served bool
	// running is set at the serve loop's first Accept.
	running bool
	// discarded is set once a life that never served has ended: the next
	// Build starts another.
	discarded bool

	// started is closed after the first Accept published the start, and
	// serveDone once Serve returned, with serveErr.
	started   chan struct{}
	serveDone chan struct{}
	serveErr  error
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
// starts a new life with the Build admitted into its run, and returns it
// with the plan to construct from. It returns a nil life with a nil error
// when the gateway is already built, and with an error when a Build is in
// progress or a check fails. It calls no application code.
func (g *Gateway) beginBuild() (*gatewayLife, *gatewayBuildPlan, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if c := g.cur; c != nil && !c.discarded {
		if c.building || (!c.served && c.run.Stopping()) {
			return nil, nil, ErrBuildInProgress
		}
		return nil, nil, nil // Already built
	}

	if g.configErr != nil {
		return nil, nil, g.configErr
	}

	// Enforce the production TLS guard before any other validation so the
	// error is unambiguous when an operator forgets to wire credentials.
	warnInsecure := false
	if !g.credsOpted {
		// Routed through contract.IsProductionEnv so "prod" and "staging"
		// are refused alongside "production". A typo'd APP_ENV cannot
		// silently downgrade the gateway to insecure dial credentials.
		if contract.IsProductionEnv(g.environment) {
			return nil, nil, fmt.Errorf("velocity/grpc: gateway TLS credentials are required in production. Use GatewayWithTLS, GatewayWithTransportConfig, or GatewayWithInsecure to opt out for a known-internal mesh")
		}
		warnInsecure = true
		g.dialOptions = []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}
	}

	if g.grpcEndpoint == "" {
		return nil, nil, ErrNoEndpoint
	}

	// Validate endpoint format (must be host:port)
	if _, _, err := net.SplitHostPort(g.grpcEndpoint); err != nil {
		return nil, nil, fmt.Errorf("velocity/grpc: invalid grpc endpoint %q: expected host:port format: %w", g.grpcEndpoint, err)
	}

	c := &gatewayLife{
		run:       g.own.NewRun(),
		building:  true,
		started:   make(chan struct{}),
		serveDone: make(chan struct{}),
	}
	c.run.Admit() // a new run admits: the Build is its first unit
	g.cur = c
	return c, &gatewayBuildPlan{
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

// abortBuild ends the Build of c without publishing it (a registration
// failed or panicked, or a stop began during it): c is discarded, so a
// later Build runs again, and when no stop began, the Build ends c's run
// itself.
func (g *Gateway) abortBuild(c *gatewayLife) {
	g.mu.Lock()
	c.building, c.discarded = false, true
	g.mu.Unlock()
	owner := c.run.Close()
	c.run.Release()
	if owner {
		c.run.Finish(nil)
	}
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

// StartWithContext builds and starts the HTTP gateway with a context. It
// binds the gateway's port, returning the error when it cannot, and
// serves until the gateway is stopped, returning http.ErrServerClosed
// then. A gateway a stop has ended does not start again: it returns
// http.ErrServerClosed, as does StartAsyncWithContext. A serve loop that
// fails stops the gateway, and StartWithContext returns that failure.
func (g *Gateway) StartWithContext(ctx context.Context) error {
	c, err := g.admitServe(ctx)
	if err != nil {
		return err
	}
	g.own.Do(func() { err = g.serve(c, false) })
	return err
}

// StartAsync builds and starts the HTTP gateway on a goroutine of its
// own; see StartAsyncWithContext.
func (g *Gateway) StartAsync() error {
	ctx := context.Background()
	return g.StartAsyncWithContext(ctx)
}

// StartAsyncWithContext builds the HTTP gateway, binds its port on the
// calling goroutine, returning the error when it cannot, and serves on a
// goroutine of its own. It returns once the gateway is taking
// connections: net/http has registered the listener and entered its first
// Accept, the gateway reports running and the starting line is written.
// When net/http refuses to serve because a stop came first, it returns
// http.ErrServerClosed and the gateway never reports running. A serve
// loop that fails later stops the gateway. Use Stop() or Shutdown() to
// stop the gateway.
func (g *Gateway) StartAsyncWithContext(ctx context.Context) error {
	c, err := g.admitServe(ctx)
	if err != nil {
		return err
	}
	g.own.Go(func() { _ = g.serve(c, true) })
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

// admitServe builds the gateway if it is not built, admits a serve loop
// into the current life's run and binds the gateway's port, returning the
// life to serve. It refuses a gateway that serves already, one a stop has
// ended, and one a rebuild is constructing. A bind that fails gives the
// admission back, so a later start may try again.
func (g *Gateway) admitServe(ctx context.Context) (*gatewayLife, error) {
	if err := g.Build(ctx); err != nil {
		return nil, err
	}
	g.mu.Lock()
	c := g.cur
	switch {
	case c == nil || c.discarded:
		g.mu.Unlock()
		return nil, http.ErrServerClosed
	case c.building:
		g.mu.Unlock()
		return nil, ErrBuildInProgress
	case c.served && !c.run.Stopping():
		g.mu.Unlock()
		return nil, ErrServerAlreadyRunning
	case c.srv == nil || !c.run.Admit():
		// A stop ended this gateway; net/http cannot serve it again.
		g.mu.Unlock()
		return nil, http.ErrServerClosed
	}
	c.served = true
	addr, raw := c.srv.Addr, g.providedListener
	g.mu.Unlock()

	var err error
	if raw == nil {
		raw, err = net.Listen("tcp", addr)
	}
	if err != nil {
		g.mu.Lock()
		c.served = false
		g.mu.Unlock()
		c.run.Release()
		return nil, fmt.Errorf("velocity/grpc: gateway failed to listen on %s: %w", addr, err)
	}
	lis := newServeListener(raw, func() { g.serving(c) }, g.logLine, "HTTP gateway")
	g.mu.Lock()
	c.lis = lis
	g.mu.Unlock()
	return c, nil
}

// serve runs c's serve loop, as the gateway's own work, and releases its
// unit of c's run when Serve returns. A loop that failed (its listener's
// Accept did, or net/http panicked) stops the gateway. A Serve a stop
// ended returns http.ErrServerClosed and needs nothing. logFailure writes
// the failure as an error line, for StartAsyncWithContext, whose caller
// has returned.
func (g *Gateway) serve(c *gatewayLife, logFailure bool) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("velocity/grpc: gateway serve loop panicked: %w", panicerr.FromRecovered(p))
		}
		c.serveErr = err
		close(c.serveDone)
		c.run.Release()
		if err == nil || errchain.Is(err, http.ErrServerClosed) {
			return
		}
		if logFailure {
			g.logLine(func(l contract.Logger) { l.Error("HTTP gateway error", "error", err) })
		}
		g.own.Signal(c.run, g.stopWork(c, false))
	}()
	return c.srv.Serve(c.lis)
}

// serving runs at the first Accept of c's serve loop. net/http calls it
// only once Serve has registered the listener, so from here on a stop
// closes it: the gateway is taking connections. It publishes that before
// StartAsyncWithContext returns: the gateway reports running and the
// starting line is written, on the serve loop's goroutine, as the
// gateway's own work.
func (g *Gateway) serving(c *gatewayLife) {
	defer close(c.started)
	g.mu.Lock()
	c.running = true
	addr, endpoint := g.addressLocked(c), g.grpcEndpoint
	g.mu.Unlock()
	g.logLine(func(l contract.Logger) {
		l.Info("HTTP gateway starting",
			"address", addr,
			"grpc_endpoint", endpoint,
		)
	})
}

// Stop stops the HTTP gateway immediately: it begins the gateway's stop
// (see Shutdown), closes the net/http server, which closes every
// connection and so cuts the requests in flight, and returns once the
// gateway has stopped accepting, its listener closed. It does not wait
// for the handlers in flight to return: a Shutdown returns nil only once
// they have. During a Shutdown it closes the gateway, cutting the
// requests that Shutdown is draining; during a Build it ends that Build
// (see Build) and returns at once. Stop logs and closes without holding
// the gateway's lock, so the logger may call the gateway's accessors.
// Called from the gateway's own work (a registration, a middleware
// constructor, the logger's lines), it begins the stop and closes the
// server on a goroutine of its own, and returns without waiting.
func (g *Gateway) Stop() {
	nested := g.own.Nested()
	c, srv, lis := g.signal(false)
	if srv == nil {
		return
	}
	closeServer := func() { _ = srv.Close() }
	if nested {
		g.own.Go(closeServer)
		return
	}
	g.own.Do(closeServer)
	if lis != nil {
		awaitClosed(lis, c.run.Finished())
	}
}

// Shutdown gracefully shuts down the HTTP gateway, or joins the stop
// already under way, and waits for it until ctx is done.
//
// Every stop of the gateway (Stop, Shutdown, and a failed serve loop) is
// one stop of the gateway's current run, which the first of them begins:
// the gateway stops accepting, a request that reaches it after is
// refused with 503 Service Unavailable ("server is stopping"), and the
// stop waits for the Build in progress, the serve loop and every request
// admitted before it began. Shutdown returns only then, and so does
// every Shutdown, overlapping or later, of the same run: nil, or the
// error net/http's Shutdown returned closing the listener. At ctx it
// returns the ctx error and closes the gateway, cutting the requests
// still in flight, once however many Shutdowns time out; a handler that
// ignores its request's context may still run after Shutdown returns.
// The stop and its line run on a goroutine of their own, so a logger that
// blocks cannot hold Shutdown past ctx.
//
// A Shutdown called from the gateway's own work (a registration, a
// middleware constructor, the logger's lines) cannot wait on the work it
// runs in: it begins the stop and returns at once an error wrapping
// contract.ErrStopFromOwnWork and http.ErrServerClosed, or the stop's
// result when it has finished. A Shutdown called from a request handler
// waits for that handler until its ctx is done.
func (g *Gateway) Shutdown(ctx context.Context) error {
	g.mu.RLock()
	c := g.cur
	g.mu.RUnlock()
	if c == nil {
		return nil
	}
	work := g.stopWork(c, true)
	if g.own.Nested() {
		g.own.Signal(c.run, work)
		err := g.own.Stop(ctx, c.run, nil, nil)
		if errchain.Is(err, contract.ErrStopFromOwnWork) {
			err = fmt.Errorf("velocity/grpc: %w: %w", err, http.ErrServerClosed)
		}
		return err
	}
	return g.own.Stop(ctx, c.run, work, func() {
		g.mu.RLock()
		srv := c.srv
		g.mu.RUnlock()
		if srv != nil {
			_ = srv.Close()
		}
	})
}

// signal begins the stop of the gateway's current life, when it has one
// and no stop began it, and returns that life with the server its Build
// published and the listener a Start bound: nil when there is none.
func (g *Gateway) signal(graceful bool) (*gatewayLife, *http.Server, *serveListener) {
	g.mu.RLock()
	c := g.cur
	g.mu.RUnlock()
	if c == nil {
		return nil, nil, nil
	}
	g.own.Signal(c.run, g.stopWork(c, graceful))
	// Read after the signal: a Build that publishes from here on saw the
	// stop, and publishes nothing.
	g.mu.RLock()
	defer g.mu.RUnlock()
	return c, c.srv, c.lis
}

// stopWork returns the stop of c, which the first stop of c's run runs as
// the gateway's own work, on a goroutine of its own
// (drain.Owner.Signal):
//
//  1. the stopping line, for a gateway that started;
//  2. the transport stop, for a gateway a Start admitted: net/http's
//     Shutdown (graceful), which closes the listener and waits for the
//     connections to go idle, or Close;
//  3. the wait for c's run to go idle: the Build, the serve loop and
//     every admitted request have returned.
//
// The run finishes with the Shutdown's error when the work returns, so a
// Shutdown returns only after all three. A stop that begins during a
// Build finds nothing published: the Build, seeing the stop, publishes
// nothing.
func (g *Gateway) stopWork(c *gatewayLife, graceful bool) func() error {
	return func() error {
		g.mu.RLock()
		srv, served, running := c.srv, c.served, c.running
		g.mu.RUnlock()
		if running {
			line := "HTTP gateway stopping"
			if graceful {
				line = "HTTP gateway gracefully shutting down"
			}
			g.logLine(func(l contract.Logger) { l.Info(line) })
		}
		var err error
		switch {
		case srv == nil || !served:
		case graceful:
			err = srv.Shutdown(context.Background())
		default:
			_ = srv.Close()
		}
		<-c.run.Idle()
		g.mu.Lock()
		if !c.served {
			c.discarded = true
		}
		lis := c.lis
		g.mu.Unlock()
		if lis != nil {
			lis.logClosePanic()
		}
		return err
	}
}

// logLine writes one Start or stop diagnostic through the gateway's
// logger with fallbacklog.Write: a logger that panics falls back and
// never skips the serve or teardown the line precedes.
func (g *Gateway) logLine(write func(contract.Logger)) {
	fallbacklog.Write(g.logger, write)
}

// publishedLocked returns the current life when its Build published it
// and a stop has not taken it before it served, nil otherwise. Caller
// holds g.mu.
func (g *Gateway) publishedLocked() *gatewayLife {
	c := g.cur
	if c == nil || c.srv == nil || (!c.served && c.run.Stopping()) {
		return nil
	}
	return c
}

// Address returns the address the gateway is listening on
func (g *Gateway) Address() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if c := g.publishedLocked(); c != nil {
		return g.addressLocked(c)
	}
	return ""
}

// addressLocked returns the address of c: its server's bind address, or
// for a caller's listener the address the listener reported when a Start
// took it. Caller holds g.mu.
func (g *Gateway) addressLocked(c *gatewayLife) string {
	if g.providedListener != nil && c.lis != nil {
		return c.lis.text
	}
	return c.srv.Addr
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

// IsRunning reports whether the gateway is taking connections: it has
// entered its first Accept and no stop has begun.
func (g *Gateway) IsRunning() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	c := g.cur
	return c != nil && c.running && !c.run.Stopping()
}

// Mux returns the underlying runtime.ServeMux.
// Returns nil if the gateway hasn't been built yet.
func (g *Gateway) Mux() *runtime.ServeMux {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if c := g.publishedLocked(); c != nil {
		return c.mux
	}
	return nil
}
