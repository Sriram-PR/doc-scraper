package fetch

import (
	"errors"
	"net"
	"net/http"

	"doc-scraper/pkg/config"

	"github.com/sirupsen/logrus"
)

// NewClient creates a new HTTP client based on the provided configuration.
func NewClient(cfg config.HTTPClientConfig, log *logrus.Logger) *http.Client {
	log.Info("Initializing HTTP client...")

	// Create custom dialer with configured timeouts
	dialer := &net.Dialer{
		Timeout:   cfg.DialerTimeout,
		KeepAlive: cfg.DialerKeepAlive,
		// DualStack support is enabled by default
	}

	// Create custom transport using configured settings
	transport := &http.Transport{
		Proxy:                  http.ProxyFromEnvironment, // Use system proxy settings
		DialContext:            dialer.DialContext,        // Use our custom dialer
		ForceAttemptHTTP2:      true,                      // Default to true unless explicitly disabled
		MaxIdleConns:           cfg.MaxIdleConns,
		MaxIdleConnsPerHost:    cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:        cfg.IdleConnTimeout,
		TLSHandshakeTimeout:    cfg.TLSHandshakeTimeout,
		ExpectContinueTimeout:  cfg.ExpectContinueTimeout,
		MaxResponseHeaderBytes: 1 << 20, // Default: 1MB max header size
		WriteBufferSize:        4096,    // Default
		ReadBufferSize:         4096,    // Default
		DisableKeepAlives:      false,   // Keep-alives enabled by default
	}
	// Handle explicit setting for ForceAttemptHTTP2 if provided
	if cfg.ForceAttemptHTTP2 != nil {
		transport.ForceAttemptHTTP2 = *cfg.ForceAttemptHTTP2
	}

	client := &http.Client{
		Timeout:   cfg.Timeout, // Use configured overall timeout
		Transport: transport,   // Use our custom transport
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Default Go behavior is 10 redirects max
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			log.Debugf("Redirecting: %s -> %s (hop %d)", via[len(via)-1].URL, req.URL, len(via))
			return nil // Allow redirect
		},
	}
	log.Info("HTTP client initialized.")
	return client
}
