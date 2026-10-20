package queue

import (
	"fmt"
	"strings"
	"testing"
)

// Config's String, and %v / %+v / Sprint through it, never carry the
// signing key or the Redis password.
func TestQueueConfigString_RedactsSecrets(t *testing.T) {
	const signingKey, password = "signing-key-secret", "redis-password-secret"
	cfg := Config{
		Driver:     "redis",
		Redis:      RedisConfig{Host: "queue.internal", Port: "6390", Password: password, DB: "2"},
		SigningKey: signingKey,
	}
	for name, out := range map[string]string{
		"String": cfg.String(),
		"%v":     fmt.Sprintf("%v", cfg),
		"%+v":    fmt.Sprintf("%+v", cfg),
		"Sprint": fmt.Sprint(cfg),
		"%v ptr": fmt.Sprintf("%v", &cfg),
	} {
		if strings.Contains(out, signingKey) || strings.Contains(out, password) {
			t.Errorf("%s leaks a secret: %q", name, out)
		}
		if !strings.Contains(out, "queue.internal") || !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s = %q, want the host and a [REDACTED] marker", name, out)
		}
	}
}

// An empty signing key prints empty rather than [REDACTED].
func TestQueueConfigString_EmptySigningKey(t *testing.T) {
	out := Config{Driver: "memory"}.String()
	if !strings.Contains(out, "SigningKey:,") {
		t.Fatalf("String = %q, want an empty SigningKey", out)
	}
}
