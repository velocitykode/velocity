package auth

import (
	"net/http"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/clientip"
)

func TestThrottleKey_SameAcrossEphemeralPorts(t *testing.T) {
	// Regression for C-05: two TCP connections from the same client
	// IP previously produced distinct throttle keys because r.RemoteAddr
	// includes the ephemeral port. ThrottleKey must strip the port so
	// the limiter actually accumulates failures per client.
	credentials := map[string]interface{}{"email": "victim@example.com", "password": "guess"}

	r1, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r1.RemoteAddr = "203.0.113.5:54321"

	r2, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r2.RemoteAddr = "203.0.113.5:60001"

	k1 := ThrottleKey(r1, credentials, nil)
	k2 := ThrottleKey(r2, credentials, nil)
	if k1 != k2 {
		t.Fatalf("port-only diff produced different keys:\nk1=%q\nk2=%q", k1, k2)
	}
	if k1 == "" {
		t.Fatalf("empty key")
	}
}

func TestThrottleKey_DifferentRemoteIPs_DifferentKeys(t *testing.T) {
	credentials := map[string]interface{}{"email": "victim@example.com"}

	r1, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r1.RemoteAddr = "203.0.113.5:54321"

	r2, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r2.RemoteAddr = "198.51.100.42:54321"

	if a, b := ThrottleKey(r1, credentials, nil), ThrottleKey(r2, credentials, nil); a == b {
		t.Fatalf("different IPs collided: %q == %q", a, b)
	}
}

func TestThrottleKey_IdentifierCaseAndWhitespace(t *testing.T) {
	// Regression for the identifier-normalisation half of C-05: case
	// rotation and surrounding whitespace must hash to the same key
	// so attackers cannot bypass the limiter by alternating case.
	r, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.5:54321"

	canonical := ThrottleKey(r, map[string]interface{}{"email": "victim@example.com"}, nil)

	variants := []map[string]interface{}{
		{"email": "VICTIM@EXAMPLE.COM"},
		{"email": "Victim@Example.Com"},
		{"email": "  victim@example.com  "},
		{"email": "\tvictim@example.com\n"},
	}
	for _, v := range variants {
		got := ThrottleKey(r, v, nil)
		if got != canonical {
			t.Errorf("variant %v produced different key %q (canonical %q)", v, got, canonical)
		}
	}
}

func TestThrottleKey_IdentifierNFKC(t *testing.T) {
	// Halfwidth/fullwidth digits and Roman-letter compatibility forms
	// normalise under NFKC. The throttle key must collapse them so
	// confusable identifiers share a bucket.
	r, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.5:54321"

	// "ＡＢＣ" (fullwidth) -> "ABC" (lowered to "abc") under NFKC.
	canonical := ThrottleKey(r, map[string]interface{}{"username": "abc"}, nil)
	got := ThrottleKey(r, map[string]interface{}{"username": "ＡＢＣ"}, nil)
	if got != canonical {
		t.Errorf("fullwidth variant produced different key: %q != %q", got, canonical)
	}
}

func TestThrottleKey_IdentifierFullwidthAtSign(t *testing.T) {
	// All four of these variants must collapse to one throttle bucket.
	// The fullwidth at-sign ("＠", U+FF20) is the load-bearing case: an
	// attacker rotating between halfwidth and fullwidth "@" in the local
	// part would otherwise get one bucket per encoding while the user
	// provider's UTF-8 collation resolves them to one account.
	r, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.5:54321"

	canonical := ThrottleKey(r, map[string]interface{}{"email": "victim@example.com"}, nil)
	variants := []map[string]interface{}{
		{"email": "Victim@Example.com"},
		{"email": "VICTIM@example.com"},
		{"email": " VICTIM@example.com "},
		{"email": "Victim＠Example.com"}, // fullwidth @
	}
	for _, v := range variants {
		got := ThrottleKey(r, v, nil)
		if got != canonical {
			t.Errorf("variant %v produced different key %q (canonical %q)", v, got, canonical)
		}
	}
}

