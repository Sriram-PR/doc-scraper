package parse

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalURL_NilInput(t *testing.T) {
	assert.Empty(t, CanonicalURL(nil))
}

func TestCanonicalURL_SchemeAndHostLowercase(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "UppercaseScheme",
			input:    "HTTP://example.com/path",
			expected: "http://example.com/path",
		},
		{
			name:     "UppercaseHost",
			input:    "http://EXAMPLE.COM/path",
			expected: "http://example.com/path",
		},
		{
			name:     "MixedCase",
			input:    "HTTPS://Example.COM/Path",
			expected: "https://example.com/Path", // Path case preserved
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.input)
			assert.Equal(t, tt.expected, CanonicalURL(parsed), "input %q", tt.input)
		})
	}
}

func TestCanonicalURL_DefaultPorts(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "HTTPPort80Removed",
			input:    "http://example.com:80/path",
			expected: "http://example.com/path",
		},
		{
			name:     "HTTPSPort443Removed",
			input:    "https://example.com:443/path",
			expected: "https://example.com/path",
		},
		{
			name:     "HTTPPort8080Kept",
			input:    "http://example.com:8080/path",
			expected: "http://example.com:8080/path",
		},
		{
			name:     "HTTPSPort8443Kept",
			input:    "https://example.com:8443/path",
			expected: "https://example.com:8443/path",
		},
		{
			name:     "HTTPPort443Kept",
			input:    "http://example.com:443/path",
			expected: "http://example.com:443/path", // Non-default for HTTP
		},
		{
			name:     "HTTPSPort80Kept",
			input:    "https://example.com:80/path",
			expected: "https://example.com:80/path", // Non-default for HTTPS
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.input)
			assert.Equal(t, tt.expected, CanonicalURL(parsed), "input %q", tt.input)
		})
	}
}

func TestCanonicalURL_PathNormalization(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "EmptyPathBecomesSlash",
			input:    "http://example.com",
			expected: "http://example.com/",
		},
		{
			name:     "RootPathKept",
			input:    "http://example.com/",
			expected: "http://example.com/",
		},
		{
			name:     "TrailingSlashRemoved",
			input:    "http://example.com/path/",
			expected: "http://example.com/path",
		},
		{
			name:     "DeepPathTrailingSlashRemoved",
			input:    "http://example.com/a/b/c/",
			expected: "http://example.com/a/b/c",
		},
		{
			name:     "NoTrailingSlash",
			input:    "http://example.com/path",
			expected: "http://example.com/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.input)
			assert.Equal(t, tt.expected, CanonicalURL(parsed), "input %q", tt.input)
		})
	}
}

func TestCanonicalURL_FragmentsRemoved(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "SimpleFragment",
			input:    "http://example.com/page#section",
			expected: "http://example.com/page",
		},
		{
			name:     "FragmentWithPath",
			input:    "http://example.com/docs/api#method",
			expected: "http://example.com/docs/api",
		},
		{
			name:     "FragmentOnly",
			input:    "http://example.com/#top",
			expected: "http://example.com/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.input)
			assert.Equal(t, tt.expected, CanonicalURL(parsed), "input %q", tt.input)
		})
	}
}

func TestCanonicalURL_QueryStringsRemoved(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "SimpleQuery",
			input:    "http://example.com/search?q=test",
			expected: "http://example.com/search",
		},
		{
			name:     "MultipleParams",
			input:    "http://example.com/page?a=1&b=2&c=3",
			expected: "http://example.com/page",
		},
		{
			name:     "QueryAndFragment",
			input:    "http://example.com/page?q=test#section",
			expected: "http://example.com/page",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.input)
			assert.Equal(t, tt.expected, CanonicalURL(parsed), "input %q", tt.input)
		})
	}
}

func TestCanonicalURL_DoesNotModifyInput(t *testing.T) {
	input := "HTTP://EXAMPLE.COM:80/path/?q=test#section"
	parsed, _ := url.Parse(input)

	// Save original values
	origScheme := parsed.Scheme
	origHost := parsed.Host
	origPath := parsed.Path
	origFragment := parsed.Fragment
	origQuery := parsed.RawQuery

	_ = CanonicalURL(parsed)

	// Verify input was not modified
	assert.Equal(t, origScheme, parsed.Scheme)
	assert.Equal(t, origHost, parsed.Host)
	assert.Equal(t, origPath, parsed.Path)
	assert.Equal(t, origFragment, parsed.Fragment)
	assert.Equal(t, origQuery, parsed.RawQuery)
}

