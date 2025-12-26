package parse

import (
	"net/url"
	"testing"
)

func TestNormalizeURL_NilInput(t *testing.T) {
	result := NormalizeURL(nil)
	if result != "" {
		t.Errorf("NormalizeURL(nil) = %q, want empty string", result)
	}
}

func TestNormalizeURL_SchemeAndHostLowercase(t *testing.T) {
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
			result := NormalizeURL(parsed)
			if result != tt.expected {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeURL_DefaultPorts(t *testing.T) {
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
			result := NormalizeURL(parsed)
			if result != tt.expected {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeURL_PathNormalization(t *testing.T) {
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
			result := NormalizeURL(parsed)
			if result != tt.expected {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeURL_FragmentsRemoved(t *testing.T) {
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
			result := NormalizeURL(parsed)
			if result != tt.expected {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeURL_QueryStringsRemoved(t *testing.T) {
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
			result := NormalizeURL(parsed)
			if result != tt.expected {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeURL_DoesNotModifyInput(t *testing.T) {
	input := "HTTP://EXAMPLE.COM:80/path/?q=test#section"
	parsed, _ := url.Parse(input)

	// Save original values
	origScheme := parsed.Scheme
	origHost := parsed.Host
	origPath := parsed.Path
	origFragment := parsed.Fragment
	origQuery := parsed.RawQuery

	_ = NormalizeURL(parsed)

	// Verify input was not modified
	if parsed.Scheme != origScheme {
		t.Errorf("NormalizeURL modified input Scheme: %q -> %q", origScheme, parsed.Scheme)
	}
	if parsed.Host != origHost {
		t.Errorf("NormalizeURL modified input Host: %q -> %q", origHost, parsed.Host)
	}
	if parsed.Path != origPath {
		t.Errorf("NormalizeURL modified input Path: %q -> %q", origPath, parsed.Path)
	}
	if parsed.Fragment != origFragment {
		t.Errorf("NormalizeURL modified input Fragment: %q -> %q", origFragment, parsed.Fragment)
	}
	if parsed.RawQuery != origQuery {
		t.Errorf("NormalizeURL modified input RawQuery: %q -> %q", origQuery, parsed.RawQuery)
	}
}

// --- ParseAndNormalize Tests ---

func TestParseAndNormalize_ValidURLs(t *testing.T) {
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
			resultStr, parsedURL, err := ParseAndNormalize(tt.input)
			if err != nil {
				t.Fatalf("ParseAndNormalize(%q) unexpected error: %v", tt.input, err)
			}
			if resultStr != tt.expectedStr {
				t.Errorf("ParseAndNormalize(%q) string = %q, want %q", tt.input, resultStr, tt.expectedStr)
			}
			if parsedURL == nil {
				t.Errorf("ParseAndNormalize(%q) returned nil URL", tt.input)
			}
		})
	}
}

func TestParseAndNormalize_InvalidURLs(t *testing.T) {
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
			resultStr, parsedURL, err := ParseAndNormalize(tt.input)
			if err == nil {
				t.Errorf("ParseAndNormalize(%q) expected error, got nil", tt.input)
			}
			if resultStr != "" {
				t.Errorf("ParseAndNormalize(%q) string = %q, want empty", tt.input, resultStr)
			}
			if parsedURL != nil {
				t.Errorf("ParseAndNormalize(%q) URL = %v, want nil", tt.input, parsedURL)
			}
		})
	}
}

func TestParseAndNormalize_AbsolutePath(t *testing.T) {
	// url.ParseRequestURI accepts absolute paths (they're valid request URIs)
	resultStr, parsedURL, err := ParseAndNormalize("/path/to/page")
	if err != nil {
		t.Fatalf("ParseAndNormalize(/path/to/page) unexpected error: %v", err)
	}
	if parsedURL == nil {
		t.Fatal("ParseAndNormalize(/path/to/page) returned nil URL")
	}
	// Absolute path without host normalizes to just the path
	if resultStr != "/path/to/page" {
		t.Errorf("ParseAndNormalize(/path/to/page) = %q, want /path/to/page", resultStr)
	}
}

func TestParseAndNormalize_EdgeCases(t *testing.T) {
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
			resultStr, _, err := ParseAndNormalize(tt.input)
			if err != nil {
				t.Fatalf("ParseAndNormalize(%q) unexpected error: %v", tt.input, err)
			}
			if resultStr != tt.expectedStr {
				t.Errorf("ParseAndNormalize(%q) = %q, want %q", tt.input, resultStr, tt.expectedStr)
			}
		})
	}
}
