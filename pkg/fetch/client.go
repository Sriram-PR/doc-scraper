package fetch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/idna"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

// Baked-in HTTP transport timings. These were exposed as config knobs prior to
// v2.0; in practice nobody tuned them and Go's defaults are appropriate for a
// doc scraper, so they are now constants. If you genuinely need to change one,
// edit this file rather than threading another YAML key through.
const (
	maxIdleConns          = 100
	idleConnTimeout       = 90 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	expectContinueTimeout = 1 * time.Second
	dialerTimeout         = 15 * time.Second
	dialerKeepAlive       = 30 * time.Second
)

type lookupFunc func(ctx context.Context, host string) ([]netip.Addr, error)

// NewClient creates an HTTP client with an SSRF-guarding dialer unless allow_private_networks is set.
func NewClient(cfg config.HTTPClientConfig, log *slog.Logger) *http.Client {
	return newClient(cfg, log, httpproxy.FromEnvironment(), nil)
}

func newClient(cfg config.HTTPClientConfig, log *slog.Logger, proxyCfg *httpproxy.Config, lookup lookupFunc) *http.Client {
	log.Info("Initializing HTTP client...")
	proxyFunc := proxyCfg.ProxyFunc()

	dialer := &net.Dialer{
		Timeout:   dialerTimeout,
		KeepAlive: dialerKeepAlive,
	}

	// Wrap with SSRF guard unless explicitly disabled. Blocks dials to
	// loopback/private/link-local/CGNAT/multicast addresses, including those
	// reached via redirect chains. Resolves once and dials each pre-validated
	// IP directly to prevent DNS-rebinding races.
	proxy := func(req *http.Request) (*url.URL, error) { return proxyFunc(req.URL) }
	dialContext := dialer.DialContext
	if cfg.AllowPrivateNetworks {
		log.Warn("HTTP client: allow_private_networks=true, SSRF guard disabled, dials to private IPs are permitted")
	} else {
		if lookup == nil {
			lookup = resolverLookup(dialer.Resolver)
		}
		proxy, dialContext = guardProxied(proxyFunc, SafeDialContext(dialer), dialer.DialContext, lookup)
	}

	transport := &http.Transport{
		Proxy:                  proxy,
		DialContext:            dialContext,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           maxIdleConns,
		MaxIdleConnsPerHost:    cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:        idleConnTimeout,
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		ExpectContinueTimeout:  expectContinueTimeout,
		MaxResponseHeaderBytes: 1 << 20, // 1 MiB
		WriteBufferSize:        4096,
		ReadBufferSize:         4096,
		DisableKeepAlives:      false,
	}

	client := &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			log.Debug(fmt.Sprintf("Redirecting: %s -> %s (hop %d)", via[len(via)-1].URL, req.URL, len(via)))
			return nil
		},
	}
	log.Info("HTTP client initialized.")
	return client
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// guardProxied extends the SSRF guard to proxied requests. Through a proxy the
// dial only sees the proxy's address, so the target is checked here instead,
// before it is handed to the proxy. The proxy itself is the operator's choice
// and may sit on a private address, so dials to it skip the guard. The proxy
// resolves the target's name on its own, so a name that resolves differently
// for the proxy than for us is not caught.
func guardProxied(proxyFunc func(*url.URL) (*url.URL, error), guarded, unguarded dialFunc, lookup lookupFunc) (func(*http.Request) (*url.URL, error), dialFunc) {
	var proxies sync.Map
	proxy := func(req *http.Request) (*url.URL, error) {
		proxyURL, err := proxyFunc(req.URL)
		if err != nil {
			return nil, err
		}
		if proxyURL == nil {
			// Naming the proxy as the target of a direct request must not
			// inherit the proxy's exemption.
			if _, isProxy := proxies.Load(canonicalAddr(req.URL)); isProxy {
				return nil, checkHost(req.Context(), lookup, req.URL.Hostname())
			}
			return nil, nil
		}
		if err := checkHost(req.Context(), lookup, req.URL.Hostname()); err != nil {
			return nil, err
		}
		proxies.Store(canonicalAddr(proxyURL), struct{}{})
		return proxyURL, nil
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if _, isProxy := proxies.Load(addr); isProxy {
			return unguarded(ctx, network, addr)
		}
		return guarded(ctx, network, addr)
	}
	return proxy, dial
}

// canonicalAddr matches the host:port net/http dials for a URL, which only
// IDNA-converts non-ASCII hosts and otherwise keeps them as written.
func canonicalAddr(u *url.URL) string {
	host := u.Hostname()
	if !isASCII(host) {
		if ascii, err := idna.Lookup.ToASCII(host); err == nil {
			host = ascii
		}
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		}
	}
	return net.JoinHostPort(host, port)
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
