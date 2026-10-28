package fetch

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// Ranges from the IANA IPv4 and IPv6 Special-Purpose Address Registries that
// are not globally reachable, plus public addresses that reach the host's own
// provider.
var blockedIPv4 = mustPrefixes(
	"0.0.0.0/8",        // "This network"
	"10.0.0.0/8",       // Private-Use
	"100.64.0.0/10",    // Shared Address Space (CGNAT)
	"127.0.0.0/8",      // Loopback
	"169.254.0.0/16",   // Link Local, incl. cloud metadata 169.254.169.254
	"172.16.0.0/12",    // Private-Use
	"192.0.0.0/24",     // IETF Protocol Assignments
	"192.0.2.0/24",     // Documentation (TEST-NET-1)
	"192.88.99.0/24",   // Deprecated 6to4 Relay Anycast
	"192.168.0.0/16",   // Private-Use
	"198.18.0.0/15",    // Benchmarking
	"198.51.100.0/24",  // Documentation (TEST-NET-2)
	"203.0.113.0/24",   // Documentation (TEST-NET-3)
	"224.0.0.0/4",      // Multicast
	"240.0.0.0/4",      // Reserved, incl. Limited Broadcast
	"168.63.129.16/32", // Azure host endpoint: public space, reachable only from inside Azure
)

var (
	// 2000::/3 is the only IPv6 block IANA allocates as Global Unicast.
	globalUnicastIPv6 = netip.MustParsePrefix("2000::/3")
	blockedIPv6       = mustPrefixes(
		"2001::/23",     // IETF Protocol Assignments, incl. Teredo and Benchmarking
		"2001:db8::/32", // Documentation
		"3fff::/20",     // Documentation
	)
	nat64Range     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFourRange = netip.MustParsePrefix("2002::/16")
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		prefixes[i] = netip.MustParsePrefix(c)
	}
	return prefixes
}

func inAny(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// embeddedIPv4 returns the IPv4 address a NAT64 or 6to4 address routes to, so
// a gateway on the path cannot be used to reach a blocked one.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	b := addr.As16()
	switch {
	case nat64Range.Contains(addr):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case sixToFourRange.Contains(addr):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}

// IsBlockedAddr reports whether the given IP address should be rejected from
// outbound HTTP dials to prevent SSRF against internal infrastructure. Only
// globally reachable addresses are allowed; NAT64 and 6to4 addresses are
// judged by the IPv4 address they carry.
func IsBlockedAddr(addr netip.Addr) bool {
	addr = addr.WithZone("").Unmap()
	if !addr.IsValid() {
		return true
	}
	if addr.Is4() {
		return inAny(blockedIPv4, addr)
	}
	if v4, ok := embeddedIPv4(addr); ok {
		return IsBlockedAddr(v4)
	}
	return !globalUnicastIPv6.Contains(addr) || inAny(blockedIPv6, addr)
}

// SafeDialContext wraps the given base dialer with a DialContext that resolves
// the destination hostname, rejects connections to addresses that are
// not globally reachable, and dials each pre-validated IP directly to
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
				return nil, fmt.Errorf("%w: %s (not a public address)", utils.ErrBlockedAddress, parsed)
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
			return fmt.Errorf("%w: %s (not a public address)", utils.ErrBlockedAddress, parsed)
		}
		return nil
	}
	_, err := resolveAllowed(ctx, lookup, host)
	return err
}
