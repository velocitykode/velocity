// Package clientip resolves the originating client IP for an inbound
// *http.Request given a list of trusted proxy networks.
//
// It is the single source of truth for "who is the real client?" across
// the framework. Both the auth login throttler (auth/throttle.go) and the
// session scheme (auth/drivers/guards/session.go) call Extract so the
// throttle key, the audit-trail IP recorded on Login, and the per-IP
// rate limiter all agree. Other layers (exceptions, rate-limit, ws
// origin checks) should adopt it next so the framework has one IP
// extraction policy, not three.
//
// Policy:
//
//   - If RemoteAddr's IP is NOT in trustedProxies, forwarded headers
//     are ignored, and Extract returns the RemoteAddr IP. This is the
//     "direct connection" fallback and is the secure default.
//
//   - When RemoteAddr IS trusted, headers are honoured in this order:
//
//     1. Forwarded (RFC 7239): the right-most `for=` whose IP is not
//     itself trusted wins. If every entry is trusted, the left-most
//     entry's IP is returned.
//
//     2. X-Forwarded-For: same right-most-of-trusted semantics.
//
//     3. X-Real-IP: parsed as a single IP. Multi-value input is
//     rejected to close the header-spoofing vector where an
//     attacker sends "X-Real-IP: 1.2.3.4, 5.6.7.8".
//
//   - Loopback/private IPs that appear in a forwarded chain are not
//     blindly trusted: only IPs that match an entry in trustedProxies
//     are skipped during right-most resolution. An attacker that
//     prepends `X-Forwarded-For: 127.0.0.1` therefore does not get
//     loopback unless 127.0.0.1 is explicitly listed as a trusted
//     proxy.
//
// Returns nil only when RemoteAddr is unparseable AND no usable header
// is present. Callers should treat nil as "unknown" and avoid keying
// security decisions on it.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// Extract returns the originating client IP for r given the configured
// trusted proxy networks. See package doc for the resolution policy.
//
// trustedProxies may be nil or empty. In that case Extract always
// returns the IP parsed from r.RemoteAddr, never honouring forwarded
// headers (secure default; matches Laravel's behaviour before
// TrustProxies is configured).
//
// Returns nil when r is nil or RemoteAddr is unparseable.
func Extract(r *http.Request, trustedProxies []*net.IPNet) net.IP {
	if r == nil {
		return nil
	}

	remoteIP := parseRemoteIP(r.RemoteAddr)
	if remoteIP == nil {
		// RemoteAddr unparseable. Don't fall through to headers, a
		// missing/garbage RemoteAddr almost always means a misconfigured
		// upstream, not "trust this attacker-controlled header".
		return nil
	}

	// Direct connection (no trusted proxies, or RemoteAddr not among
	// them): return RemoteAddr IP verbatim. Headers are NOT honoured.
	if len(trustedProxies) == 0 || !ipInNets(remoteIP, trustedProxies) {
		return remoteIP
	}

	// RFC 7239 Forwarded header takes precedence (newer, structured,
	// less prone to comma-split confusion than XFF).
	if fwd := r.Header.Get("Forwarded"); fwd != "" {
		if ip := resolveForwarded(fwd, trustedProxies); ip != nil {
			return ip
		}
	}

	// Legacy X-Forwarded-For: right-most-of-trusted semantics.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip := resolveXFF(xff, trustedProxies); ip != nil {
			return ip
		}
	}

	// Single-value X-Real-IP. Reject multi-value input to close the
	// throttle-key spoofing vector documented at router/rate_limit.go.
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if ip := parseSingleIP(xri); ip != nil {
			return ip
		}
	}

	return remoteIP
}

// ExtractString is a convenience wrapper that returns Extract's result
// formatted as a string. Returns "" when Extract returns nil. Useful
// for callers (log fields, key derivation) that want a string and do
// not need to inspect the net.IP value.
func ExtractString(r *http.Request, trustedProxies []*net.IPNet) string {
	ip := Extract(r, trustedProxies)
	if ip == nil {
		return ""
	}
	return ip.String()
}

// parseRemoteIP strips a "host:port" suffix from r.RemoteAddr and
// returns the parsed net.IP. Handles IPv4 ("1.2.3.4:54321"), IPv6
// ("[2001:db8::1]:54321"), and bare host forms ("1.2.3.4"). Returns
// nil for unparseable input.
func parseRemoteIP(addr string) net.IP {
	if addr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// May be bare host with no port. SplitHostPort fails on those.
		// Trim any wrapping brackets (uncommon but valid for IPv6).
		host = strings.Trim(addr, "[]")
	}
	return net.ParseIP(strings.TrimSpace(host))
}

// ipInNets reports whether ip is contained in any entry of nets.
func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n == nil {
			continue
		}
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// resolveXFF walks the X-Forwarded-For list right-to-left, returning
// the first IP that is not itself trusted. If the chain is exhausted
// (every hop is trusted) the left-most entry is returned. Returns nil
// when the header has no parseable IPs.
func resolveXFF(xff string, trustedProxies []*net.IPNet) net.IP {
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			continue
		}
		if !ipInNets(ip, trustedProxies) {
			return ip
		}
	}
	// Whole chain trusted, fall back to left-most parseable entry.
	for _, raw := range parts {
		if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
			return ip
		}
	}
	return nil
}

