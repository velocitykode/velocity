package grpc

import (
	"fmt"

	"github.com/velocitykode/velocity/contract"
)

// Default values for gRPC configuration. Sourced once here so NewServer,
// NewGateway, and normalizeConfig agree on what a zero Config field means.
const (
	defaultGRPCPort     = "50051"
	defaultGatewayPort  = "8080"
	defaultGRPCEndpoint = "localhost:" + defaultGRPCPort
	defaultMaxMsgSize   = 4 * 1024 * 1024

	// maxMsgSizeCeil is an upper sanity bound for configurable gRPC message
	// sizes. A value far above this is almost certainly a misconfiguration
	// (and a memory-amplification vector), so the config and option paths
	// clamp down to it. 1 GiB is generous enough for any legitimate
	// large-message deployment while still bounding the absurd (e.g.
	// math.MaxInt).
	maxMsgSizeCeil = 1 << 30
)

// Config is the gRPC server and gateway configuration NewServer and
// NewGateway take. It is defined in contract (see contract.GRPCConfig) so
// the root velocity.ConfigFromEnv can fill it without importing grpc; the
// grpc package itself never reads the environment. The zero value of every
// field means the built-in default.
type Config = contract.GRPCConfig

// normalizeConfig returns cfg with every zero field replaced by its built-in
// default and every message size sanitized. Corrections an operator should
// know about are appended to the returned config's Warnings; cfg itself is
// not modified (Warnings is copied before appending).
//
// The floor and ceiling both matter for the message-size DoS guard: in
// grpc-go a negative MaxRecvMsgSize means UNLIMITED (silently removing the
// guard) and an absurdly large value is a memory-amplification vector. Zero
// means "use the default" and is corrected silently; a negative size falls
// back to the default and an oversize one clamps to maxMsgSizeCeil, each with
// a warning so the operator learns their value was overridden instead of it
// happening silently.
func normalizeConfig(cfg Config) Config {
	cfg.Warnings = append([]string(nil), cfg.Warnings...)
	if cfg.ServerPort == "" {
		cfg.ServerPort = defaultGRPCPort
	}
	if cfg.GatewayPort == "" {
		cfg.GatewayPort = defaultGatewayPort
	}
	if cfg.GRPCEndpoint == "" {
		cfg.GRPCEndpoint = defaultGRPCEndpoint
	}
	var w string
	if cfg.MaxRecvMsgSize, w = normalizeMsgSize("MaxRecvMsgSize", cfg.MaxRecvMsgSize); w != "" {
		cfg.Warnings = append(cfg.Warnings, w)
	}
	if cfg.MaxSendMsgSize, w = normalizeMsgSize("MaxSendMsgSize", cfg.MaxSendMsgSize); w != "" {
		cfg.Warnings = append(cfg.Warnings, w)
	}
	return cfg
}

// normalizeMsgSize sanitizes one Config message-size field, returning the
// value to use and an optional operator-facing warning naming the field.
func normalizeMsgSize(field string, size int) (int, string) {
	switch {
	case size == 0:
		return defaultMaxMsgSize, ""
	case size < 0:
		return defaultMaxMsgSize, fmt.Sprintf("velocity/grpc: %s=%d is not positive (grpc-go reads <=0 as unlimited, removing the message-size DoS guard); using the %d default", field, size, defaultMaxMsgSize)
	case size > maxMsgSizeCeil:
		return maxMsgSizeCeil, fmt.Sprintf("velocity/grpc: %s=%d exceeds the %d ceiling; clamping to the ceiling", field, size, maxMsgSizeCeil)
	default:
		return size, ""
	}
}

// clampMsgSize sanitizes an explicitly-configured message size the same way
// normalizeConfig does: non-positive falls back to the default, oversize
// clamps to the ceiling. Used by WithMaxRecvMsgSize / WithMaxSendMsgSize so
// the option path cannot bypass the floor that protects the message-size DoS
// guard.
func clampMsgSize(size int) int {
	if size <= 0 {
		return defaultMaxMsgSize
	}
	if size > maxMsgSizeCeil {
		return maxMsgSizeCeil
	}
	return size
}
