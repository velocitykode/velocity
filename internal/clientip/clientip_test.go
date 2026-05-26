package clientip

import (
	"net"
	"net/http"
	"testing"
)

// mustCIDR is a tiny helper that turns a CIDR string into *net.IPNet.
// Fails the test on a parse error.
func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	out, err := ParseCIDRs([]string{s})
	if err != nil || len(out) != 1 {
		t.Fatalf("ParseCIDRs(%q): %v (n=%d)", s, err, len(out))
	}
	return out[0]
}

// newReq builds an *http.Request with the given RemoteAddr and
// optional header key/value pairs.
func newReq(remoteAddr string, headers ...string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remoteAddr
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return r
}

func TestExtract_NoTrustedProxies_ReturnsRemoteAddr(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		headers    []string
		want       string
	}{
		{
			name:       "ipv4 with port, no headers",
			remoteAddr: "203.0.113.5:54321",
			want:       "203.0.113.5",
		},
		{
			name:       "ipv4 with port, ignores XFF",
			remoteAddr: "203.0.113.5:54321",
			headers:    []string{"X-Forwarded-For", "1.2.3.4, 5.6.7.8"},
			want:       "203.0.113.5",
		},
		{
			name:       "ipv4 with port, ignores X-Real-IP",
			remoteAddr: "203.0.113.5:54321",
			headers:    []string{"X-Real-IP", "1.2.3.4"},
			want:       "203.0.113.5",
		},
		{
			name:       "ipv4 with port, ignores Forwarded",
			remoteAddr: "203.0.113.5:54321",
			headers:    []string{"Forwarded", "for=1.2.3.4"},
			want:       "203.0.113.5",
		},
		{
			name:       "ipv6 with brackets+port",
			remoteAddr: "[2001:db8::1]:443",
			want:       "2001:db8::1",
		},
		{
			name:       "bare ipv4 no port",
			remoteAddr: "203.0.113.5",
			want:       "203.0.113.5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractString(newReq(tc.remoteAddr, tc.headers...), nil)
			if got != tc.want {
				t.Fatalf("ExtractString = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtract_TrustedProxy_HonorsRightmostOfTrustedXFF(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}

	cases := []struct {
		name string
		req  *http.Request
		want string
	}{
		{
			name: "right-most untrusted wins",
			req:  newReq("10.0.0.1:443", "X-Forwarded-For", "203.0.113.9, 10.0.0.2, 10.0.0.3"),
			want: "203.0.113.9",
		},
		{
			name: "single untrusted entry",
			req:  newReq("10.0.0.1:443", "X-Forwarded-For", "198.51.100.7"),
			want: "198.51.100.7",
		},
		{
			name: "whole chain trusted falls back to left-most",
			req:  newReq("10.0.0.1:443", "X-Forwarded-For", "10.0.0.5, 10.0.0.6"),
			want: "10.0.0.5",
		},
		{
			name: "garbage entries skipped",
			req:  newReq("10.0.0.1:443", "X-Forwarded-For", "not-an-ip, 203.0.113.9, 10.0.0.2"),
			want: "203.0.113.9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractString(tc.req, trusted)
			if got != tc.want {
				t.Fatalf("ExtractString = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtract_TrustedProxy_PrefersForwardedOverXFF(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}

	r := newReq("10.0.0.1:443",
		"Forwarded", `for="192.0.2.43:47011";proto=https`,
		"X-Forwarded-For", "198.51.100.7",
	)
	if got := ExtractString(r, trusted); got != "192.0.2.43" {
		t.Fatalf("ExtractString = %q, want %q", got, "192.0.2.43")
	}
}

func TestExtract_TrustedProxy_ForwardedIPv6(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}
	r := newReq("10.0.0.1:443", "Forwarded", `for="[2001:db8::1]:443"`)
	if got := ExtractString(r, trusted); got != "2001:db8::1" {
		t.Fatalf("ExtractString = %q, want %q", got, "2001:db8::1")
	}
}

func TestExtract_TrustedProxy_RightmostOfTrustedForwarded(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}
	r := newReq("10.0.0.1:443", "Forwarded", "for=203.0.113.9, for=10.0.0.2, for=10.0.0.3")
	if got := ExtractString(r, trusted); got != "203.0.113.9" {
		t.Fatalf("ExtractString = %q, want %q", got, "203.0.113.9")
	}
}

func TestExtract_SpoofedXFFFromUntrustedRemote_Ignored(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}

	// Attacker connects directly (203.0.113.9) and tries to spoof the
	// client IP via XFF. RemoteAddr is not in trustedProxies, so XFF
	// must be ignored.
	r := newReq("203.0.113.9:54321", "X-Forwarded-For", "8.8.8.8")
	if got := ExtractString(r, trusted); got != "203.0.113.9" {
		t.Fatalf("spoofed XFF leaked: got %q, want %q", got, "203.0.113.9")
	}

	// Same for X-Real-IP.
	r = newReq("203.0.113.9:54321", "X-Real-IP", "8.8.8.8")
	if got := ExtractString(r, trusted); got != "203.0.113.9" {
		t.Fatalf("spoofed X-Real-IP leaked: got %q, want %q", got, "203.0.113.9")
	}

	// And RFC 7239 Forwarded.
	r = newReq("203.0.113.9:54321", "Forwarded", "for=8.8.8.8")
	if got := ExtractString(r, trusted); got != "203.0.113.9" {
		t.Fatalf("spoofed Forwarded leaked: got %q, want %q", got, "203.0.113.9")
	}
}

func TestExtract_LoopbackPrivateSpoofRejected(t *testing.T) {
	// trustedProxies = [10.0.0.0/8]; XFF contains loopback (127.0.0.1)
	// and private (192.168.x) entries. Those addresses are NOT in the
	// trusted set, so they must be returned as the right-most-of-trusted
	// hit (i.e. NOT skipped as if loopback were implicitly trusted).
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}

	// Attacker behind LB tries to forge "I am 127.0.0.1".
	r := newReq("10.0.0.1:443", "X-Forwarded-For", "127.0.0.1, 10.0.0.2")
	got := ExtractString(r, trusted)
	if got != "127.0.0.1" {
		t.Fatalf("got %q, want %q (loopback must be returned, NOT skipped as if trusted)", got, "127.0.0.1")
	}

	// Same for private range.
	r = newReq("10.0.0.1:443", "X-Forwarded-For", "192.168.1.50, 10.0.0.2")
	if got := ExtractString(r, trusted); got != "192.168.1.50" {
		t.Fatalf("got %q, want %q", got, "192.168.1.50")
	}

	// If the deployment legitimately fronts everything with localhost
	// (e.g. sidecar pattern) and explicitly lists 127.0.0.1, THEN it
	// gets skipped during right-most resolution.
	trustedLoop := []*net.IPNet{mustCIDR(t, "127.0.0.0/8"), mustCIDR(t, "10.0.0.0/8")}
	r = newReq("127.0.0.1:443", "X-Forwarded-For", "203.0.113.9, 127.0.0.2, 10.0.0.2")
	if got := ExtractString(r, trustedLoop); got != "203.0.113.9" {
		t.Fatalf("got %q, want %q", got, "203.0.113.9")
	}
}

func TestExtract_XRealIP_RejectsMultiValue(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}

	// Multi-value X-Real-IP must be rejected (would otherwise allow
	// throttle-key spoofing).
	r := newReq("10.0.0.1:443", "X-Real-IP", "1.2.3.4, 5.6.7.8")
	if got := ExtractString(r, trusted); got != "10.0.0.1" {
		t.Fatalf("got %q, want fall-through to RemoteAddr %q", got, "10.0.0.1")
	}

	// Whitespace-separated also rejected.
	r = newReq("10.0.0.1:443", "X-Real-IP", "1.2.3.4 5.6.7.8")
	if got := ExtractString(r, trusted); got != "10.0.0.1" {
		t.Fatalf("got %q, want fall-through to RemoteAddr %q", got, "10.0.0.1")
	}

	// Well-formed single value is honoured.
	r = newReq("10.0.0.1:443", "X-Real-IP", "1.2.3.4")
	if got := ExtractString(r, trusted); got != "1.2.3.4" {
		t.Fatalf("got %q, want %q", got, "1.2.3.4")
	}
}

func TestExtract_UnparseableRemoteAddr(t *testing.T) {
	// Nil request.
	if got := Extract(nil, nil); got != nil {
		t.Fatalf("nil request: got %v, want nil", got)
	}

	// Garbage RemoteAddr returns nil even when headers are set, because
	// trusting headers when we cannot identify the peer is unsafe.
	r := newReq("garbage", "X-Forwarded-For", "1.2.3.4")
	if got := Extract(r, []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}); got != nil {
		t.Fatalf("garbage RemoteAddr: got %v, want nil", got)
	}
}

func TestParseCIDRs(t *testing.T) {
	cases := []struct {
		name    string
		input   []string
		wantLen int
		wantErr bool
	}{
		{"empty input", nil, 0, false},
		{"single ipv4 host", []string{"10.0.0.1"}, 1, false},
		{"ipv4 cidr", []string{"10.0.0.0/8"}, 1, false},
		{"single ipv6 host", []string{"2001:db8::1"}, 1, false},
		{"ipv6 cidr", []string{"2001:db8::/32"}, 1, false},
		{"mixed", []string{"10.0.0.0/8", "192.168.0.0/16", "127.0.0.1"}, 3, false},

		{"empty entry", []string{""}, 0, true},
		{"garbage", []string{"not-an-ip"}, 0, true},
		{"bad cidr", []string{"10.0.0.0/99"}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ParseCIDRs(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseCIDRs(%v): want error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCIDRs(%v): unexpected error %v", tc.input, err)
			}
			if len(out) != tc.wantLen {
				t.Fatalf("ParseCIDRs(%v): len=%d, want %d", tc.input, len(out), tc.wantLen)
			}
		})
	}
}

