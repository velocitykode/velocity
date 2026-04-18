package drivers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestV1RoundTrip_CBC_AND_GCM asserts that encrypt->decrypt succeeds on
// both CBC and GCM for v1 payloads (the current wire format). This also
// pins the v1 sentinel on the wire so consumers written against v1 don't
// silently regress.
func TestV1RoundTrip_CBC_AND_GCM(t *testing.T) {
	cases := []struct {
		name   string
		cipher string
		key    []byte
	}{
		{"AES-128-CBC", "AES-128-CBC", []byte("0123456789abcdef")},
		{"AES-256-CBC", "AES-256-CBC", []byte("0123456789abcdef0123456789abcdef")},
		{"AES-128-GCM", "AES-128-GCM", []byte("0123456789abcdef")},
		{"AES-256-GCM", "AES-256-GCM", []byte("0123456789abcdef0123456789abcdef")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewAESDriver(tc.key, nil, tc.cipher)
			if err != nil {
				t.Fatalf("NewAESDriver: %v", err)
			}
			plaintext := "hello v1 wire format"
			payload, err := d.Encrypt(plaintext)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			if !strings.HasPrefix(payload, "v1:") {
				t.Fatalf("expected v1 sentinel prefix, got %q", payload)
			}
			got, err := d.Decrypt(payload)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			if got != plaintext {
				t.Fatalf("round trip mismatch: got %q, want %q", got, plaintext)
			}
		})
	}
}

// handCraftLegacyCBC produces a pre-sweep (v0) CBC payload using the exact
// MAC framing the framework emitted before the domain-separated sweep:
//
//	HMAC-SHA256(hmacKey, "base64:" + base64(value) + "." + base64(iv))
//
// This reproduces the bug-for-bug legacy format so the dual-read path can
// be exercised end-to-end.
func handCraftLegacyCBC(t *testing.T, d *AESDriver, plaintext []byte) string {
	t.Helper()

	// Reuse the driver's ciphertext generation: emit a v1 payload, strip
	// the sentinel to get the envelope, then reserialize with the legacy
	// MAC. This keeps iv/ct bytes identical to a real encrypt; only the
	// MAC framing differs.
	v1, err := d.Encrypt(string(plaintext))
	if err != nil {
		t.Fatalf("Encrypt seed: %v", err)
	}
	envelope := strings.TrimPrefix(v1, "v1:")
	data, err := base64.URLEncoding.DecodeString(envelope)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	// Overwrite the MAC with the legacy fmt-concatenated format, driven
	// by the driver's derived HMAC key.
	mac := hmac.New(sha256.New, d.hmacKey)
	mac.Write([]byte("base64:"))
	mac.Write([]byte(p.Value))
	mac.Write([]byte("."))
	mac.Write([]byte(p.IV))
	p.MAC = base64.StdEncoding.EncodeToString(mac.Sum(nil))

	out, err := json.Marshal(&p)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	// v0 payloads have no sentinel.
	return base64.URLEncoding.EncodeToString(out)
}

// TestV0LegacyRoundTrip_CBC confirms that a hand-crafted pre-sweep payload
// decrypts, fires the crypto.legacy_decrypt event exactly once per WARN
// (sync.Once gate), and dispatches the event on every legacy decrypt so
// operators can count the stream.
func TestV0LegacyRoundTrip_CBC(t *testing.T) {
	d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}

	var mu sync.Mutex
	var seen []string
	d.SetEventDispatcher(func(event interface{}) error {
		if e, ok := event.(*LegacyDecryptEvent); ok {
			mu.Lock()
			seen = append(seen, e.Name())
			mu.Unlock()
		}
		return nil
	})

	plaintext := []byte("sensitive legacy payload")
	legacy := handCraftLegacyCBC(t, d, plaintext)

	// First decrypt -> success + event.
	got, err := d.Decrypt(legacy)
	if err != nil {
		t.Fatalf("legacy Decrypt: %v", err)
	}
	if got != string(plaintext) {
		t.Fatalf("legacy round trip mismatch: got %q, want %q", got, plaintext)
	}

	// Second decrypt of the same legacy payload -> another event (count
	// is not gated by the once-per-instance log).
	if _, err := d.Decrypt(legacy); err != nil {
		t.Fatalf("second legacy Decrypt: %v", err)
	}

	mu.Lock()
	count := len(seen)
	mu.Unlock()
	if count != 2 {
		t.Fatalf("expected 2 legacy_decrypt events across 2 calls, got %d", count)
	}
	if seen[0] != "crypto.legacy_decrypt" {
		t.Fatalf("unexpected event name: %q", seen[0])
	}
}

// TestV1DoesNotAcceptV0_MAC mutates a v1 payload to swap in a legacy MAC
// and asserts decryption rejects it (v1 path must not accidentally accept
// the legacy MAC computation).
func TestV1DoesNotAcceptV0_MAC(t *testing.T) {
	d, _ := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
	v1, err := d.Encrypt("mixed-mac attack")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Decode and swap in a legacy MAC while keeping the v1 sentinel.
	envelope := strings.TrimPrefix(v1, "v1:")
	data, _ := base64.URLEncoding.DecodeString(envelope)
	var p Payload
	_ = json.Unmarshal(data, &p)

	mac := hmac.New(sha256.New, d.hmacKey)
	mac.Write([]byte("base64:"))
	mac.Write([]byte(p.Value))
	mac.Write([]byte("."))
	mac.Write([]byte(p.IV))
	p.MAC = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	out, _ := json.Marshal(&p)
	mixed := "v1:" + base64.URLEncoding.EncodeToString(out)

	if _, err := d.Decrypt(mixed); err == nil {
		t.Fatal("v1 decrypt must reject a legacy MAC smuggled under the v1 sentinel")
	}
}