// resolveForwarded parses an RFC 7239 Forwarded header and applies the
// same right-most-of-trusted semantics as resolveXFF over the `for=`
// parameters.
//
// Parsing is intentionally lenient: each comma-separated element is
// scanned for a `for=` parameter; if its value is quoted it is
// unquoted, and a "[v6]" or "[v6]:port" bracket form is reduced to
// the IP. Unparseable values are skipped.
func resolveForwarded(value string, trustedProxies []*net.IPNet) net.IP {
	elements := strings.Split(value, ",")
	parsedFor := make([]net.IP, 0, len(elements))
	for _, el := range elements {
		ip := forwardedFor(el)
		if ip == nil {
			continue
		}
		parsedFor = append(parsedFor, ip)
	}
	if len(parsedFor) == 0 {
		return nil
	}
	for i := len(parsedFor) - 1; i >= 0; i-- {
		if !ipInNets(parsedFor[i], trustedProxies) {
			return parsedFor[i]
		}
	}
	return parsedFor[0]
}

// forwardedFor extracts the IP from a single Forwarded-header element
// (e.g. `for="192.0.2.43:47011";proto=https`). Returns nil when no
// usable IP is present (missing for=, "unknown" sentinel, etc.).
func forwardedFor(element string) net.IP {
	for _, param := range strings.Split(element, ";") {
		param = strings.TrimSpace(param)
		if !strings.HasPrefix(strings.ToLower(param), "for=") {
			continue
		}
		raw := param[len("for="):]
		raw = strings.Trim(raw, "\"")
		// Bracketed IPv6 forms: "[2001:db8::1]" or "[2001:db8::1]:8080".
		if strings.HasPrefix(raw, "[") {
			if end := strings.Index(raw, "]"); end > 0 {
				return net.ParseIP(raw[1:end])
			}
		}
		// Strip "ip:port" form for IPv4; net.SplitHostPort handles both
		// but tolerates the no-port case here without erroring out.
		if host, _, err := net.SplitHostPort(raw); err == nil {
			return net.ParseIP(host)
		}
		return net.ParseIP(strings.TrimSpace(raw))
	}
	return nil
}

// parseSingleIP returns the parsed IP iff value is exactly one IP
// literal (no comma, whitespace, or trailing junk). Returns nil
// otherwise. Matches the policy enforced by router/rate_limit.go for
// X-Real-IP so the throttle layer and the IP-extraction layer agree.
func parseSingleIP(value string) net.IP {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if strings.ContainsAny(trimmed, ", \t") {
		return nil
	}
	return net.ParseIP(trimmed)
}

// CloneIPNets returns a deep copy of src. Every *net.IPNet element
// is independently allocated and its IP / Mask byte slices are
// freshly copied, so subsequent caller-side mutation of any element
// in src (or of its IP / Mask fields) cannot affect the returned
// slice or vice versa.
//
// This is the right defensive-copy primitive at the
// configure-trusted-proxies boundary. A shallow []*net.IPNet copy
// would keep the same pointers, so a caller (or a buggy framework
// path) holding the original slice could still flip every consumer's
// trust decisions at runtime by reassigning an IPNet's fields.
//
// Returns nil when src is nil or empty so the caller's "no trust"
// path is preserved without an extra allocation. nil elements in
// src are skipped (they were never trusted anyway).
func CloneIPNets(src []*net.IPNet) []*net.IPNet {
	if len(src) == 0 {
		return nil
	}
	out := make([]*net.IPNet, 0, len(src))
	for _, n := range src {
		if n == nil {
			continue
		}
		ipCopy := make(net.IP, len(n.IP))
		copy(ipCopy, n.IP)
		maskCopy := make(net.IPMask, len(n.Mask))
		copy(maskCopy, n.Mask)
		out = append(out, &net.IPNet{IP: ipCopy, Mask: maskCopy})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ParseCIDRs parses a list of IP or CIDR strings into a slice of
// *net.IPNet. Useful at boot to translate a TrustedProxies config
// slice into the form Extract accepts. Returns the first parse error
// (with the offending entry quoted) so a malformed config fails
// startup rather than being silently ignored at request time.
//
// An empty input returns (nil, nil). A bare IP is treated as a /32
// (IPv4) or /128 (IPv6) network.
func ParseCIDRs(entries []string) ([]*net.IPNet, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]*net.IPNet, 0, len(entries))
	for _, raw := range entries {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, &parseError{entry: raw, reason: "empty entry"}
		}
		if strings.Contains(raw, "/") {
			_, ipNet, err := net.ParseCIDR(raw)
			if err != nil {
				return nil, &parseError{entry: raw, reason: err.Error()}
			}
			out = append(out, ipNet)
			continue
		}
		ip := net.ParseIP(raw)
		if ip == nil {
			return nil, &parseError{entry: raw, reason: "not an IP or CIDR"}
		}
		mask := net.CIDRMask(32, 32)
		if ip.To4() == nil {
			mask = net.CIDRMask(128, 128)
		}
		out = append(out, &net.IPNet{IP: ip, Mask: mask})
	}
	return out, nil
}

// parseError is returned by ParseCIDRs so callers can format a
// uniform "invalid trusted proxy entry" startup error.
type parseError struct {
	entry  string
	reason string
}

func (e *parseError) Error() string {
	return "clientip: invalid trusted proxy entry \"" + e.entry + "\": " + e.reason
}