func TestCloneIPNets_NilAndEmpty(t *testing.T) {
	if got := CloneIPNets(nil); got != nil {
		t.Errorf("CloneIPNets(nil) = %v, want nil", got)
	}
	if got := CloneIPNets([]*net.IPNet{}); got != nil {
		t.Errorf("CloneIPNets(empty) = %v, want nil", got)
	}
	if got := CloneIPNets([]*net.IPNet{nil, nil}); got != nil {
		t.Errorf("CloneIPNets(all-nil) = %v, want nil (skipped)", got)
	}
}

func TestCloneIPNets_DeepCopiesPointers(t *testing.T) {
	src, err := ParseCIDRs([]string{"10.0.0.0/8", "2001:db8::/32"})
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}
	dst := CloneIPNets(src)
	if len(dst) != len(src) {
		t.Fatalf("len(dst) = %d, want %d", len(dst), len(src))
	}
	for i := range src {
		if dst[i] == src[i] {
			t.Errorf("dst[%d] aliases src[%d] (*net.IPNet pointer reused)", i, i)
		}
		// IP backing array must be distinct.
		if len(src[i].IP) > 0 && len(dst[i].IP) > 0 && &src[i].IP[0] == &dst[i].IP[0] {
			t.Errorf("dst[%d].IP shares backing array with src[%d].IP", i, i)
		}
		// Mask backing array must be distinct.
		if len(src[i].Mask) > 0 && len(dst[i].Mask) > 0 && &src[i].Mask[0] == &dst[i].Mask[0] {
			t.Errorf("dst[%d].Mask shares backing array with src[%d].Mask", i, i)
		}
		// Equality of value still holds.
		if !src[i].IP.Equal(dst[i].IP) {
			t.Errorf("dst[%d].IP = %v, want %v", i, dst[i].IP, src[i].IP)
		}
		if src[i].Mask.String() != dst[i].Mask.String() {
			t.Errorf("dst[%d].Mask = %v, want %v", i, dst[i].Mask, src[i].Mask)
		}
	}
}

