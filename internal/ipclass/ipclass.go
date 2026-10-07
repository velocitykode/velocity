// Package ipclass decides whether an IP address is internal: one the
// framework must not reach on behalf of a caller-supplied destination
// (SSRF). It is the single list of ranges for every such check in the
// module: the outbound dial guard (internal/neturl, httpclient) and the
// url_public validation rule both call it, so the two cannot drift.
//
// It imports only the standard library so the validation rules can link
// it without pulling anything else into their graph; deps_boundary_test.go
// holds it there.
//
// Refused as written:
//   - "this network" (0.0.0.0/8) and unspecified (::)
//   - loopback (127.0.0.0/8, ::1)
//   - link-local unicast (169.254.0.0/16, fe80::/10)
//   - multicast (224.0.0.0/4, ff00::/8)
//   - RFC 1918 private IPv4 (10/8, 172.16/12, 192.168/16)
//   - IPv6 unique-local (fc00::/7)
//   - carrier-grade NAT / shared address space (100.64.0.0/10)
//   - cloud metadata (169.254.169.254, fd00:ec2::254; both already inside
//     ranges above, named so callers can report them distinctly)
//   - Teredo (2001::/32) and the NAT64 local-use pool (64:ff9b:1::/48),
//     both whole
//
// Refused by what they carry: an IPv6 address that embeds an IPv4
// address is internal when the embedded address is. See embeddedIPv4 for
// the forms. The net.IP predicates judge an IPv6 address by its IPv6
// prefix alone, so without this 64:ff9b::7f00:1 (127.0.0.1 behind a NAT64
// gateway) reads as public.
//
// Two NAT64 prefixes, two rules, on purpose. The well-known prefix
// 64:ff9b::/96 has one fixed layout and is how every public IPv4 site
// appears on an IPv6-only host with DNS64, so it is judged by the
// embedded address: refusing it whole would break ordinary outbound
// calls there. The local-use pool 64:ff9b:1::/48 (RFC 8215) is carved by
// each operator into prefixes of any RFC 6052 length, which moves the
// IPv4 bits; the layout cannot be known from the address, and reading
// the wrong one lets an internal destination pass, so the pool is
// refused whole. Teredo is refused whole for a like reason: its embedded
// addresses are obfuscated and name a relay, not the destination.
//
// Known limit: RFC 6052 also allows NAT64 on a network-specific prefix
// chosen by the operator from its own address space (lengths 32, 40, 48,
// 56, 64, 96). Such a prefix is indistinguishable from any other global
// IPv6 address, so an internal IPv4 address reached through one is not
// detected here. A deployment that runs NAT64 on its own prefix must
// keep that prefix unreachable from the application by network policy.
package ipclass

import "net"

// MetadataIPv4 is the well-known cloud instance metadata IPv4 address
// (AWS, GCP, Azure, DigitalOcean, etc.).
const MetadataIPv4 = "169.254.169.254"

// MetadataIPv6 is the IPv6 metadata endpoint address (AWS IMDS over
// IPv6).
const MetadataIPv6 = "fd00:ec2::254"

var (
	metadataV4 = net.ParseIP(MetadataIPv4).To4()
	metadataV6 = net.ParseIP(MetadataIPv6)

	// thisNet is 0.0.0.0/8, "this host on this network" (RFC 1122).
	// 0.0.0.0 itself reaches the local machine on common kernels and the
	// rest of the block is never a legitimate remote destination.
	thisNet = &net.IPNet{IP: net.IPv4(0, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)}

	// cgnatNet is 100.64.0.0/10, RFC 6598 carrier-grade NAT space, also
	// frequently reachable from inside cloud VPCs.
	cgnatNet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

	// teredoNet is 2001::/32, RFC 4380 Teredo tunneling. A Teredo
	// address encodes arbitrary IPv4 addresses (including private
	// ranges), so any Teredo destination is internal.
	teredoNet = &net.IPNet{IP: net.IP{0x20, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, Mask: net.CIDRMask(32, 128)}

	// nat64LocalNet is 64:ff9b:1::/48, the RFC 8215 NAT64 local-use
	// pool. Refused whole; the package comment says why.
	nat64LocalNet = &net.IPNet{IP: net.IP{0, 0x64, 0xff, 0x9b, 0, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, Mask: net.CIDRMask(48, 128)}
)

// IsMetadataIP reports whether ip is a well-known cloud metadata
// endpoint, or an IPv6 address carrying the IPv4 one (see embeddedIPv4).
// These are link-local or unique-local already; they are called out
// separately because exfiltrating instance credentials is the canonical
// SSRF attack. A nil ip is not one.
func IsMetadataIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if isMetadataAddr(ip) {
		return true
	}
	carried, n := embeddedIPv4(ip)
	for i := range n {
		if isMetadataAddr(carried[i][:]) {
			return true
		}
	}
	return false
}

// isMetadataAddr classifies ip as written, without looking inside it.
func isMetadataAddr(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		return v4.Equal(metadataV4)
	}
	return ip.Equal(metadataV6)
}

