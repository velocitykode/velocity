package drivers

import (
	"strings"
	"testing"
)

// BenchmarkSecureCompare measures the cost of the constant-time string
// comparison used for HMAC verification. Skipped under -short.
func BenchmarkSecureCompare(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping in -short")
	}
	a := strings.Repeat("a", 64)
	bb := strings.Repeat("a", 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = secureCompare(a, bb)
	}
}

// TestSecureCompare_RejectsDifferentLengths guards the constant-time path
// against a panic on mismatched lengths.
func TestSecureCompare_RejectsDifferentLengths(t *testing.T) {
	if secureCompare("short", strings.Repeat("a", 32)) {
		t.Error("different-length inputs must not compare equal")
	}
}