func TestParseURL_ValidURLs(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectedStr string
	}{
		{
			name:        "SimpleHTTP",
			input:       "http://example.com/path",
			expectedStr: "http://example.com/path",
		},
		{
			name:        "HTTPSWithPort",
			input:       "https://example.com:443/page",
			expectedStr: "https://example.com/page",
		},
		{
			name:        "WithQueryAndFragment",
			input:       "http://example.com/page?q=1#top",
			expectedStr: "http://example.com/page",
		},
		{
			name:        "UppercaseNormalized",
			input:       "HTTP://EXAMPLE.COM/PATH",
			expectedStr: "http://example.com/PATH",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultStr, _, parsedURL, err := ParseURL(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedStr, resultStr)
			assert.NotNil(t, parsedURL)
		})
	}
}

func TestParseURL_InvalidURLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"NoScheme", "example.com/path"},
		{"EmptyString", ""},
		{"RelativeURL", "path/to/page"},
		{"InvalidScheme", "://example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultStr, _, parsedURL, err := ParseURL(tt.input)
			require.Error(t, err)
			assert.Empty(t, resultStr)
			assert.Nil(t, parsedURL)
		})
	}
}

func TestParseURL_AbsolutePathRejected(t *testing.T) {
	// Bare absolute paths (no scheme/host) are not crawlable URLs and are
	// rejected by ParseURL so callers don't end up with rows in the
	// visited DB that can't be fetched.
	_, _, _, err := ParseURL("/path/to/page")
	require.Error(t, err)
}

func TestParseURL_FragmentStripped(t *testing.T) {
	// Regression: ParseRequestURI absorbed "#foo" into the path and
	// round-tripped as "%23foo", which polluted the visited DB with
	// uncrawlable URLs. Parse + clear-fragment must produce a clean URL.
	tests := []struct {
		input    string
		expected string
	}{
		{"https://example.com/page#section", "https://example.com/page"},
		{"https://example.com/foo.html#a", "https://example.com/foo.html"},
		{"https://example.com/page?q=1#top", "https://example.com/page"},
	}
	for _, tt := range tests {
		got, _, _, err := ParseURL(tt.input)
		require.NoError(t, err, "input %q", tt.input)
		assert.Equal(t, tt.expected, got, "input %q", tt.input)
	}
}

func TestParseURL_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectedStr string
	}{
		{
			name:        "RootOnly",
			input:       "http://example.com",
			expectedStr: "http://example.com/",
		},
		{
			name:        "TrailingSlashOnly",
			input:       "http://example.com/",
			expectedStr: "http://example.com/",
		},
		{
			name:        "IPv4Host",
			input:       "http://192.168.1.1/path",
			expectedStr: "http://192.168.1.1/path",
		},
		{
			name:        "IPv4WithPort",
			input:       "http://192.168.1.1:8080/path",
			expectedStr: "http://192.168.1.1:8080/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultStr, _, _, err := ParseURL(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedStr, resultStr)
		})
	}
}

func TestSameHost(t *testing.T) {
	assert.True(t, SameHost("docs.example.com", "docs.example.com"))
	assert.True(t, SameHost("Docs.Example.COM", "docs.example.com"))
	assert.True(t, SameHost("docs.example.com", "DOCS.EXAMPLE.COM"))
	assert.False(t, SameHost("docs.example.com", "example.com"))
	assert.False(t, SameHost("docs.example.com", ""))
}

func TestFetchURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://h/book/", "https://h/book/"},
		{"https://h/book/index.html", "https://h/book/index.html"},
		{"https://h/d/A?", "https://h/d/A"},
		{"https://h/d/%41", "https://h/d/A"},
		{"https://h/d/%2F", "https://h/d/%2F"},
		{"https://h/d/%2f", "https://h/d/%2f"},
		{"https://h/s?q=1", "https://h/s?q=1"},
		{"https://h/p#frag", "https://h/p"},
		{"HTTPS://H:443/p", "https://h/p"},
		{"https://[::1]:443/x", "https://[::1]/x"},
		{"http://[::1]:8080/x", "http://[::1]:8080/x"},
	} {
		u, err := url.Parse(tc.in)
		require.NoError(t, err)
		assert.Equal(t, tc.want, FetchURL(u), tc.in)
	}
}

func TestParseURL_ReturnsKeyAndFetch(t *testing.T) {
	key, fetch, parsed, err := ParseURL("https://h/docs/")
	require.NoError(t, err)
	assert.Equal(t, "https://h/docs", key)
	assert.Equal(t, "https://h/docs/", fetch)
	assert.Equal(t, "/docs/", parsed.Path)
}
