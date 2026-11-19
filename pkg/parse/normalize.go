package parse

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// CanonicalURL is the key a page is deduplicated by: it lowercases scheme/host, strips
// default ports, removes trailing slashes (except root), removes fragments and query strings.
// Does not modify the input *url.URL.
func CanonicalURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	normalized := *u

	normalized.Scheme = strings.ToLower(normalized.Scheme)
	normalized.Host = strings.ToLower(normalized.Host)

	normalized.Host = hostWithoutDefaultPort(normalized.Scheme, normalized.Host)

	if normalized.Path == "" {
		normalized.Path = "/"
	} else if len(normalized.Path) > 1 && strings.HasSuffix(normalized.Path, "/") {
		normalized.Path = normalized.Path[:len(normalized.Path)-1]
	}

	normalized.Fragment = ""
	normalized.RawQuery = ""

	return normalized.String()
}

// SameHost reports whether two hostnames (no port) are the same. Hostnames are
// case-insensitive, and CanonicalURL lowercases the ones it queues, so a plain
// string comparison against a configured or scraped host is not enough.
func SameHost(a, b string) bool {
	return strings.EqualFold(a, b)
}

// FetchURL is the URL to request for u: as linked, minus what never changes
// the response (fragment, an empty "?", a default port, escaped unreserved
// characters, scheme and host case). A trailing slash and index.html stay,
// since some servers answer only the form that was linked.
func FetchURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	f := *u
	f.Scheme = strings.ToLower(f.Scheme)
	f.Host = hostWithoutDefaultPort(f.Scheme, strings.ToLower(f.Host))
	f.Fragment, f.RawFragment = "", ""
	if f.RawQuery == "" {
		f.ForceQuery = false
	}
	setEscapedPath(&f, cleanEscapes(u.EscapedPath(), false))
	return f.String()
}

// ParseURL parses an absolute URL and returns its dedup key and the URL to
// fetch. Uses url.Parse (not url.ParseRequestURI) so the fragment separator is
// honored: ParseRequestURI folds "#anchor" into the path, which round-trips as
// "%23anchor" and breaks dedup.
func ParseURL(urlStr string) (key, fetch string, parsed *url.URL, err error) {
	parsed, err = url.Parse(urlStr)
	if err != nil {
		return "", "", nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", "", nil, fmt.Errorf("URL must be absolute (have scheme and host): %q", urlStr)
	}
	return CanonicalURL(parsed), FetchURL(parsed), parsed, nil
}

// hostWithoutDefaultPort drops the scheme's default port, keeping IPv6 brackets.
func hostWithoutDefaultPort(scheme, host string) string {
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		return host
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		if strings.Contains(h, ":") {
			return "[" + h + "]"
		}
		return h
	}
	return host
}

// cleanEscapes decodes %XX escapes of unreserved characters in an escaped
// path; with upperRest it also uppercases the hex of the escapes it keeps.
func cleanEscapes(p string, upperRest bool) string {
	if !strings.Contains(p, "%") {
		return p
	}
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && i+2 < len(p) && isHex(p[i+1]) && isHex(p[i+2]) {
			c := unhex(p[i+1])<<4 | unhex(p[i+2])
			if isUnreserved(c) {
				b.WriteByte(c)
				i += 2
				continue
			}
			if upperRest {
				b.WriteByte('%')
				b.WriteString(strings.ToUpper(p[i+1 : i+3]))
				i += 2
				continue
			}
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

func setEscapedPath(u *url.URL, escaped string) {
	if p, err := url.PathUnescape(escaped); err == nil {
		u.Path, u.RawPath = p, escaped
	}
}

func isUnreserved(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