// IsPrivateOrInternal reports whether ip must never be the destination
// of a request made for a caller-supplied address. The package comment
// lists the ranges. A nil ip reports false: callers reject an address
// they could not parse before asking.
//
// Invariant: for every IPv4 address a, each IPv6 form embeddedIPv4
// knows gets the same answer when it carries a as a gets itself. Why
// both directions: an internal destination must not read as public
// under any spelling, and a public one must stay reachable through
// NAT64 and 6to4.
func IsPrivateOrInternal(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if isInternalAddr(ip) {
		return true
	}
	carried, n := embeddedIPv4(ip)
	for i := range n {
		if isInternalAddr(carried[i][:]) {
			return true
		}
	}
	return false
}

// isInternalAddr classifies ip as written, without looking inside it.
// Every range check lives here, so a plain address and one extracted by
// embeddedIPv4 go through the same list.
func isInternalAddr(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsPrivate() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return thisNet.Contains(v4) || cgnatNet.Contains(v4)
	}
	return teredoNet.Contains(ip) || nat64LocalNet.Contains(ip) || ip.Equal(metadataV6)
}

// maxEmbedded bounds how many IPv4 addresses one IPv6 address can carry
// under the forms embeddedIPv4 knows: one from its prefix form and one
// from an ISATAP interface identifier.
const maxEmbedded = 2

// embeddedIPv4 returns the IPv4 addresses carried inside the IPv6
// address ip, n of them. It is the one place that knows the layouts; the
// classifiers run their ordinary checks on what it returns. A translator
// or tunnel endpoint on the path delivers a packet for such an address
// to the IPv4 address inside it.
//
// Forms, by where the 32 bits sit:
//
//   - ::/96            IPv4-compatible (RFC 4291, deprecated): last 32 bits
//   - ::ffff:0:0:0/96  IPv4-translated (RFC 7915): last 32 bits
//   - 64:ff9b::/96     NAT64 well-known prefix (RFC 6052): last 32 bits
//   - 2002::/16        6to4 (RFC 3056): bits 16 to 47
//   - any /64 whose interface identifier is 0000:5efe or 0200:5efe,
//     ISATAP (RFC 5214): last 32 bits
//
// ISATAP is an interface identifier, not a prefix, so it can sit under
// 6to4 (ISATAP over 6to4); such an address carries two IPv4 addresses
// and both are returned.
//
// The IPv4-mapped form ::ffff:0:0/96 is not handled here: net.IP.To4
// already unwraps it and the classifiers see the IPv4 address directly.
// A 4-byte or IPv4-mapped ip therefore carries nothing.
func embeddedIPv4(ip net.IP) (out [maxEmbedded][4]byte, n int) {
	if len(ip) != net.IPv6len || ip.To4() != nil {
		return out, 0
	}
	switch {
	case ip[0] == 0x20 && ip[1] == 0x02:
		copy(out[n][:], ip[2:6])
		n++
	case allZero(ip[:8]) && ip[8] == 0xff && ip[9] == 0xff && ip[10] == 0 && ip[11] == 0,
		ip[0] == 0 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b && allZero(ip[4:12]),
		allZero(ip[:12]):
		copy(out[n][:], ip[12:16])
		n++
	}
	if ip[8]&^0x02 == 0 && ip[9] == 0 && ip[10] == 0x5e && ip[11] == 0xfe {
		copy(out[n][:], ip[12:16])
		n++
	}
	return out, n
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
