package fetch

import (
	"context"
	"fmt"
	"net"
	"net/netip"
)

// cgnatRange is RFC 6598 Carrier-Grade NAT space (100.64.0.0/10). Go's
// netip.Addr.IsPrivate does not include this range, but it should be blocked
// for SSRF defense since it commonly addresses internal infrastructure.
var cgnatRange = netip.MustParsePrefix("100.64.0.0/10")

// IsBlockedAddr reports whether the given IP address should be rejected from
// outbound HTTP dials to prevent SSRF against internal infrastructure. It
// blocks loopback, unspecified, link-local, private (RFC 1918 / RFC 4193),
// CGNAT, and multicast addresses.
func IsBlockedAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	if addr.IsLoopback() ||
		addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsPrivate() ||
		addr.IsMulticast() ||
		addr.IsInterfaceLocalMulticast() {
		return true
	}
	if addr.Is4() && cgnatRange.Contains(addr) {
		return true
	}
	return false
}

// SafeDialContext wraps the given base dialer with a DialContext that resolves
// the destination hostname, rejects connections to private/loopback/link-local/
// CGNAT/multicast addresses, and dials each pre-validated IP directly to
// prevent DNS-rebinding races between the check and the connect.
//
// HTTPS still works because http.Transport sets TLS ServerName from the
// request URL, not from the dial target.
func SafeDialContext(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("safedial: parse host/port %q: %w", addr, err)
		}

		if parsed, perr := netip.ParseAddr(host); perr == nil {
			if IsBlockedAddr(parsed.Unmap()) {
				return nil, fmt.Errorf("safedial: blocked address %s (private/loopback/link-local/cgnat/multicast)", parsed)
			}
			return base.DialContext(ctx, network, addr)
		}

		resolver := base.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		ips, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("safedial: resolve %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("safedial: no addresses for %q", host)
		}
		for _, ip := range ips {
			if IsBlockedAddr(ip.Unmap()) {
				return nil, fmt.Errorf("safedial: %q resolved to blocked address %s", host, ip)
			}
		}

		var firstErr error
		for _, ip := range ips {
			conn, dErr := base.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dErr == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = dErr
			}
		}
		return nil, firstErr
	}
}
