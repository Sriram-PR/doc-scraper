package parse

import (
	"net"
	"net/url"
	"strings"
)

// NormalizeURL standardizes a URL for comparison and storage
// It lowercases the scheme and host, removes default ports (80 for http, 443 for https), removes trailing slashes from paths (unless root "/"), ensures empty path becomes "/", and removes fragments and query strings
// Does not modify the input *url.URL
func NormalizeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	// Work on a copy
	normalized := *u

	normalized.Scheme = strings.ToLower(normalized.Scheme)
	normalized.Host = strings.ToLower(normalized.Host)

	// Remove default ports
	host, port, err := net.SplitHostPort(normalized.Host)
	if err == nil { // Host included a port
		if (normalized.Scheme == "http" && port == "80") ||
			(normalized.Scheme == "https" && port == "443") {
			normalized.Host = host // Use hostname without default port
		}
	} // If no port or error, Host remains unchanged

	// Handle path normalization
	if normalized.Path == "" {
		normalized.Path = "/" // Ensure empty path becomes "/"
	} else if len(normalized.Path) > 1 && strings.HasSuffix(normalized.Path, "/") {
		normalized.Path = normalized.Path[:len(normalized.Path)-1] // Remove trailing slash
	}

	normalized.Fragment = "" // Remove fragment
	normalized.RawQuery = "" // Remove query string

	return normalized.String()
}

// ParseAndNormalize parses a URL string using the stricter url.ParseRequestURI (requiring a scheme) and then normalizes it using NormalizeURL
// Returns the normalized string, the parsed URL object, and any parse error
func ParseAndNormalize(urlStr string) (string, *url.URL, error) {
	parsed, err := url.ParseRequestURI(urlStr) // Stricter parsing
	if err != nil {
		return "", nil, err
	}
	normalizedStr := NormalizeURL(parsed)
	return normalizedStr, parsed, nil
}
