package parse

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// NormalizeURL standardizes a URL for comparison and storage: lowercases scheme/host, strips
// default ports, removes trailing slashes (except root), removes fragments and query strings.
// Does not modify the input *url.URL.
func NormalizeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	normalized := *u

	normalized.Scheme = strings.ToLower(normalized.Scheme)
	normalized.Host = strings.ToLower(normalized.Host)

	host, port, err := net.SplitHostPort(normalized.Host)
	if err == nil {
		if (normalized.Scheme == "http" && port == "80") ||
			(normalized.Scheme == "https" && port == "443") {
			normalized.Host = host
		}
	}

	if normalized.Path == "" {
		normalized.Path = "/"
	} else if len(normalized.Path) > 1 && strings.HasSuffix(normalized.Path, "/") {
		normalized.Path = normalized.Path[:len(normalized.Path)-1]
	}

	normalized.Fragment = ""
	normalized.RawQuery = ""

	return normalized.String()
}

// ParseAndNormalize parses an absolute URL string and normalizes it. Uses
// url.Parse (not url.ParseRequestURI) so the fragment separator is honored:
// ParseRequestURI silently folds "#anchor" into the path or query, which
// round-trips as a percent-encoded "%23anchor" and breaks dedup. We then
// require scheme + host explicitly to keep the "absolute URL only" contract.
func ParseAndNormalize(urlStr string) (string, *url.URL, error) {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return "", nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", nil, fmt.Errorf("URL must be absolute (have scheme and host): %q", urlStr)
	}
	normalizedStr := NormalizeURL(parsed)
	return normalizedStr, parsed, nil
}