// Caller-side mutation of the source slice and its elements must not
// alter the clone. This is the core property exercised end-to-end by
// the auth / exceptions / router consumers.
func TestCloneIPNets_CallerMutationIsolated(t *testing.T) {
	src, err := ParseCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}
	dst := CloneIPNets(src)

	// Mutate the source IP and Mask bytes.
	for i := range src[0].IP {
		src[0].IP[i] = 0xff
	}
	for i := range src[0].Mask {
		src[0].Mask[i] = 0
	}
	// Reassign the slice's pointer to nil. (Caller mutation of the
	// outer slice contents.)
	src[0] = nil

	// dst must still be 10.0.0.0/8.
	if len(dst) != 1 || dst[0] == nil {
		t.Fatalf("dst was clobbered by source mutation: %v", dst)
	}
	if !dst[0].Contains(net.ParseIP("10.0.0.1")) {
		t.Errorf("clone no longer contains 10.0.0.1; src mutation leaked: dst[0]=%v", dst[0])
	}
	if dst[0].Contains(net.ParseIP("203.0.113.5")) {
		t.Errorf("clone now contains 203.0.113.5; src mutation leaked: dst[0]=%v", dst[0])
	}
}

// Appending to the source slice after clone must not show up in the
// clone (the outer slice header is owned by the consumer too).
func TestCloneIPNets_AppendToSourceNotVisible(t *testing.T) {
	src, _ := ParseCIDRs([]string{"10.0.0.0/8"})
	dst := CloneIPNets(src)

	// Append a brand-new trusted network to the source.
	extra, _ := ParseCIDRs([]string{"192.168.0.0/16"})
	src = append(src, extra...)
	if len(src) != 2 {
		t.Fatalf("src append precondition: want 2, got %d", len(src))
	}

	if len(dst) != 1 {
		t.Fatalf("dst length changed after append to src: %d", len(dst))
	}
	if dst[0].Contains(net.ParseIP("192.168.1.1")) {
		t.Errorf("dst silently picked up the appended entry")
	}
}
