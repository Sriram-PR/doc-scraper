package parse

import (
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

// ParseAndNormalize parses a URL string with url.ParseRequestURI (scheme required) and normalizes it.
// Returns the normalized string, the parsed URL, and any parse error.
func ParseAndNormalize(urlStr string) (string, *url.URL, error) {
	parsed, err := url.ParseRequestURI(urlStr)
	if err != nil {
		return "", nil, err
	}
	normalizedStr := NormalizeURL(parsed)
	return normalizedStr, parsed, nil
}
