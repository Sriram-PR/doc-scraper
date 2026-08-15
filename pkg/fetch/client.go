package fetch

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

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

// NewClient creates an HTTP client with an SSRF-guarding dialer unless allow_private_networks is set.
func NewClient(cfg config.HTTPClientConfig, log *slog.Logger) *http.Client {
	log.Info("Initializing HTTP client...")

	dialer := &net.Dialer{
		Timeout:   dialerTimeout,
		KeepAlive: dialerKeepAlive,
	}

	// Wrap with SSRF guard unless explicitly disabled. Blocks dials to
	// loopback/private/link-local/CGNAT/multicast addresses, including those
	// reached via redirect chains. Resolves once and dials each pre-validated
	// IP directly to prevent DNS-rebinding races.
	dialContext := dialer.DialContext
	if cfg.AllowPrivateNetworks {
		log.Warn("HTTP client: allow_private_networks=true, SSRF guard disabled, dials to private IPs are permitted")
	} else {
		dialContext = SafeDialContext(dialer)
	}

	transport := &http.Transport{
		Proxy:                  http.ProxyFromEnvironment,
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
