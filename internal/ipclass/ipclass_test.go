package ipclass

import (
	"encoding/binary"
	"net"
	"testing"
)

// embedding builds, independently of the code under test, an IPv6 address
// that carries v4 in one of the forms the guard must see through.
type embedding struct {
	name  string
	build func(v4 [4]byte) net.IP
}

func ip6(prefix string) net.IP {
	ip := net.ParseIP(prefix).To16()
	out := make(net.IP, net.IPv6len)
	copy(out, ip)
	return out
}

// tail32 writes v4 into the last 32 bits of prefix.
func tail32(prefix string) func([4]byte) net.IP {
	return func(v4 [4]byte) net.IP {
		ip := ip6(prefix)
		copy(ip[12:], v4[:])
		return ip
	}
}

// sixToFour writes v4 into bits 16..47 of rest, a 2002::/16 address.
func sixToFour(rest string) func([4]byte) net.IP {
	return func(v4 [4]byte) net.IP {
		ip := ip6(rest)
		copy(ip[2:6], v4[:])
		return ip
	}
}

var embeddings = []embedding{
	{"ipv4-compatible ::/96", tail32("::")},
	{"ipv4-translated ::ffff:0:0:0/96", tail32("::ffff:0:0:0")},
	{"nat64 well-known 64:ff9b::/96", tail32("64:ff9b::")},
	{"6to4 2002::/16", sixToFour("2002::")},
	{"6to4 with subnet and interface id", sixToFour("2002::1:0:0:0:1")},
	{"isatap private-use", tail32("2606:4700::5efe:0:0")},
	{"isatap globally unique", tail32("2606:4700::200:5efe:0:0")},
	{"isatap over 6to4 with a public outer address", func(v4 [4]byte) net.IP {
		ip := ip6("2002:808:808::5efe:0:0")
		copy(ip[12:], v4[:])
		return ip
	}},
}

func TestIsPrivateOrInternal_EmbeddedIPv4(t *testing.T) {
	internal := []string{
		"127.0.0.1", "127.255.255.254", "0.0.0.0", "0.0.0.1", "0.255.255.255",
		"10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.0.1", "192.168.255.255",
		"169.254.0.1", "169.254.169.254", "100.64.0.1", "100.127.255.255",
		"224.0.0.1", "239.255.255.255",
	}
	public := []string{
		"8.8.8.8", "1.1.1.1", "93.184.216.34",
		"126.255.255.255", "128.0.0.0", "9.255.255.255", "11.0.0.0",
		"172.15.255.255", "172.32.0.0", "192.167.255.255", "192.169.0.0",
		"169.253.255.255", "169.255.0.0", "100.63.255.255", "100.128.0.0",
		"223.255.255.255",
	}
	for _, e := range embeddings {
		for _, s := range internal {
			var v4 [4]byte
			copy(v4[:], net.ParseIP(s).To4())
			ip := e.build(v4)
			if !IsPrivateOrInternal(ip) {
				t.Errorf("%s: %s (carrying %s) must be internal", e.name, ip, s)
			}
		}
		for _, s := range public {
			var v4 [4]byte
			copy(v4[:], net.ParseIP(s).To4())
			ip := e.build(v4)
			if IsPrivateOrInternal(ip) {
				t.Errorf("%s: %s (carrying public %s) must pass", e.name, ip, s)
			}
		}
	}
}

