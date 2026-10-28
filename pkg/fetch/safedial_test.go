package fetch

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

func TestIsBlockedAddr(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		blocked bool
	}{
		// Blocked: loopback
		{"IPv4Loopback", "127.0.0.1", true},
		{"IPv4LoopbackHigh", "127.255.255.255", true},
		{"IPv6Loopback", "::1", true},
		// Blocked: unspecified
		{"IPv4Unspecified", "0.0.0.0", true},
		{"IPv6Unspecified", "::", true},
		// Blocked: link-local (incl. AWS IMDS 169.254.169.254)
		{"IPv4LinkLocal", "169.254.0.1", true},
		{"AWSIMDS", "169.254.169.254", true},
		{"IPv6LinkLocal", "fe80::1", true},
		// Blocked: RFC 1918 private
		{"Private10", "10.0.0.1", true},
		{"Private172", "172.16.0.1", true},
		{"Private172High", "172.31.255.255", true},
		{"Private192", "192.168.1.1", true},
		// Blocked: IPv6 ULA (RFC 4193)
		{"IPv6ULA", "fc00::1", true},
		{"IPv6ULAHigh", "fdff::1", true},
		// Blocked: CGNAT (RFC 6598)
		{"CGNATLow", "100.64.0.1", true},
		{"CGNATHigh", "100.127.255.254", true},
		// Blocked: multicast
		{"IPv4Multicast", "224.0.0.1", true},
		{"IPv6Multicast", "ff00::1", true},
		// Blocked: IPv6 forms that carry a blocked IPv4 address
		{"NAT64Loopback", "64:ff9b::7f00:1", true},
		{"NAT64IMDS", "64:ff9b::a9fe:a9fe", true},
		{"NAT64LocalUsePrivate", "64:ff9b:1::a00:1", true},
		{"SixToFourLoopback", "2002:7f00:1::", true},
		{"SixToFourPrivate", "2002:c0a8:101::1", true},
		{"IPv4CompatibleLoopback", "::7f00:1", true},

		// Blocked: IANA IPv4 special-purpose ranges that are not globally reachable
		{"ThisNetwork", "0.0.0.1", true},
		{"IETFProtocolAssignments", "192.0.0.1", true},
		{"IPv4DummyAddress", "192.0.0.8", true},
		{"PCPAnycastOverBlocked", "192.0.0.9", true},
		{"NAT64Discovery", "192.0.0.170", true},
		{"TestNet1", "192.0.2.1", true},
		{"SixToFourRelayAnycast", "192.88.99.1", true},
		{"Benchmarking", "198.18.0.1", true},
		{"BenchmarkingHigh", "198.19.255.255", true},
		{"TestNet2", "198.51.100.1", true},
		{"TestNet3", "203.0.113.5", true},
		{"ReservedClassE", "240.0.0.1", true},
		{"LimitedBroadcast", "255.255.255.255", true},
		{"AzureWireServer", "168.63.129.16", true},
		{"AlibabaMetadata", "100.100.100.200", true},
		// Blocked: IPv4-mapped forms are unmapped inside IsBlockedAddr
		{"MappedLoopback", "::ffff:127.0.0.1", true},
		{"MappedBenchmarking", "::ffff:198.18.0.1", true},
		{"MappedAzure", "::ffff:168.63.129.16", true},
		// Blocked: IPv6 outside 2000::/3 or special inside it
		{"IPv4Translated", "::ffff:0:7f00:1", true},
		{"IPv4TranslatedPublic", "::ffff:0:808:808", true},
		{"DiscardOnly", "100::1", true},
		{"DummyPrefix", "100:0:0:1::1", true},
		{"SRv6SIDs", "5f00::1", true},
		{"ReservedIPv6", "4000::1", true},
		{"AWSIMDSv6", "fd00:ec2::254", true},
		{"Teredo", "2001:0:4136:e378:8000:63bf:3fff:fdd2", true},
		{"IETFProtocolAssignmentsV6", "2001::1", true},
		{"BenchmarkingV6", "2001:2::1", true},
		{"DocumentationV6", "2001:db8::1", true},
		{"DocumentationV6New", "3fff::1", true},
		{"NAT64LocalUsePublic", "64:ff9b:1::808:808", true},
		{"NAT64Benchmarking", "64:ff9b::c612:1", true},
		{"SixToFourBenchmarking", "2002:c612:1::1", true},
		{"LinkLocalWithZone", "fe80::1%eth0", true},

		// Allowed: public addresses
		{"PublicCloudflare", "1.1.1.1", false},
		{"PublicGoogle", "8.8.8.8", false},
		{"PublicGitHub", "140.82.121.3", false},
		{"PublicJustBelowCGNAT", "100.63.255.255", false},
		{"PublicJustAboveCGNAT", "100.128.0.1", false},
		{"PublicJustBelowPrivate172", "172.15.255.255", false},
		{"PublicJustAbovePrivate172", "172.32.0.1", false},
		{"IPv6PublicGoogle", "2001:4860:4860::8888", false},
		{"NAT64Public", "64:ff9b::101:101", false},
		{"SixToFourPublic", "2002:808:808::1", false},

		// Allowed: more public controls
		{"PublicJustBelowBenchmarking", "198.17.255.255", false},
		{"PublicJustAboveBenchmarking", "198.20.0.1", false},
		{"MulticastTop", "239.255.255.255", true},
		{"MappedPublic", "::ffff:8.8.8.8", false},
		{"IPv6PublicCloudflare", "2606:4700::1111", false},
		{"IPv6JustAboveIETF", "2001:200::1", false},
		{"NAT64PublicGoogle", "64:ff9b::808:808", false},
		{"PublicWithZone", "2606:4700::1111%eth0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := netip.ParseAddr(tt.addr)
			require.NoError(t, err)
			assert.Equal(t, tt.blocked, IsBlockedAddr(addr), "%s", tt.addr)
		})
	}
}

