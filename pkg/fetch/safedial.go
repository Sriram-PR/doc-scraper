package fetch

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// cgnatRange is RFC 6598 Carrier-Grade NAT space (100.64.0.0/10). Go's
// netip.Addr.IsPrivate does not include this range, but it should be blocked
// for SSRF defense since it commonly addresses internal infrastructure.
var cgnatRange = netip.MustParsePrefix("100.64.0.0/10")

var (
	nat64Range         = netip.MustParsePrefix("64:ff9b::/96")
	nat64LocalUseRange = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFourRange     = netip.MustParsePrefix("2002::/16")
	ipv4CompatRange    = netip.MustParsePrefix("::/96")
)

// embeddedIPv4 returns the IPv4 address an IPv6 translation or tunnel address
// routes to, so a gateway on the path cannot be used to reach a blocked one.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	if !addr.Is6() {
		return netip.Addr{}, false
	}
	b := addr.As16()
	switch {
	case nat64Range.Contains(addr), nat64LocalUseRange.Contains(addr), ipv4CompatRange.Contains(addr):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case sixToFourRange.Contains(addr):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}

// IsBlockedAddr reports whether the given IP address should be rejected from
// outbound HTTP dials to prevent SSRF against internal infrastructure. It
// blocks loopback, unspecified, link-local, private (RFC 1918 / RFC 4193),
// CGNAT, and multicast addresses, and NAT64, 6to4 and IPv4-compatible IPv6
// addresses that carry one of them.
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
	if v4, ok := embeddedIPv4(addr); ok {
		return IsBlockedAddr(v4)
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
				return nil, fmt.Errorf("%w: %s (private/loopback/link-local/cgnat/multicast)", utils.ErrBlockedAddress, parsed)
			}
			return base.DialContext(ctx, network, addr)
		}

		ips, err := resolveAllowed(ctx, resolverLookup(base.Resolver), host)
		if err != nil {
			return nil, err
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

func resolverLookup(r *net.Resolver) lookupFunc {
	if r == nil {
		r = net.DefaultResolver
	}
	return func(ctx context.Context, host string) ([]netip.Addr, error) {
		return r.LookupNetIP(ctx, "ip", host)
	}
}

// resolveAllowed resolves host and fails if any of its addresses is blocked,
// so a name with one public and one private record cannot slip through.
func resolveAllowed(ctx context.Context, lookup lookupFunc, host string) ([]netip.Addr, error) {
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("safedial: resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("safedial: no addresses for %q", host)
	}
	for _, ip := range ips {
		if IsBlockedAddr(ip.Unmap()) {
			return nil, fmt.Errorf("%w: %q resolved to %s", utils.ErrBlockedAddress, host, ip)
		}
	}
	return ips, nil
}

// checkHost applies the guard to a host that will be reached through a proxy,
// where the dial only sees the proxy's address.
func checkHost(ctx context.Context, lookup lookupFunc, host string) error {
	if parsed, err := netip.ParseAddr(host); err == nil {
		if IsBlockedAddr(parsed.Unmap()) {
			return fmt.Errorf("%w: %s (private/loopback/link-local/cgnat/multicast)", utils.ErrBlockedAddress, parsed)
		}
		return nil
	}
	_, err := resolveAllowed(ctx, lookup, host)
	return err
}
