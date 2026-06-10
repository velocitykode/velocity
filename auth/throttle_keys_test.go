package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/clientip"
)

func keysWithPrefix(keys []string, prefix string) []string {
	var out []string
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		// The pair prefix is a prefix of the id/ip prefixes; exclude
		// those when selecting pair keys.
		if prefix == ThrottleKeyPairPrefix &&
			(strings.HasPrefix(k, ThrottleKeyIdentifierPrefix) || strings.HasPrefix(k, ThrottleKeyIPPrefix)) {
			continue
		}
		out = append(out, k)
	}
	return out
}

func requestFrom(remoteAddr string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = remoteAddr
	return r
}

func TestThrottleKeys_ThreeDimensions(t *testing.T) {
	r := requestFrom("198.51.100.7:4242")
	creds := map[string]interface{}{"email": "victim@example.com"}

	keys := ThrottleKeys(r, creds, nil)
	if len(keys) != 3 {
		t.Fatalf("ThrottleKeys returned %d keys (%v), want 3", len(keys), keys)
	}
	if keys[0] != ThrottleKey(r, creds, nil) {
		t.Fatalf("pair key %q != ThrottleKey %q; pair dimension must stay identical to the legacy key", keys[0], ThrottleKey(r, creds, nil))
	}
	if got := keysWithPrefix(keys, ThrottleKeyIdentifierPrefix); len(got) != 1 {
		t.Fatalf("identifier-dimension keys = %v, want exactly 1", got)
	}
	if got := keysWithPrefix(keys, ThrottleKeyIPPrefix); len(got) != 1 {
		t.Fatalf("IP-dimension keys = %v, want exactly 1", got)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("duplicate key %q in %v", k, keys)
		}
		seen[k] = true
	}
}

// TestThrottleKeys_IdentifierKeyStableAcrossIPs is the distributed
// brute-force shape: one account, many source IPs. The identifier
// dimension must land in the same bucket for every IP while the pair
// and IP dimensions diverge.
func TestThrottleKeys_IdentifierKeyStableAcrossIPs(t *testing.T) {
	creds := map[string]interface{}{"email": "victim@example.com"}
	k1 := ThrottleKeys(requestFrom("10.0.0.1:1000"), creds, nil)
	k2 := ThrottleKeys(requestFrom("10.0.0.2:1000"), creds, nil)

	id1 := keysWithPrefix(k1, ThrottleKeyIdentifierPrefix)
	id2 := keysWithPrefix(k2, ThrottleKeyIdentifierPrefix)
	if id1[0] != id2[0] {
		t.Fatalf("identifier key varies by IP: %q vs %q", id1[0], id2[0])
	}
	if k1[0] == k2[0] {
		t.Fatal("pair key did not vary by IP")
	}
	ip1 := keysWithPrefix(k1, ThrottleKeyIPPrefix)
	ip2 := keysWithPrefix(k2, ThrottleKeyIPPrefix)
	if ip1[0] == ip2[0] {
		t.Fatal("IP key did not vary by IP")
	}
}

// TestThrottleKeys_IPKeyStableAcrossIdentifiers is the password-spray
// shape: one source IP, many accounts. The IP dimension must land in
// the same bucket for every identifier.
func TestThrottleKeys_IPKeyStableAcrossIdentifiers(t *testing.T) {
	k1 := ThrottleKeys(requestFrom("203.0.113.9:2000"), map[string]interface{}{"email": "alice@example.com"}, nil)
	k2 := ThrottleKeys(requestFrom("203.0.113.9:2000"), map[string]interface{}{"email": "bob@example.com"}, nil)

	ip1 := keysWithPrefix(k1, ThrottleKeyIPPrefix)
	ip2 := keysWithPrefix(k2, ThrottleKeyIPPrefix)
	if ip1[0] != ip2[0] {
		t.Fatalf("IP key varies by identifier: %q vs %q", ip1[0], ip2[0])
	}
	if k1[0] == k2[0] {
		t.Fatal("pair key did not vary by identifier")
	}
	id1 := keysWithPrefix(k1, ThrottleKeyIdentifierPrefix)
	id2 := keysWithPrefix(k2, ThrottleKeyIdentifierPrefix)
	if id1[0] == id2[0] {
		t.Fatal("identifier key did not vary by identifier")
	}
}