// TestV0DoesNotAcceptV1_MAC does the reverse: take a legacy envelope but
// carry a v1 (domain-separated) MAC — this must also fail.
func TestV0DoesNotAcceptV1_MAC(t *testing.T) {
	d, _ := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")

	// Produce a v1 payload so we get an iv/ct pair + the v1 MAC.
	v1, err := d.Encrypt("mixed-mac attack 2")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	envelope := strings.TrimPrefix(v1, "v1:")
	// Use the v1 envelope AS a v0 envelope (no sentinel). v0 verifier will
	// compute the legacy MAC which differs from the v1 MAC still stored
	// in p.MAC.
	if _, err := d.Decrypt(envelope); err == nil {
		t.Fatal("v0 decrypt must reject a v1 MAC under the v0 (no-sentinel) format")
	}
}

// TestMalformedPayloads_AllReturnErrors exercises every malformed shape
// the spec calls out: nil, empty, truncated, wrong base64, wrong
// sentinel, correct sentinel but wrong MAC.
func TestMalformedPayloads_AllReturnErrors(t *testing.T) {
	d, _ := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")

	// Build a legitimate envelope we can truncate or surgically alter.
	v1, err := d.Encrypt("seed")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	envelope := strings.TrimPrefix(v1, "v1:")
	truncated := "v1:" + envelope[:len(envelope)-4]

	// Correct sentinel but corrupted MAC.
	data, _ := base64.URLEncoding.DecodeString(envelope)
	var p Payload
	_ = json.Unmarshal(data, &p)
	p.MAC = base64.StdEncoding.EncodeToString([]byte("garbagegarbagegarbagegarbagegarba"))
	bad, _ := json.Marshal(&p)
	wrongMAC := "v1:" + base64.URLEncoding.EncodeToString(bad)

	cases := []struct {
		name         string
		payload      string
		wantSubstr   string
		wantErrAtAll bool
	}{
		{"empty", "", "invalid payload format", true},
		{"wrong sentinel", "v2:" + envelope, "", true},
		{"truncated", truncated, "", true},
		{"wrong base64", "v1:!!!not-base64!!!", "invalid payload format", true},
		{"correct sentinel wrong mac", wrongMAC, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Decrypt(tc.payload)
			if tc.wantErrAtAll && err == nil {
				t.Fatalf("expected error, got nil")
			}
			// Every error must carry the velocity/crypto: prefix.
			if err != nil && !strings.HasPrefix(err.Error(), "velocity/crypto:") {
				t.Fatalf("error must be prefixed velocity/crypto: got %q", err.Error())
			}
			if tc.wantSubstr != "" && err != nil && !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q missing substring %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestLegacyEventFiresWithMultipleCalls verifies the dispatcher wiring is
// safe under concurrent use (SetEventDispatcher is mutex-protected). This
// guards against a concurrent-map/pointer-race regression if someone adds
// fields to AESDriver later.
func TestLegacyEventFiresWithMultipleCalls(t *testing.T) {
	d, _ := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")

	var count int32
	d.SetEventDispatcher(func(event interface{}) error {
		atomic.AddInt32(&count, 1)
		return nil
	})

	legacy := handCraftLegacyCBC(t, d, []byte("repeat me"))

	// Fire from multiple goroutines.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.Decrypt(legacy); err != nil {
				t.Errorf("legacy decrypt in goroutine: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&count); got != 10 {
		t.Fatalf("expected 10 legacy events across 10 decrypts, got %d", got)
	}
}

// TestDispatcher_NotSet_DoesNotPanic confirms the dispatcher is optional.
func TestDispatcher_NotSet_DoesNotPanic(t *testing.T) {
	d, _ := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")

	// No SetEventDispatcher call — decrypt a legacy payload and confirm
	// no panic / crash.
	legacy := handCraftLegacyCBC(t, d, []byte("quiet"))
	if _, err := d.Decrypt(legacy); err != nil {
		t.Fatalf("decrypt without dispatcher: %v", err)
	}
}

// TestV1NoLegacyEvent confirms that v1 decrypts never fire the legacy
// event, even when a dispatcher is wired up.
func TestV1NoLegacyEvent(t *testing.T) {
	d, _ := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")

	var count int32
	d.SetEventDispatcher(func(event interface{}) error {
		atomic.AddInt32(&count, 1)
		return nil
	})

	v1, err := d.Encrypt("new world")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := d.Decrypt(v1); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if got := atomic.LoadInt32(&count); got != 0 {
		t.Fatalf("v1 decrypt must not dispatch legacy event, got %d events", got)
	}
}

// TestV1AndV0_GCM_GCMIsVersionIndependent confirms that GCM-mode payloads
// round-trip across both versions. Since GCM integrity is cipher-provided,
// v0 and v1 GCM envelopes are decoded identically once the sentinel is
// stripped — this test pins that behaviour so changing the GCM path later
// doesn't quietly break legacy GCM cookies.
func TestV1AndV0_GCM_VersionIndependent(t *testing.T) {
	d, _ := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-GCM")

	v1, err := d.Encrypt("gcm")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// v0 form of the same GCM envelope: strip the sentinel.
	v0 := strings.TrimPrefix(v1, "v1:")

	for _, p := range []struct {
		name    string
		payload string
	}{
		{"v1", v1},
		{"v0", v0},
	} {
		t.Run(p.name, func(t *testing.T) {
			got, err := d.Decrypt(p.payload)
			if err != nil {
				t.Fatalf("Decrypt %s: %v", p.name, err)
			}
			if got != "gcm" {
				t.Fatalf("got %q, want %q", got, "gcm")
			}
		})
	}
}
