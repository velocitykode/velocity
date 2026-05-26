package router

import (
	"errors"
	"net"
	"testing"
)

func TestParseTrustedProxies_Valid(t *testing.T) {
	tp, err := ParseTrustedProxies([]string{
		"10.0.0.1",
		"192.168.0.0/16",
		"::1",
		"2001:db8::/32",
	})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	if got, want := tp.Len(), 4; got != want {
		t.Errorf("Len = %d, want %d", got, want)
	}
	if !tp.Contains("10.0.0.1") {
		t.Error("expected 10.0.0.1 to be trusted")
	}
	if !tp.Contains("192.168.42.7") {
		t.Error("expected 192.168.42.7 to match /16")
	}
	if !tp.Contains("2001:db8::1") {
		t.Error("expected 2001:db8::1 to match ::/32")
	}
	if tp.Contains("8.8.8.8") {
		t.Error("8.8.8.8 must not be trusted")
	}
	if tp.Contains("not-an-ip") {
		t.Error("garbage input must not be trusted")
	}
}

func TestParseTrustedProxies_InvalidFailsFast(t *testing.T) {
	cases := []struct {
		name  string
		entry string
	}{
		{"empty entry", ""},
		{"bad cidr", "10.0.0.0/99"},
		{"garbage", "not-an-ip"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTrustedProxies([]string{tc.entry})
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.entry)
			}
			if !errors.Is(err, ErrInvalidTrustedProxy) {
				t.Errorf("err = %v, want wrap of ErrInvalidTrustedProxy", err)
			}
		})
	}
}

func TestParseTrustedProxies_EmptyIsOk(t *testing.T) {
	tp, err := ParseTrustedProxies(nil)
	if err != nil {
		t.Fatalf("nil entries returned error: %v", err)
	}
	if tp.Len() != 0 {
		t.Errorf("expected empty set, got Len=%d", tp.Len())
	}
	if tp.Contains("10.0.0.1") {
		t.Error("empty set must not trust anything")
	}
}

func TestTrustedProxies_ClientIP_Table(t *testing.T) {
	ipv4, _ := ParseTrustedProxies([]string{"10.0.0.0/8"})
	ipv6, _ := ParseTrustedProxies([]string{"2001:db8::/32"})

	cases := []struct {
		name     string
		trusted  *TrustedProxies
		remote   string
		xff      string
		expected string
	}{
		{
			name:     "no trust returns remote verbatim",
			trusted:  &TrustedProxies{},
			remote:   "203.0.113.5",
			xff:      "1.2.3.4, 5.6.7.8",
			expected: "203.0.113.5",
		},
		{
			name:     "ipv4 trust returns right-most untrusted",
			trusted:  ipv4,
			remote:   "10.0.0.1",
			xff:      "203.0.113.1, 10.0.0.2, 10.0.0.3",
			expected: "203.0.113.1",
		},
		{
			name:     "ipv6 trust returns right-most untrusted",
			trusted:  ipv6,
			remote:   "2001:db8::1",
			xff:      "203.0.113.9, 2001:db8::2",
			expected: "203.0.113.9",
		},
		{
			name:     "spoofed xff from untrusted hop ignored",
			trusted:  ipv4,
			remote:   "203.0.113.99",
			xff:      "127.0.0.1",
			expected: "203.0.113.99",
		},
		{
			name:     "untrusted hop in middle becomes the answer",
			trusted:  ipv4,
			remote:   "10.0.0.1",
			xff:      "8.8.8.8, 9.9.9.9, 10.0.0.2",
			expected: "9.9.9.9",
		},
		{
			name:     "all trusted falls back to leftmost",
			trusted:  ipv4,
			remote:   "10.0.0.1",
			xff:      "10.0.0.2, 10.0.0.3",
			expected: "10.0.0.2",
		},
		{
			name:     "empty xff returns remote",
			trusted:  ipv4,
			remote:   "10.0.0.1",
			xff:      "",
			expected: "10.0.0.1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.trusted.ClientIP(tc.remote, tc.xff)
			if got != tc.expected {
				t.Errorf("ClientIP = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestRouter_ValidateConfig_Fails_On_Invalid_Proxy(t *testing.T) {
	r := NewV2()
	r.TrustedProxies = []string{"bogus"}
	err := r.ValidateConfig()
	if err == nil {
		t.Fatal("expected ValidateConfig to reject bogus proxy")
	}
	if !errors.Is(err, ErrInvalidTrustedProxy) {
		t.Errorf("err = %v, want wrap of ErrInvalidTrustedProxy", err)
	}
}

func TestRouter_ValidateConfig_Empty_Ok(t *testing.T) {
	r := NewV2()
	if err := r.ValidateConfig(); err != nil {
		t.Errorf("empty TrustedProxies should validate, got %v", err)
	}
}

func TestRateLimitByIPE_InvalidCIDR(t *testing.T) {
	_, err := RateLimitByIPE(1, 1, WithTrustedProxies([]string{"not-a-cidr"}))
	if err == nil {
		t.Fatal("expected RateLimitByIPE to reject bogus proxy")
	}
	if !errors.Is(err, ErrInvalidTrustedProxy) {
		t.Errorf("err = %v, want ErrInvalidTrustedProxy wrap", err)
	}
}

// TestTrustedProxies_IPNets_DeepClone pins the C-05 follow-up fix:
// mutation of any *net.IPNet returned by IPNets() must not affect
// the underlying TrustedProxies value or subsequent IPNets() calls.
func TestTrustedProxies_IPNets_DeepClone(t *testing.T) {
	tp, err := ParseTrustedProxies([]string{"10.0.0.0/8", "192.168.0.0/16"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}

	snap1 := tp.IPNets()
	if len(snap1) != 2 {
		t.Fatalf("snap1 len = %d, want 2", len(snap1))
	}

	// Stomp every byte of every IPNet returned.
	for _, n := range snap1 {
		for i := range n.IP {
			n.IP[i] = 0xff
		}
		for i := range n.Mask {
			n.Mask[i] = 0
		}
	}

	// A fresh IPNets() call must return un-mutated nets.
	snap2 := tp.IPNets()
	if len(snap2) != 2 {
		t.Fatalf("snap2 len = %d, want 2", len(snap2))
	}
	if !snap2[0].Contains(net.ParseIP("10.0.0.5")) {
		t.Errorf("snap2[0] lost 10.0.0.5 (mutation leaked): %v", snap2[0])
	}
	if !snap2[1].Contains(net.ParseIP("192.168.1.1")) {
		t.Errorf("snap2[1] lost 192.168.1.1 (mutation leaked): %v", snap2[1])
	}
	// And the underlying Contains() check still works.
	if !tp.Contains("10.0.0.5") {
		t.Error("TrustedProxies.Contains() broke after caller mutation of IPNets() return")
	}
}
