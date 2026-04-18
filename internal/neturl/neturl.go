// Package neturl provides host and IP classification helpers used by the
// framework's SSRF defenses. It is internal by design: SSRF policy lives
// behind explicit options on packages like httpclient and notification —
// do not add transitive consumers.
//
// The classifications here cover the usual SSRF vectors:
//   - loopback (127.0.0.0/8, ::1)
//   - unspecified (0.0.0.0, ::)
//   - link-local unicast (169.254.0.0/16, fe80::/10)
//   - link-local multicast
//   - RFC1918 private IPv4 (10/8, 172.16/12, 192.168/16)
//   - IPv6 unique-local (fc00::/7)
//   - carrier-grade NAT / shared address space (100.64.0.0/10)
//   - cloud metadata IP (169.254.169.254 — already link-local, called out
//     explicitly so callers can log/report it distinctly)
package neturl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrPrivateHost is the sentinel returned when a host resolves to a
// disallowed private, loopback, link-local, or metadata address. Callers
// should wrap it with their package prefix before surfacing to callers.
var ErrPrivateHost = errors.New("neturl: host resolves to private or internal address")

// MetadataIPv4 is the well-known cloud instance metadata IPv4 address
// (AWS, GCP, Azure, DigitalOcean, etc.).
const MetadataIPv4 = "169.254.169.254"

// MetadataIPv6 is the IPv6 counterpart used by GCP for its metadata
// endpoint.
const MetadataIPv6 = "fd00:ec2::254"

// cgnatNet is 100.64.0.0/10 — RFC 6598 carrier-grade NAT space, also
// frequently reachable from inside cloud VPCs.
var cgnatNet = mustCIDR("100.64.0.0/10")

// teredoNet is 2001::/32 — RFC 4380 Teredo tunneling. Rarely seen on the
// modern internet, but an attacker-supplied Teredo target encodes an
// arbitrary IPv4 (including private ranges) inside an IPv6 address. Treat
// any Teredo destination as internal to avoid that escape hatch.
var teredoNet = mustCIDR("2001::/32")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(fmt.Errorf("neturl: bad CIDR %q: %w", s, err))
	}
	return n
}

// IsMetadataIP reports whether ip matches a well-known cloud metadata
// endpoint. These are technically link-local but are called out separately
// because exfiltrating IAM credentials via SSRF is the canonical attack.
func IsMetadataIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.Equal(net.ParseIP(MetadataIPv4).To4())
	}
	return ip.Equal(net.ParseIP(MetadataIPv6))
}

// IsPrivateOrInternal reports whether ip falls in any range that should
// never be reachable from an outbound request in normal operation:
// loopback, unspecified, link-local, RFC1918, fc00::/7, CGNAT, or a
// known metadata IP.
func IsPrivateOrInternal(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && cgnatNet.Contains(v4) {
		return true
	}
	if teredoNet.Contains(ip) {
		return true
	}
	if IsMetadataIP(ip) {
		return true
	}
	return false
}

// IsPrivateHost reports whether host (an IP literal or DNS name) refers
// to a disallowed address range. Hostnames are resolved via resolver —
// when nil, net.DefaultResolver is used. Well-known localhost aliases
// ("localhost") are treated as private without DNS resolution.
//
// If any resolved address is private, the whole host is considered
// private. DNS rebinding is partially mitigated: callers should still
// pin the resolved IP in their DialContext rather than re-resolving.
func IsPrivateHost(ctx context.Context, resolver *net.Resolver, host string) (bool, error) {
	if host == "" {
		return false, errors.New("neturl: empty host")
	}
	// Strip brackets from IPv6 literals.
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")

	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return true, nil
	}

	if ip := net.ParseIP(host); ip != nil {
		return IsPrivateOrInternal(ip), nil
	}

	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return false, fmt.Errorf("neturl: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return false, fmt.Errorf("neturl: resolve %q: no addresses", host)
	}
	for _, a := range addrs {
		if IsPrivateOrInternal(a.IP) {
			return true, nil
		}
	}
	return false, nil
}

// ValidateURLHost parses rawURL and reports whether its host resolves to
// a private range. Scheme must be http or https. Returns ErrPrivateHost
// wrapped with context when the host is disallowed.
func ValidateURLHost(ctx context.Context, resolver *net.Resolver, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("neturl: parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("neturl: unsupported scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("neturl: url has no host")
	}
	private, err := IsPrivateHost(ctx, resolver, host)
	if err != nil {
		return err
	}
	if private {
		return fmt.Errorf("%w: %s", ErrPrivateHost, host)
	}
	return nil
}

// ETLDPlusOne returns a best-effort eTLD+1 for host. It is intentionally
// simple (no PSL dependency): it returns the last two labels for plain
// hostnames and the literal IP for IP inputs. Use only for cross-host
// comparison in redirect stripping where a false-negative (stripping too
// aggressively) is safer than a false-positive.
func ETLDPlusOne(host string) string {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	host = strings.ToLower(host)
	// Strip port if present.
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i+1:], ":") {
		host = host[:i]
	}
	labels := strings.Split(host, ".")
	if len(labels) <= 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}
