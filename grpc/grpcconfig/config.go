package grpcconfig

// Config is the configuration grpc.NewServer and grpc.NewGateway take. The
// grpc package never reads the environment: the root velocity.ConfigFromEnv
// fills this struct from GRPC_*, GATEWAY_PORT and APP_ENV, and a standalone
// caller fills it directly.
//
// The zero value of every field means the built-in default, so Config{} is
// a valid, secure-by-default configuration: server port 50051, gateway port
// 8080, gateway endpoint localhost:50051, 4 MiB receive and send message
// sizes, reflection off, insecure off, and the "" environment
// (non-production, as IsProductionEnv decides).
type Config struct {
	// EnableReflection turns on gRPC server reflection (GRPC_REFLECTION,
	// default off). The server refuses to build with reflection on in a
	// production environment.
	EnableReflection bool

	// Environment is the deployment environment, classified by
	// IsProductionEnv (APP_ENV, empty means unset, which is non-production).
	Environment string

	// GRPCEndpoint is the gRPC endpoint the gateway dials (GRPC_ENDPOINT,
	// default "localhost:50051").
	GRPCEndpoint string

	// GatewayPort is the HTTP gateway port (GATEWAY_PORT, default "8080").
	GatewayPort string

	// Insecure is the production TLS opt-out: when true, a server in a
	// production environment may start without transport credentials, for
	// a known-internal mTLS mesh or a sidecar-terminated mesh
	// (GRPC_INSECURE, default off).
	//
	// final: do not rename. GRPC_INSECURE is the 1.0 surface name for the
	// production TLS opt-out; downstream operators may already key off it.
	Insecure bool

	// MaxRecvMsgSize is the maximum receive message size in bytes
	// (GRPC_MAX_RECV_SIZE, default 4 MiB). A negative value falls back
	// to the default and a value above the 1 GiB ceiling is clamped; either
	// correction records a warning.
	MaxRecvMsgSize int

	// MaxSendMsgSize is the maximum send message size in bytes, with the
	// same zero, negative and ceiling rules as MaxRecvMsgSize
	// (GRPC_MAX_SEND_SIZE, default 4 MiB).
	MaxSendMsgSize int

	// ServerPort is the gRPC server port (GRPC_PORT, default "50051").
	ServerPort string

	// Warnings holds operator-facing diagnostics produced while parsing and
	// normalising the configuration, such as a non-positive or oversize
	// message size that was corrected. NewServer logs these once a logger
	// exists, so a misconfiguration is never silent.
	Warnings []string
}
