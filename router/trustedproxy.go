package router

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/velocitykode/velocity/internal/clientip"
)

// ErrInvalidTrustedProxy is returned when a trusted-proxy entry is not a
// valid IP or CIDR expression.
var ErrInvalidTrustedProxy = errors.New("velocity/router: invalid trusted proxy entry")

// TrustedProxies is a parsed, immutable set of trusted proxy networks.
// Callers build it once (typically at boot) via ParseTrustedProxies and
// reuse the value across requests — it is safe for concurrent reads.
type TrustedProxies struct {
	nets []*net.IPNet
}

// ParseTrustedProxies parses a list of IP or CIDR strings into a
// TrustedProxies set. Any invalid entry returns an error wrapped with
// ErrInvalidTrustedProxy so callers can fail fast at boot instead of
// silently swallowing typos.
func ParseTrustedProxies(entries []string) (*TrustedProxies, error) {
	if len(entries) == 0 {
		return &TrustedProxies{}, nil
	}
	nets := make([]*net.IPNet, 0, len(entries))
	for _, raw := range entries {
		n, err := parseTrustedProxyEntry(raw)
		if err != nil {
			return nil, err
		}
		nets = append(nets, n)
	}
	return &TrustedProxies{nets: nets}, nil
}

// parseTrustedProxyEntry converts a single IP or CIDR string into *net.IPNet.
func parseTrustedProxyEntry(raw string) (*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: empty entry", ErrInvalidTrustedProxy)
	}
	if strings.Contains(raw, "/") {
		_, ipNet, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %q: %v", ErrInvalidTrustedProxy, raw, err)
		}
		return ipNet, nil
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return nil, fmt.Errorf("%w: not an ip or cidr: %q", ErrInvalidTrustedProxy, raw)
	}
	var mask net.IPMask
	if ip.To4() != nil {
		mask = net.CIDRMask(32, 32)
	} else {
		mask = net.CIDRMask(128, 128)
	}
	return &net.IPNet{IP: ip, Mask: mask}, nil
}

// Len reports the number of trusted-proxy networks.
func (tp *TrustedProxies) Len() int {
	if tp == nil {
		return 0
	}
	return len(tp.nets)
}

// IPNets returns a deep clone of the parsed proxy networks, suitable
// for passing to internal/clientip.Extract or unioning with another
// trust list at the middleware layer.
//
// The clone is independent: callers may mutate the slice header or
// any IPNet's IP / Mask backing array without affecting subsequent
// reads on the underlying TrustedProxies value (which is itself
// immutable after construction). Returns nil when the set is empty.
func (tp *TrustedProxies) IPNets() []*net.IPNet {
	if tp == nil || len(tp.nets) == 0 {
		return nil
	}
	return clientip.CloneIPNets(tp.nets)
}

// Contains reports whether the given IP (string form) falls inside any
// trusted network. Returns false for unparseable input.
func (tp *TrustedProxies) Contains(ipStr string) bool {
	if tp == nil || len(tp.nets) == 0 {
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	for _, n := range tp.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the originating client IP given the direct-connection
// IP (remoteIP, stripped of port) and the X-Forwarded-For header. It
// follows RFC 7239 "right-most-trusted" semantics: walk the XFF list
// right-to-left, returning the first IP that is not itself trusted.
//
// If remoteIP is not trusted, XFF is ignored and remoteIP is returned.
// If the full chain is trusted, the left-most XFF entry is returned.
// If XFF is empty, remoteIP is returned.
func (tp *TrustedProxies) ClientIP(remoteIP, xff string) string {
	if tp == nil || len(tp.nets) == 0 || !tp.Contains(remoteIP) {
		return remoteIP
	}

	ips := splitXFF(xff)
	if len(ips) == 0 {
		return remoteIP
	}

	// Walk right-to-left, return first non-trusted IP.
	for i := len(ips) - 1; i >= 0; i-- {
		ip := ips[i]
		if ip == "" {
			continue
		}
		if !tp.Contains(ip) {
			return ip
		}
	}
	// Whole chain trusted — fall back to the left-most entry.
	return ips[0]
}

// splitXFF splits an X-Forwarded-For header into trimmed entries.
func splitXFF(xff string) []string {
	if xff == "" {
		return nil
	}
	parts := strings.Split(xff, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// stripPortHost removes the port from a RemoteAddr style string. If the
// input has no port it is returned unchanged.
func stripPortHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
