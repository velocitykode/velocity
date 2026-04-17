package grpc

import (
	"strconv"

	"github.com/velocitykode/velocity/config"
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
		ServerPort:       config.Get("GRPC_PORT", "50051"),
		EnableReflection: config.Get("GRPC_REFLECTION", "false") == "true",
		MaxRecvMsgSize:   getEnvInt("GRPC_MAX_RECV_SIZE", 4*1024*1024),
		MaxSendMsgSize:   getEnvInt("GRPC_MAX_SEND_SIZE", 4*1024*1024),
		GatewayPort:      config.Get("GATEWAY_PORT", "8080"),
		GRPCEndpoint:     config.Get("GRPC_ENDPOINT", "localhost:50051"),
	}
}

// getEnvInt gets an integer from environment or returns default
func getEnvInt(key string, defaultValue int) int {
	if val := config.Get(key, ""); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultValue
}