// TestThrottleKeys_NoIdentifier_OmitsIdentifierKey: with no recognisable
// identifier the identifier bucket would pool unrelated traffic, so it
// must be omitted rather than emitted empty.
func TestThrottleKeys_NoIdentifier_OmitsIdentifierKey(t *testing.T) {
	keys := ThrottleKeys(requestFrom("198.51.100.7:4242"), map[string]interface{}{"password": "x"}, nil)
	if len(keys) != 2 {
		t.Fatalf("ThrottleKeys = %v, want pair + IP only", keys)
	}
	if got := keysWithPrefix(keys, ThrottleKeyIdentifierPrefix); len(got) != 0 {
		t.Fatalf("identifier key emitted for identifier-less credentials: %v", got)
	}
	if got := keysWithPrefix(keys, ThrottleKeyIPPrefix); len(got) != 1 {
		t.Fatalf("IP-dimension keys = %v, want exactly 1", got)
	}
}

// TestThrottleKeys_NoResolvableIP_OmitsIPKey: nil request (no client IP)
// must omit the IP bucket for the same pooling reason.
func TestThrottleKeys_NoResolvableIP_OmitsIPKey(t *testing.T) {
	keys := ThrottleKeys(nil, map[string]interface{}{"email": "alice@example.com"}, nil)
	if len(keys) != 2 {
		t.Fatalf("ThrottleKeys = %v, want pair + identifier only", keys)
	}
	if got := keysWithPrefix(keys, ThrottleKeyIPPrefix); len(got) != 0 {
		t.Fatalf("IP key emitted with no resolvable client IP: %v", got)
	}
	if got := keysWithPrefix(keys, ThrottleKeyIdentifierPrefix); len(got) != 1 {
		t.Fatalf("identifier-dimension keys = %v, want exactly 1", got)
	}
}

// TestThrottleKeys_IdentifierNormalisation: the identifier dimension
// must apply the same case/whitespace/NFKC folding as the pair key so
// case-rotated spraying cannot split the per-identifier bucket.
func TestThrottleKeys_IdentifierNormalisation(t *testing.T) {
	canonical := keysWithPrefix(
		ThrottleKeys(requestFrom("10.1.1.1:1"), map[string]interface{}{"email": "victim@example.com"}, nil),
		ThrottleKeyIdentifierPrefix,
	)[0]
	for _, variant := range []string{"Victim@Example.com", "  victim@example.com  ", "VICTIM@EXAMPLE.COM"} {
		got := keysWithPrefix(
			ThrottleKeys(requestFrom("10.1.1.1:1"), map[string]interface{}{"email": variant}, nil),
			ThrottleKeyIdentifierPrefix,
		)[0]
		if got != canonical {
			t.Fatalf("identifier key for variant %q = %q, want %q", variant, got, canonical)
		}
	}
}

// TestThrottleKeys_IdentifierAndIPDimensionsNeverCollide: an identifier
// crafted to equal a victim's IP string must not share that IP's bucket
// (and vice versa); the dimension prefixes keep the hash spaces apart.
func TestThrottleKeys_IdentifierAndIPDimensionsNeverCollide(t *testing.T) {
	keys := ThrottleKeys(requestFrom("10.0.0.5:80"), map[string]interface{}{"username": "10.0.0.5"}, nil)
	idKey := keysWithPrefix(keys, ThrottleKeyIdentifierPrefix)[0]
	ipKey := keysWithPrefix(keys, ThrottleKeyIPPrefix)[0]
	if idKey == ipKey {
		t.Fatalf("identifier and IP dimensions collided on %q", idKey)
	}
}

// TestThrottleKeys_HonoursTrustedProxies: the IP dimension must use the
// proxy-resolved client IP, not the load balancer's, or every user
// behind the LB shares one spray bucket.
func TestThrottleKeys_HonoursTrustedProxies(t *testing.T) {
	trusted, err := clientip.ParseCIDRs([]string{"192.0.2.0/24"})
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}
	creds := map[string]interface{}{"email": "victim@example.com"}

	r1 := requestFrom("192.0.2.10:5000")
	r1.Header.Set("X-Forwarded-For", "198.51.100.1")
	r2 := requestFrom("192.0.2.10:5000")
	r2.Header.Set("X-Forwarded-For", "198.51.100.2")

	ip1 := keysWithPrefix(ThrottleKeys(r1, creds, trusted), ThrottleKeyIPPrefix)
	ip2 := keysWithPrefix(ThrottleKeys(r2, creds, trusted), ThrottleKeyIPPrefix)
	if ip1[0] == ip2[0] {
		t.Fatal("IP key ignored trusted-proxy forwarded client IPs")
	}

	// Without trust, both resolve to the proxy address and share a bucket.
	noTrust1 := keysWithPrefix(ThrottleKeys(r1, creds, nil), ThrottleKeyIPPrefix)
	noTrust2 := keysWithPrefix(ThrottleKeys(r2, creds, nil), ThrottleKeyIPPrefix)
	if noTrust1[0] != noTrust2[0] {
		t.Fatal("IP key honoured XFF without trusted proxies")
	}
}