// TestIsPrivateOrInternal_EmbeddedLiterals pins the reported addresses in
// the textual forms an attacker would supply.
func TestIsPrivateOrInternal_EmbeddedLiterals(t *testing.T) {
	cases := []struct {
		ip       string
		internal bool
	}{
		{"::127.0.0.1", true},
		{"::7f00:1", true},
		{"::10.0.0.1", true},
		{"::169.254.169.254", true},
		{"::8.8.8.8", false},
		{"::ffff:0:127.0.0.1", true},
		{"::ffff:0:8.8.8.8", false},
		{"64:ff9b::7f00:1", true},
		{"64:ff9b::a9fe:a9fe", true},
		{"64:ff9b::127.0.0.1", true},
		{"64:ff9b::c0a8:1", true},
		{"64:ff9b::808:808", false},
		{"2002:7f00:1::", true},
		{"2002:a9fe:a9fe::", true},
		{"2002:a00:1::1", true},
		{"2002:c0a8:101:1::1", true},
		{"2002:808:808::1", false},
		{"2606:4700::5efe:127.0.0.1", true},
		{"2606:4700::200:5efe:10.0.0.1", true},
		{"2606:4700::5efe:8.8.8.8", false},
		{"2002:808:808::5efe:192.168.1.1", true},
		// Already caught before the change; pinned so they stay caught.
		{"::ffff:127.0.0.1", true},
		{"::ffff:8.8.8.8", false},
		{"::1", true},
		{"::", true},
		{"fe80::5efe:8.8.8.8", true},
		// The NAT64 local-use pool is refused whole: its layout is the
		// operator's choice, so no reading of the address can be trusted.
		{"64:ff9b:1::7f00:1", true},
		{"64:ff9b:1::808:808", true},
		{"64:ff9b:1:7f00:0:100::", true},
		{"64:ff9b:1:abcd::808:808", true},
		{"64:ff9b:1:ffff:ffff:ffff:ffff:ffff", true},
		{"64:ff9b:2::808:808", false},
		{"64:ff9b:0:1::7f00:1", false},
		// An interface identifier that is not ISATAP carries nothing.
		{"2606:4700::5eff:7f00:1", false},
		{"2606:4700::100:5efe:7f00:1", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("parse %q", tc.ip)
		}
		if got := IsPrivateOrInternal(ip); got != tc.internal {
			t.Errorf("IsPrivateOrInternal(%s) = %v, want %v", tc.ip, got, tc.internal)
		}
	}
}

func TestIsMetadataIP_EmbeddedIPv4(t *testing.T) {
	meta := [4]byte{169, 254, 169, 254}
	near := [4]byte{169, 254, 169, 253}
	for _, e := range embeddings {
		if ip := e.build(meta); !IsMetadataIP(ip) {
			t.Errorf("%s: %s carries the metadata address", e.name, ip)
		}
		if ip := e.build(near); IsMetadataIP(ip) {
			t.Errorf("%s: %s does not carry the metadata address", e.name, ip)
		}
	}
	if !IsMetadataIP(net.ParseIP(MetadataIPv6)) {
		t.Error("the IPv6 metadata address must still match")
	}
	if !IsMetadataIP(net.ParseIP("::ffff:169.254.169.254")) {
		t.Error("the IPv4-mapped metadata address must still match")
	}
}

// checkEmbeddingAgreement asserts the guard answers the same for a plain
// IPv4 address and for every IPv6 form carrying it.
func checkEmbeddingAgreement(t *testing.T, v4 [4]byte) {
	t.Helper()
	plain := net.IPv4(v4[0], v4[1], v4[2], v4[3])
	want := IsPrivateOrInternal(plain)
	wantMeta := IsMetadataIP(plain)
	for _, e := range embeddings {
		ip := e.build(v4)
		if got := IsPrivateOrInternal(ip); got != want {
			t.Errorf("%s: IsPrivateOrInternal(%s) = %v, plain %s = %v", e.name, ip, got, plain, want)
		}
		if got := IsMetadataIP(ip); got != wantMeta {
			t.Errorf("%s: IsMetadataIP(%s) = %v, plain %s = %v", e.name, ip, got, plain, wantMeta)
		}
	}
}

// TestEmbeddedIPv4_AgreesWithPlain walks both sides of every boundary of
// every IPv4 range the guard knows, plus a fixed stride over the whole
// IPv4 space, and requires the embedded forms to agree with the plain
// address.
func TestEmbeddedIPv4_AgreesWithPlain(t *testing.T) {
	ranges := []string{
		"0.0.0.0/32", "0.0.0.0/8", "127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "169.254.169.254/32", "100.64.0.0/10", "224.0.0.0/4", "224.0.0.0/24",
		"240.0.0.0/4", "255.255.255.255/32", "192.0.0.0/24", "198.18.0.0/15",
	}
	for _, r := range ranges {
		_, n, err := net.ParseCIDR(r)
		if err != nil {
			t.Fatal(err)
		}
		first := binary.BigEndian.Uint32(n.IP.To4())
		last := first | ^binary.BigEndian.Uint32(n.Mask)
		for _, u := range []uint32{first - 1, first, first + 1, last - 1, last, last + 1} {
			var v4 [4]byte
			binary.BigEndian.PutUint32(v4[:], u)
			checkEmbeddingAgreement(t, v4)
		}
	}
	// 65521 is prime, so the stride visits every /16 and varies the low
	// bytes instead of sampling one host per network.
	for u := uint64(0); u <= 0xffffffff; u += 65521 {
		var v4 [4]byte
		binary.BigEndian.PutUint32(v4[:], uint32(u))
		checkEmbeddingAgreement(t, v4)
	}
}

