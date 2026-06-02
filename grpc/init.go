package grpc

import (
	"os"
	"strconv"
)

// Default values for gRPC configuration. Sourced once here so NewServer,
// NewGateway, and LoadConfig agree on what "no env, no option" means.
const (
	defaultGRPCPort     = "50051"
	defaultGatewayPort  = "8080"
	defaultGRPCEndpoint = "localhost:" + defaultGRPCPort
	defaultMaxMsgSize   = 4 * 1024 * 1024
)

// Config holds gRPC configuration loaded from environment
type Config struct {
	// Server configuration
	ServerPort       string
	EnableReflection bool
	MaxRecvMsgSize   int
	MaxSendMsgSize   int

	// Gateway configuration
	GatewayPort  string
	GRPCEndpoint string
}

// LoadConfig loads gRPC configuration from environment variables:
//   - GRPC_PORT: gRPC server port (default: 50051)
//   - GRPC_REFLECTION: Enable reflection (default: false)
//   - GRPC_MAX_RECV_SIZE: Max receive message size in bytes (default: 4MB).
//     Non-positive or unparseable values fall back to the 4MB default.
//   - GRPC_MAX_SEND_SIZE: Max send message size in bytes (default: 4MB).
//     Non-positive or unparseable values fall back to the 4MB default.
//   - GATEWAY_PORT: HTTP gateway port (default: 8080)
//   - GRPC_ENDPOINT: gRPC endpoint for gateway (default: localhost:50051)
func LoadConfig() *Config {
	return &Config{
		ServerPort:       envOr("GRPC_PORT", defaultGRPCPort),
		EnableReflection: envOr("GRPC_REFLECTION", "false") == "true",
		MaxRecvMsgSize:   envPositiveInt("GRPC_MAX_RECV_SIZE", defaultMaxMsgSize),
		MaxSendMsgSize:   envPositiveInt("GRPC_MAX_SEND_SIZE", defaultMaxMsgSize),
		GatewayPort:      envOr("GATEWAY_PORT", defaultGatewayPort),
		GRPCEndpoint:     envOr("GRPC_ENDPOINT", defaultGRPCEndpoint),
	}
}

// envOr returns os.Getenv(key) when non-empty, otherwise fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envPositiveInt parses an environment variable as a positive int. It returns
// defaultValue when the variable is unset, unparseable, or non-positive (<= 0).
// The positive floor matters for message-size limits: in grpc-go a negative
// MaxRecvMsgSize means UNLIMITED, which would silently remove the message-size
// DoS guard, and 0 is nonsensical. Falling back to the default preserves that
// protection against operator misconfiguration.
func envPositiveInt(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 {
			return i
		}
	}
	return defaultValue
}
