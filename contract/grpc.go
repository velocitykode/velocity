package contract

// GRPCConfig is the configuration the grpc package's NewServer and
// NewGateway take. The grpc package never reads the environment: the root
// velocity.ConfigFromEnv fills this struct from GRPC_* / GATEWAY_PORT /
// GRPC_ENDPOINT / GRPC_INSECURE / APP_ENV, and a standalone caller fills
// it directly.
//
// The zero value of every field means the built-in default, so
// GRPCConfig{} is a valid, secure-by-default configuration: server port
// 50051, gateway port 8080, gateway endpoint localhost:50051, 4 MiB
// receive and send message sizes, reflection off, insecure off, and the
// "" environment (non-production, as IsProductionEnv decides).
type GRPCConfig struct {
	// ServerPort is the gRPC server port. Empty means "50051".
	ServerPort string

	// EnableReflection turns on gRPC server reflection. False (the zero
	// value) means off. The server refuses to build with reflection on in
	// a production environment.
	EnableReflection bool

	// MaxRecvMsgSize is the maximum receive message size in bytes. Zero
	// means the 4 MiB default. A negative value falls back to the default
	// and a value above the 1 GiB ceiling is clamped; either correction
	// records a warning.
	MaxRecvMsgSize int

	// MaxSendMsgSize is the maximum send message size in bytes, with the
	// same zero, negative and ceiling rules as MaxRecvMsgSize.
	MaxSendMsgSize int

	// GatewayPort is the HTTP gateway port. Empty means "8080".
	GatewayPort string

	// GRPCEndpoint is the gRPC endpoint the gateway dials. Empty means
	// "localhost:50051".
	GRPCEndpoint string

	// Environment is the deployment environment, classified by
	// IsProductionEnv. Empty means unset, which is non-production.
	Environment string

	// Insecure is the production TLS opt-out (the GRPC_INSECURE surface):
	// when true, a server in a production environment may start without
	// transport credentials, for a known-internal mTLS mesh or a
	// sidecar-terminated mesh. False (the zero value) means off.
	//
	// final: do not rename. GRPC_INSECURE is the 1.0 surface name for the
	// production TLS opt-out; downstream operators may already key off it.
	Insecure bool

	// Warnings holds operator-facing diagnostics produced while parsing and
	// normalising the configuration (e.g. a non-positive or oversize
	// message size that was corrected). NewServer logs these once a logger
	// exists, so a misconfiguration is never silent.
	Warnings []string
}