func FuzzEmbeddedIPv4AgreesWithPlain(f *testing.F) {
	for _, s := range []uint32{0, 1, 0x7f000001, 0x0a000001, 0xa9fea9fe, 0xc0a80101, 0x64400001, 0x08080808, 0xffffffff} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, u uint32) {
		var v4 [4]byte
		binary.BigEndian.PutUint32(v4[:], u)
		checkEmbeddingAgreement(t, v4)
	})
}

func BenchmarkIsPrivateOrInternal(b *testing.B) {
	for _, s := range []string{"8.8.8.8", "2606:4700:4700::1111", "64:ff9b::808:808", "2002:808:808::1"} {
		ip := net.ParseIP(s)
		b.Run(s, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if IsPrivateOrInternal(ip) {
					b.Fatal("public address judged internal")
				}
			}
		})
	}
}

// TestIsPrivateOrInternal_AsWritten pins the range list for addresses
// that carry nothing: both edges of every range.
func TestIsPrivateOrInternal_AsWritten(t *testing.T) {
	cases := []struct {
		ip       string
		internal bool
	}{
		{"0.0.0.0", true}, {"0.0.0.1", true}, {"0.255.255.255", true}, {"1.0.0.0", false},
		{"127.0.0.1", true}, {"127.255.255.255", true}, {"126.255.255.255", false}, {"128.0.0.0", false},
		{"10.0.0.0", true}, {"10.255.255.255", true}, {"9.255.255.255", false}, {"11.0.0.0", false},
		{"172.16.0.0", true}, {"172.31.255.255", true}, {"172.15.255.255", false}, {"172.32.0.0", false},
		{"192.168.0.0", true}, {"192.168.255.255", true}, {"192.167.255.255", false}, {"192.169.0.0", false},
		{"169.254.0.0", true}, {"169.254.255.255", true}, {"169.253.255.255", false}, {"169.255.0.0", false},
		{"100.64.0.0", true}, {"100.127.255.255", true}, {"100.63.255.255", false}, {"100.128.0.0", false},
		{"224.0.0.0", true}, {"239.255.255.255", true}, {"223.255.255.255", false},
		{"8.8.8.8", false},
		{"::", true}, {"::1", true},
		{"fe80::1", true}, {"febf::1", true}, {"fec0::1", false},
		{"fc00::1", true}, {"fdff::1", true}, {"fd00:ec2::254", true}, {"fbff::1", false},
		{"ff02::1", true}, {"ff0e::1", true},
		{"2001::1", true}, {"2001:0:ffff::1", true}, {"2001:1::1", false},
		{"64:ff9b:1::", true}, {"64:ff9b:1:ffff:ffff:ffff:ffff:ffff", true}, {"64:ff9b:2::", false},
		{"2606:4700:4700::1111", false}, {"2001:4860:4860::8888", false},
		{"::ffff:10.0.0.1", true}, {"::ffff:0.0.0.1", true}, {"::ffff:8.8.8.8", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("parse %q", tc.ip)
		}
		if got := IsPrivateOrInternal(ip); got != tc.internal {
			t.Errorf("IsPrivateOrInternal(%s) = %v, want %v", tc.ip, got, tc.internal)
		}
		if v4 := ip.To4(); v4 != nil {
			if got := IsPrivateOrInternal(v4); got != tc.internal {
				t.Errorf("IsPrivateOrInternal(4-byte %s) = %v, want %v", tc.ip, got, tc.internal)
			}
		}
	}
}

// TestEdgeInputs feeds slices that are not addresses. Nil and empty
// report false; a malformed length must be answered without a panic
// and carries nothing.
func TestEdgeInputs(t *testing.T) {
	if IsPrivateOrInternal(nil) || IsMetadataIP(nil) {
		t.Error("nil is not an address and must report false")
	}
	for _, ip := range []net.IP{{}, {1, 2, 3}, make(net.IP, 5), make(net.IP, 17), {0x20, 0x02, 0x7f}} {
		if IsPrivateOrInternal(ip) {
			t.Errorf("IsPrivateOrInternal(%v) = true for a non-address", []byte(ip))
		}
		if IsMetadataIP(ip) {
			t.Errorf("IsMetadataIP(%v) = true for a non-address", []byte(ip))
		}
		if _, n := embeddedIPv4(ip); n != 0 {
			t.Errorf("embeddedIPv4(%v) returned %d addresses for a non-address", []byte(ip), n)
		}
	}
}
