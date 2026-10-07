// Package neturl provides the host and URL helpers used by the
// framework's SSRF defenses. It is internal by design: SSRF policy lives
// behind explicit options on packages like httpclient and notification;
// do not add transitive consumers.
//
// The decision whether an IP address is internal is made in one place,
// internal/ipclass, which lists the ranges and the IPv6 forms that carry
// an IPv4 address. This package resolves hosts and parses URLs and asks
// ipclass about each address; IsPrivateOrInternal and IsMetadataIP are
// kept here as pass-throughs for the callers of this package.
package neturl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/ipclass"
)

// ErrPrivateHost is the sentinel returned when a host resolves to a
// disallowed private, loopback, link-local, or metadata address. Callers
// should wrap it with their package prefix before surfacing to callers.
var ErrPrivateHost = errors.New("neturl: host resolves to private or internal address")

// MetadataIPv4 is the well-known cloud instance metadata IPv4 address.
const MetadataIPv4 = ipclass.MetadataIPv4

// MetadataIPv6 is the IPv6 metadata endpoint address.
const MetadataIPv6 = ipclass.MetadataIPv6

// IsMetadataIP reports whether ip matches a well-known cloud metadata
// endpoint, as written or carried inside an IPv6 address. See
// [ipclass.IsMetadataIP].
func IsMetadataIP(ip net.IP) bool { return ipclass.IsMetadataIP(ip) }

// IsPrivateOrInternal reports whether ip falls in any range that should
// never be reachable from an outbound request in normal operation, as
// written or carried inside an IPv6 address. See
// [ipclass.IsPrivateOrInternal] for the list.
func IsPrivateOrInternal(ip net.IP) bool { return ipclass.IsPrivateOrInternal(ip) }

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
		return false, errchain.Errorf("neturl: resolve %q: %w", host, err)
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
		return errchain.Errorf("neturl: parse url: %w", err)
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
		return errchain.Errorf("%w: %s", ErrPrivateHost, host)
	}
	return nil
}

// ETLDPlusOne returns the Public Suffix List-backed eTLD+1 for host via
// publicsuffix.EffectiveTLDPlusOne. Literal IPs are returned in canonical
// form. On PSL lookup failure, including public suffixes with no registrable
// label, it returns the full normalized host so comparisons fail closed. Use
// only for cross-host comparison where a false-negative (stripping too
// aggressively) is safer than a false-positive.
func ETLDPlusOne(host string) string {
	host = strings.ToLower(host)
	if strings.HasPrefix(host, "[") {
		if i := strings.LastIndex(host, "]"); i >= 0 {
			host = host[1:i]
		}
	} else if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if i := strings.LastIndex(host, ":"); i >= 0 && strings.Count(host, ":") == 1 {
		host = host[:i]
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	etldPlusOne, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return etldPlusOne
}
