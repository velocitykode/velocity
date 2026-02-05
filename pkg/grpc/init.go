package grpc

import (
	"os"
	"strconv"
	"sync"

	"github.com/velocitykode/velocity/pkg/config"
)

var (
	defaultServer  *Server
	defaultGateway *Gateway
	serverOnce     sync.Once
	gatewayOnce    sync.Once
	mu             sync.RWMutex
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
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultValue
}

// Initialize sets up the default server and gateway from environment.
// Call this at application startup if you want to use the global instances.
func Initialize() error {
	cfg := LoadConfig()

	// Initialize default server
	serverOnce.Do(func() {
		opts := []ServerOption{
			WithPort(cfg.ServerPort),
			WithReflection(cfg.EnableReflection),
		}

		if cfg.MaxRecvMsgSize > 0 {
			opts = append(opts, WithMaxRecvMsgSize(cfg.MaxRecvMsgSize))
		}
		if cfg.MaxSendMsgSize > 0 {
			opts = append(opts, WithMaxSendMsgSize(cfg.MaxSendMsgSize))
		}

		defaultServer = NewServer(opts...)
	})

	// Initialize default gateway
	gatewayOnce.Do(func() {
		defaultGateway = NewGateway(
			GatewayWithPort(cfg.GatewayPort),
			GatewayWithGRPCEndpoint(cfg.GRPCEndpoint),
		)
	})

	return nil
}

// GetServer returns the default gRPC server instance.
// Initializes from environment if not already initialized.
func GetServer() *Server {
	mu.RLock()
	if defaultServer != nil {
		defer mu.RUnlock()
		return defaultServer
	}
	mu.RUnlock()

	Initialize()
	return defaultServer
}

// GetGateway returns the default HTTP gateway instance.
// Initializes from environment if not already initialized.
func GetGateway() *Gateway {
	mu.RLock()
	if defaultGateway != nil {
		defer mu.RUnlock()
		return defaultGateway
	}
	mu.RUnlock()

	Initialize()
	return defaultGateway
}

// SetServer sets the default server instance.
// Useful for testing or custom initialization.
func SetServer(s *Server) {
	mu.Lock()
	defer mu.Unlock()
	defaultServer = s
}

// SetGateway sets the default gateway instance.
// Useful for testing or custom initialization.
func SetGateway(g *Gateway) {
	mu.Lock()
	defer mu.Unlock()
	defaultGateway = g
}

// Reset resets the global instances.
// Useful for testing.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	defaultServer = nil
	defaultGateway = nil
	serverOnce = sync.Once{}
	gatewayOnce = sync.Once{}
}
