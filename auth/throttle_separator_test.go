package auth

import (
	"net/http"
	"strings"
	"testing"
)

// TestThrottleKey_PipeSeparatorWouldHaveCollided demonstrates the audit
// M-46 attack and that the current Unit-Separator-based key resists it.
//
// Pre-fix, the throttle key was built as "ident|ip" so a crafted
// identifier like "victim|198.51.100.1" submitted from RemoteAddr 10.0.0.1
// produced the same key as the legitimate ("victim", "198.51.100.1")
// pair. The attacker could therefore poison or share the victim's
// per-IP rate-limit bucket from a totally different network position.
//
// With the throttleKeySep switched to ASCII Unit Separator (0x1f) the
// two key-string inputs become "victim|198.51.100.1<US>10.0.0.1" vs
// "victim<US>198.51.100.1", which are distinct under SHA-256 and the
// keys MUST differ.
func TestThrottleKey_PipeSeparatorWouldHaveCollided(t *testing.T) {
	// Legitimate flow: victim@example.com hitting login from 198.51.100.1.
	rLegit, _ := http.NewRequest(http.MethodPost, "/login", nil)
	rLegit.RemoteAddr = "198.51.100.1:12345"
	keyLegit := ThrottleKey(rLegit, map[string]interface{}{
		"email": "victim@example.com",
	}, nil)

	// Crafted flow: attacker submits an identifier that embeds a pipe
	// and the victim's IP, hitting login from 10.0.0.1.
	rAttack, _ := http.NewRequest(http.MethodPost, "/login", nil)
	rAttack.RemoteAddr = "10.0.0.1:12345"
	keyAttack := ThrottleKey(rAttack, map[string]interface{}{
		"email": "victim@example.com|198.51.100.1",
	}, nil)

	if keyLegit == keyAttack {
		t.Fatalf("ThrottleKey collided under pipe-bearing identifier: %q == %q\nthis is exactly the M-46 attack the unit-separator fix is meant to close",
			keyLegit, keyAttack)
	}
}

// TestThrottleKey_UnitSeparatorBearingIdentifier covers the dual of the
// pipe case: even if an attacker crafts an identifier that already
// contains the chosen separator, the key MUST still differ from the
// legitimate (victim, attacker-ip) pair. Hashing protects against
// separator-injection regardless of the chosen byte; this test pins the
// invariant so a future tweak to the separator keeps the contract.
func TestThrottleKey_UnitSeparatorBearingIdentifier(t *testing.T) {
	rLegit, _ := http.NewRequest(http.MethodPost, "/login", nil)
	rLegit.RemoteAddr = "198.51.100.1:12345"
	keyLegit := ThrottleKey(rLegit, map[string]interface{}{
		"email": "victim@example.com",
	}, nil)

	rAttack, _ := http.NewRequest(http.MethodPost, "/login", nil)
	rAttack.RemoteAddr = "10.0.0.1:12345"
	keyAttack := ThrottleKey(rAttack, map[string]interface{}{
		"email": "victim@example.com\x1f198.51.100.1",
	}, nil)

	if keyLegit == keyAttack {
		t.Fatalf("ThrottleKey collided under unit-separator-bearing identifier: %q == %q",
			keyLegit, keyAttack)
	}
}

// TestNormaliseIdentifier_CapBytes pins the maxIdentifierBytes scheme
// from C-05: a multi-MB email field must NOT survive past the
// normalisation stage. With the cap in place the normalised string
// length is exactly maxIdentifierBytes so cache-store key amplification
// is bounded.
func TestNormaliseIdentifier_CapBytes(t *testing.T) {
	huge := strings.Repeat("a", 10_000_000)
	got := normaliseIdentifier(map[string]interface{}{"email": huge})
	if len(got) != maxIdentifierBytes {
		t.Fatalf("normaliseIdentifier did not cap; got %d bytes, want %d", len(got), maxIdentifierBytes)
	}
}
