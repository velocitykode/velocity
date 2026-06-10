package redis

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// captureSlog swaps the default slog logger for one writing to the returned
// buffer and restores the original when the test ends.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestIsLoopbackHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"127.0.0.1", true},
		{"127.255.0.3", true},
		{"::1", true},
		{"redis.internal", false},
		{"10.0.0.5", false},
		{"192.168.1.20", false},
		{"2001:db8::1", false},
		{"example.com", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isLoopbackHost(tt.host); got != tt.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestWarnIfInsecure(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		password    string
		tlsEnabled  bool
		wantTLSWarn bool
		wantPwdWarn bool
	}{
		{"remote no TLS no password", "redis.internal", "", false, true, true},
		{"remote no TLS with password", "redis.internal", "secret", false, true, false},
		{"remote TLS no password", "redis.internal", "", true, false, true},
		{"remote TLS with password", "redis.internal", "secret", true, false, false},
		{"loopback IP no TLS no password", "127.0.0.1", "", false, false, false},
		{"localhost no TLS no password", "localhost", "", false, false, false},
		{"IPv6 loopback no TLS no password", "::1", "", false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := captureSlog(t)
			warnIfInsecure(tt.host, tt.password, tt.tlsEnabled)
			out := buf.String()
			if got := strings.Contains(out, "without TLS"); got != tt.wantTLSWarn {
				t.Errorf("TLS warning present = %v, want %v; log output:\n%s", got, tt.wantTLSWarn, out)
			}
			if got := strings.Contains(out, "without a password"); got != tt.wantPwdWarn {
				t.Errorf("password warning present = %v, want %v; log output:\n%s", got, tt.wantPwdWarn, out)
			}
		})
	}
}

// TestNewRedisStore_LoopbackSilent verifies that connecting to a loopback
// Redis without TLS or password emits no insecure-connection warning.
func TestNewRedisStore_LoopbackSilent(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	buf := captureSlog(t)
	store, err := NewRedisStore(context.Background(), "warncheck", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	defer store.Shutdown(context.Background())

	out := buf.String()
	if strings.Contains(out, "without TLS") || strings.Contains(out, "without a password") {
		t.Errorf("unexpected insecure-connection warning for loopback host; log output:\n%s", out)
	}
}
