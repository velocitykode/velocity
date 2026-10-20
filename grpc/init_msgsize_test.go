package grpc

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeMsgSize(t *testing.T) {
	const def = defaultMaxMsgSize
	tests := []struct {
		name     string
		value    int
		want     int
		wantWarn bool
	}{
		{name: "zero uses default silently", value: 0, want: def, wantWarn: false},
		{name: "valid positive", value: 1048576, want: 1048576, wantWarn: false},
		{name: "negative falls back + warns", value: -1, want: def, wantWarn: true},
		{name: "oversize clamps + warns", value: 9999999999, want: maxMsgSizeCeil, wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warn := normalizeMsgSize("MaxRecvMsgSize", tt.value)
			if got != tt.want {
				t.Errorf("value = %d, want %d", got, tt.want)
			}
			if (warn != "") != tt.wantWarn {
				t.Errorf("warn = %q, wantWarn = %v", warn, tt.wantWarn)
			}
			if tt.wantWarn && !strings.Contains(warn, "MaxRecvMsgSize=") {
				t.Errorf("warn = %q, want it to name the MaxRecvMsgSize field", warn)
			}
		})
	}
}

func TestNormalizeConfig(t *testing.T) {
	got := normalizeConfig(Config{})
	if got.ServerPort != defaultGRPCPort || got.GatewayPort != defaultGatewayPort || got.GRPCEndpoint != defaultGRPCEndpoint {
		t.Errorf("zero Config ports/endpoint = %q/%q/%q, want defaults", got.ServerPort, got.GatewayPort, got.GRPCEndpoint)
	}
	if got.MaxRecvMsgSize != defaultMaxMsgSize || got.MaxSendMsgSize != defaultMaxMsgSize {
		t.Errorf("zero Config sizes = %d/%d, want default %d", got.MaxRecvMsgSize, got.MaxSendMsgSize, defaultMaxMsgSize)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("zero Config warnings = %v, want none", got.Warnings)
	}

	in := Config{MaxRecvMsgSize: -5, MaxSendMsgSize: maxMsgSizeCeil + 1, Warnings: []string{"prior"}}
	got = normalizeConfig(in)
	want := []string{
		"prior",
		"velocity/grpc: MaxRecvMsgSize=-5 is not positive (grpc-go reads <=0 as unlimited, removing the message-size DoS guard); using the 4194304 default",
		fmt.Sprintf("velocity/grpc: MaxSendMsgSize=%d exceeds the %d ceiling; clamping to the ceiling", maxMsgSizeCeil+1, maxMsgSizeCeil),
	}
	if !reflect.DeepEqual(got.Warnings, want) {
		t.Errorf("warnings = %q, want %q", got.Warnings, want)
	}
	if len(in.Warnings) != 1 {
		t.Errorf("normalizeConfig mutated the caller's Warnings: %v", in.Warnings)
	}
}

func TestClampMsgSize(t *testing.T) {
	if got := clampMsgSize(0); got != defaultMaxMsgSize {
		t.Errorf("clampMsgSize(0) = %d, want default %d", got, defaultMaxMsgSize)
	}
	if got := clampMsgSize(-5); got != defaultMaxMsgSize {
		t.Errorf("clampMsgSize(-5) = %d, want default %d", got, defaultMaxMsgSize)
	}
	if got := clampMsgSize(1024); got != 1024 {
		t.Errorf("clampMsgSize(1024) = %d, want 1024", got)
	}
	if got := clampMsgSize(maxMsgSizeCeil + 1); got != maxMsgSizeCeil {
		t.Errorf("clampMsgSize(ceil+1) = %d, want ceil %d", got, maxMsgSizeCeil)
	}
}