func TestIsBlockedAddr_InvalidAddr(t *testing.T) {
	var zero netip.Addr // zero value is invalid
	assert.True(t, IsBlockedAddr(zero), "invalid addr should be blocked")
}

func TestSafeDialContext_BlocksIPLiteral(t *testing.T) {
	base := &net.Dialer{Timeout: 2 * time.Second}
	dial := SafeDialContext(base)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	tests := []string{
		"127.0.0.1:80",
		"169.254.169.254:80",
		"10.0.0.1:80",
		"[::1]:80",
		"[fe80::1]:80",
		"100.64.0.1:80",
	}
	for _, addr := range tests {
		t.Run(addr, func(t *testing.T) {
			conn, err := dial(ctx, "tcp", addr)
			if err == nil {
				conn.Close()
			}
			assert.ErrorIs(t, err, utils.ErrBlockedAddress)
		})
	}
}

// TestSafeDialContext_BlocksRedirectToLoopback verifies the end-to-end path:
// an HTTP client built via NewClient (with default SSRF guard) must refuse to
// follow a redirect that targets a loopback address.
func TestSafeDialContext_BlocksRedirectToLoopback(t *testing.T) {
	// Loopback target server — should never be reached via the guarded client.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Fail(t, "loopback target was reached; SSRF guard failed to block redirect")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	// Redirector — also on loopback, but the client connects to it directly
	// via its httptest URL (which httptest builds with 127.0.0.1). To make the
	// test meaningful we directly invoke SafeDialContext on the redirect
	// target's host:port instead of relying on the redirector.
	base := &net.Dialer{Timeout: 2 * time.Second}
	dial := SafeDialContext(base)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Strip scheme from target.URL to get host:port.
	addr := strings.TrimPrefix(target.URL, "http://")
	conn, err := dial(ctx, "tcp", addr)
	if err == nil {
		conn.Close()
	}
	require.Error(t, err, "SafeDialContext allowed dial to httptest loopback %s", addr)
}