func TestThrottleKey_DifferentIdentifiersDifferentKeys(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.5:54321"

	a := ThrottleKey(r, map[string]interface{}{"email": "alice@example.com"}, nil)
	b := ThrottleKey(r, map[string]interface{}{"email": "bob@example.com"}, nil)
	if a == b {
		t.Fatalf("different identifiers collided: %q == %q", a, b)
	}
}

func TestThrottleKey_PipeBearingIdentifier_NoCollision(t *testing.T) {
	// Pre-fix, the legacy key format was "<ident>|<ip>" so an identifier
	// containing "|" could collide with another (ident, ip) pair. SHA-256
	// of (ident || NUL || ip) closes this. Pin it.
	r1, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r1.RemoteAddr = "10.0.0.5:54321"

	r2, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r2.RemoteAddr = "10.0.0.5:54321"

	a := ThrottleKey(r1, map[string]interface{}{"email": "alice|10.0.0.5"}, nil)
	b := ThrottleKey(r2, map[string]interface{}{"email": "alice"}, nil)
	if a == b {
		t.Fatalf("pipe-bearing identifier collided with another bucket: %q == %q", a, b)
	}
}

func TestThrottleKey_HonoursTrustedProxies(t *testing.T) {
	// Behind a load balancer (RemoteAddr = LB), without trusted-proxy
	// resolution every request shares one bucket (the LB IP). With
	// resolution, the real client IP is honoured.
	trusted, err := clientip.ParseCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}

	r1, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r1.RemoteAddr = "10.0.0.1:443"
	r1.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.2")

	r2, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r2.RemoteAddr = "10.0.0.1:443"
	r2.Header.Set("X-Forwarded-For", "198.51.100.42, 10.0.0.2")

	creds := map[string]interface{}{"email": "victim@example.com"}

	// Without trusted proxies: both requests resolve to the LB IP and
	// therefore SHARE a bucket.
	noTrustA := ThrottleKey(r1, creds, nil)
	noTrustB := ThrottleKey(r2, creds, nil)
	if noTrustA != noTrustB {
		t.Errorf("without trusted proxies expected same bucket (LB IP): a=%q b=%q", noTrustA, noTrustB)
	}

	// With trusted proxies: distinct real clients get distinct buckets.
	withTrustA := ThrottleKey(r1, creds, trusted)
	withTrustB := ThrottleKey(r2, creds, trusted)
	if withTrustA == withTrustB {
		t.Errorf("with trusted proxies expected distinct buckets, got %q == %q", withTrustA, withTrustB)
	}
}

func TestThrottleKey_IdentifierLengthCap(t *testing.T) {
	// Excessively long identifier (would otherwise bloat cache keys
	// and amplify memory cost). Output is fixed-size hash so any
	// truncation we apply BEFORE hashing is invisible at the output
	// layer; assert the output is bounded.
	r, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.5:54321"

	huge := strings.Repeat("a", 10_000_000)
	key := ThrottleKey(r, map[string]interface{}{"email": huge}, nil)
	// Fixed: "login:" + 16 hex chars = 22 bytes total.
	if got, want := len(key), 22; got != want {
		t.Errorf("len(key) = %d, want %d", got, want)
	}
	if !strings.HasPrefix(key, "login:") {
		t.Errorf("missing login: prefix: %q", key)
	}
}

func TestThrottleKey_NoIdentifier_StillVariesByIP(t *testing.T) {
	r1, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r1.RemoteAddr = "203.0.113.5:54321"

	r2, _ := http.NewRequest(http.MethodPost, "/login", nil)
	r2.RemoteAddr = "198.51.100.42:54321"

	// No "email"/"username"/"name"/"login" present in credentials.
	a := ThrottleKey(r1, map[string]interface{}{"password": "x"}, nil)
	b := ThrottleKey(r2, map[string]interface{}{"password": "x"}, nil)
	if a == b {
		t.Fatalf("no-ident: different IPs collided: %q == %q", a, b)
	}
}

func TestThrottleKey_NilRequest(t *testing.T) {
	// nil request must not panic; key depends only on the (empty) ident
	// and the (empty) IP.
	key := ThrottleKey(nil, map[string]interface{}{"email": "alice@example.com"}, nil)
	if !strings.HasPrefix(key, "login:") || len(key) != 22 {
		t.Fatalf("unexpected key shape: %q", key)
	}
}
