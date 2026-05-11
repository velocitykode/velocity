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
//   - GRPC_MAX_RECV_SIZE: Max receive message size in bytes (default: 4MB)
//   - GRPC_MAX_SEND_SIZE: Max send message size in bytes (default: 4MB)
//   - GATEWAY_PORT: HTTP gateway port (default: 8080)
//   - GRPC_ENDPOINT: gRPC endpoint for gateway (default: localhost:50051)
func LoadConfig() *Config {
	return &Config{
		ServerPort:       envOr("GRPC_PORT", defaultGRPCPort),
		EnableReflection: envOr("GRPC_REFLECTION", "false") == "true",
		MaxRecvMsgSize:   envInt("GRPC_MAX_RECV_SIZE", defaultMaxMsgSize),
		MaxSendMsgSize:   envInt("GRPC_MAX_SEND_SIZE", defaultMaxMsgSize),
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

// envInt parses an environment variable as int with a default fallback.
func envInt(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultValue
}
