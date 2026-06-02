package grpc

import (
	"fmt"
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

	// maxMsgSizeCeil is an upper sanity bound for configurable gRPC message
	// sizes. A value far above this is almost certainly a misconfiguration
	// (and a memory-amplification vector), so the env and option paths clamp
	// down to it. 1 GiB is generous enough for any legitimate large-message
	// deployment while still bounding the absurd (e.g. math.MaxInt).
	maxMsgSizeCeil = 1 << 30
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

	// Warnings accumulates operator-facing diagnostics produced while parsing
	// the environment (e.g. a non-positive or unparseable message-size value
	// that was silently clamped). NewServer logs these once a logger is
	// available so a misconfiguration is never silent.
	Warnings []string
}

// LoadConfig loads gRPC configuration from environment variables:
//   - GRPC_PORT: gRPC server port (default: 50051)
//   - GRPC_REFLECTION: Enable reflection (default: false)
//   - GRPC_MAX_RECV_SIZE: Max receive message size in bytes (default: 4MB).
//     Non-positive or unparseable values fall back to the 4MB default; values
//     above the 1 GiB ceiling are clamped. Either case records a warning.
//   - GRPC_MAX_SEND_SIZE: Max send message size in bytes (default: 4MB).
//     Same clamping/warning behaviour as GRPC_MAX_RECV_SIZE.
//   - GATEWAY_PORT: HTTP gateway port (default: 8080)
//   - GRPC_ENDPOINT: gRPC endpoint for gateway (default: localhost:50051)
func LoadConfig() *Config {
	recv, recvWarn := msgSizeFromEnv("GRPC_MAX_RECV_SIZE", defaultMaxMsgSize)
	send, sendWarn := msgSizeFromEnv("GRPC_MAX_SEND_SIZE", defaultMaxMsgSize)

	cfg := &Config{
		ServerPort:       envOr("GRPC_PORT", defaultGRPCPort),
		EnableReflection: envOr("GRPC_REFLECTION", "false") == "true",
		MaxRecvMsgSize:   recv,
		MaxSendMsgSize:   send,
		GatewayPort:      envOr("GATEWAY_PORT", defaultGatewayPort),
		GRPCEndpoint:     envOr("GRPC_ENDPOINT", defaultGRPCEndpoint),
	}
	for _, w := range []string{recvWarn, sendWarn} {
		if w != "" {
			cfg.Warnings = append(cfg.Warnings, w)
		}
	}
	return cfg
}

// envOr returns os.Getenv(key) when non-empty, otherwise fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// msgSizeFromEnv parses an environment variable as a gRPC message-size limit,
// returning the sanitized value and an optional operator-facing warning.
//
// The floor and ceiling both matter for the message-size DoS guard: in grpc-go
// a negative MaxRecvMsgSize means UNLIMITED (silently removing the guard), 0 is
// nonsensical, and an absurdly large value is a memory-amplification vector.
// Unset/unparseable/non-positive fall back to defaultValue; oversize clamps to
// maxMsgSizeCeil. Either correction returns a non-empty warning so the operator
// learns their value was overridden instead of it happening silently.
func msgSizeFromEnv(key string, defaultValue int) (int, string) {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue, ""
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return defaultValue, fmt.Sprintf("velocity/grpc: %s=%q is not an integer; using the %d default", key, v, defaultValue)
	}
	if i <= 0 {
		return defaultValue, fmt.Sprintf("velocity/grpc: %s=%d is non-positive (grpc-go reads <=0 as unlimited, removing the message-size DoS guard); using the %d default", key, i, defaultValue)
	}
	if i > maxMsgSizeCeil {
		return maxMsgSizeCeil, fmt.Sprintf("velocity/grpc: %s=%d exceeds the %d ceiling; clamping to the ceiling", key, i, maxMsgSizeCeil)
	}
	return i, ""
}

// clampMsgSize sanitizes an explicitly-configured message size the same way the
// env path does: non-positive falls back to the default, oversize clamps to the
// ceiling. Used by WithMaxRecvMsgSize / WithMaxSendMsgSize so the option path
// cannot bypass the floor that protects the message-size DoS guard.
func clampMsgSize(size int) int {
	if size <= 0 {
		return defaultMaxMsgSize
	}
	if size > maxMsgSizeCeil {
		return maxMsgSizeCeil
	}
	return size
}
