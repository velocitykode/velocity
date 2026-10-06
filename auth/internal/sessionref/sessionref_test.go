package sessionref

import (
	"strings"
	"testing"
)

func TestOf(t *testing.T) {
	const id = "q2dW0mYy3Jm1Yk1n4p8Q2b0m3o9w0c7m5h1Zr4t6u8A="
	ref := Of(id)
	if len(ref) != 12 {
		t.Fatalf("Of = %q, want 12 hex characters", ref)
	}
	if strings.Contains(id, ref) || strings.Contains(ref, id) {
		t.Fatalf("Of = %q carries the id", ref)
	}
	if Of(id) != ref {
		t.Fatal("Of is not stable for one id")
	}
	if Of(id+"x") == ref {
		t.Fatal("two ids share a reference")
	}
	if got := Of(""); got != "" {
		t.Fatalf("Of(\"\") = %q, want empty", got)
	}
}
