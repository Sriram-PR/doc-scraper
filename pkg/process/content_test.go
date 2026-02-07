package process

import (
	"net/url"
	"path/filepath"
	"testing"

	"github.com/piratf/doc-scraper/pkg/config"
)

// testContentProcessor returns a minimal ContentProcessor for testing
func testContentProcessor() *ContentProcessor {
	return &ContentProcessor{}
}

// --- getOutputPathForURL Tests ---

func TestGetOutputPathForURL_RootURL(t *testing.T) {
	cp := testContentProcessor()
	siteCfg := config.SiteConfig{
		AllowedDomain:     "example.com",
		AllowedPathPrefix: "/",
	}
	siteOutputDir := "/output/site"

	tests := []struct {
		name         string
		inputURL     string
		expectedPath string
		expectedOK   bool
	}{
		{
			name:         "RootWithTrailingSlash",
			inputURL:     "https://example.com/",
			expectedPath: filepath.Join(siteOutputDir, "index.md"),
			expectedOK:   true,
		},
		{
			name:         "RootWithoutTrailingSlash",
			inputURL:     "https://example.com",
			expectedPath: filepath.Join(siteOutputDir, "index.md"),
			expectedOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.inputURL)
			path, ok := cp.getOutputPathForURL(parsed, siteCfg, siteOutputDir)
			if ok != tt.expectedOK {
				t.Errorf("getOutputPathForURL(%q) ok = %v, want %v", tt.inputURL, ok, tt.expectedOK)
			}
			if path != tt.expectedPath {
				t.Errorf("getOutputPathForURL(%q) path = %q, want %q", tt.inputURL, path, tt.expectedPath)
			}
		})
	}
}

func TestGetOutputPathForURL_FileLikeURLs(t *testing.T) {
	cp := testContentProcessor()
	siteCfg := config.SiteConfig{
		AllowedDomain:     "example.com",
		AllowedPathPrefix: "/docs/",
	}
	siteOutputDir := "/output/site"

	tests := []struct {
		name         string
		inputURL     string
		expectedPath string
		expectedOK   bool
	}{
		{
			name:         "HTMLFile",
			inputURL:     "https://example.com/docs/guide.html",
			expectedPath: filepath.Join(siteOutputDir, "guide.md"),
			expectedOK:   true,
		},
		{
			name:         "NestedHTMLFile",
			inputURL:     "https://example.com/docs/api/reference.html",
			expectedPath: filepath.Join(siteOutputDir, "api", "reference.md"),
			expectedOK:   true,
		},
		{
			name:         "DeepNesting",
			inputURL:     "https://example.com/docs/a/b/c/d.html",
			expectedPath: filepath.Join(siteOutputDir, "a", "b", "c", "d.md"),
			expectedOK:   true,
		},
		{
			name:         "PHPFile",
			inputURL:     "https://example.com/docs/page.php",
			expectedPath: filepath.Join(siteOutputDir, "page.md"),
			expectedOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.inputURL)
			path, ok := cp.getOutputPathForURL(parsed, siteCfg, siteOutputDir)
			if ok != tt.expectedOK {
				t.Errorf("getOutputPathForURL(%q) ok = %v, want %v", tt.inputURL, ok, tt.expectedOK)
			}
			if path != tt.expectedPath {
				t.Errorf("getOutputPathForURL(%q) path = %q, want %q", tt.inputURL, path, tt.expectedPath)
			}
		})
	}
}

func TestGetOutputPathForURL_DirectoryLikeURLs(t *testing.T) {
	cp := testContentProcessor()
	siteCfg := config.SiteConfig{
		AllowedDomain:     "example.com",
		AllowedPathPrefix: "/docs/",
	}
	siteOutputDir := "/output/site"

	tests := []struct {
		name         string
		inputURL     string
		expectedPath string
		expectedOK   bool
	}{
		{
			name:         "DirectoryWithSlash",
			inputURL:     "https://example.com/docs/api/",
			expectedPath: filepath.Join(siteOutputDir, "api", "index.md"),
			expectedOK:   true,
		},
		{
			name:         "DirectoryWithoutSlash",
			inputURL:     "https://example.com/docs/api",
			expectedPath: filepath.Join(siteOutputDir, "api", "index.md"),
			expectedOK:   true,
		},
		{
			name:         "NestedDirectory",
			inputURL:     "https://example.com/docs/guides/tutorials/",
			expectedPath: filepath.Join(siteOutputDir, "guides", "tutorials", "index.md"),
			expectedOK:   true,
		},
		{
			name:         "PrefixRootWithSlash",
			inputURL:     "https://example.com/docs/",
			expectedPath: filepath.Join(siteOutputDir, "docs", "index.md"),
			expectedOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.inputURL)
			path, ok := cp.getOutputPathForURL(parsed, siteCfg, siteOutputDir)
			if ok != tt.expectedOK {
				t.Errorf("getOutputPathForURL(%q) ok = %v, want %v", tt.inputURL, ok, tt.expectedOK)
			}
			if path != tt.expectedPath {
				t.Errorf("getOutputPathForURL(%q) path = %q, want %q", tt.inputURL, path, tt.expectedPath)
			}
		})
	}
}

func TestGetOutputPathForURL_ScopeViolations(t *testing.T) {
	cp := testContentProcessor()
	siteCfg := config.SiteConfig{
		AllowedDomain:     "example.com",
		AllowedPathPrefix: "/docs/",
	}
	siteOutputDir := "/output/site"

	tests := []struct {
		name       string
		inputURL   string
		expectedOK bool
	}{
		{
			name:       "WrongDomain",
			inputURL:   "https://other.com/docs/page.html",
			expectedOK: false,
		},
		{
			name:       "WrongPathPrefix",
			inputURL:   "https://example.com/blog/post.html",
			expectedOK: false,
		},
		{
			name:       "FTPScheme",
			inputURL:   "ftp://example.com/docs/file.txt",
			expectedOK: false,
		},
		{
			name:       "FileScheme",
			inputURL:   "file:///docs/page.html",
			expectedOK: false,
		},
		{
			name:       "MailtoScheme",
			inputURL:   "mailto:user@example.com",
			expectedOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.inputURL)
			path, ok := cp.getOutputPathForURL(parsed, siteCfg, siteOutputDir)
			if ok != tt.expectedOK {
				t.Errorf("getOutputPathForURL(%q) ok = %v, want %v", tt.inputURL, ok, tt.expectedOK)
			}
			if ok && path != "" {
				t.Errorf("getOutputPathForURL(%q) returned path %q for out-of-scope URL", tt.inputURL, path)
			}
		})
	}
}

func TestGetOutputPathForURL_HTTPScheme(t *testing.T) {
	cp := testContentProcessor()
	siteCfg := config.SiteConfig{
		AllowedDomain:     "example.com",
		AllowedPathPrefix: "/",
	}
	siteOutputDir := "/output/site"

	tests := []struct {
		name         string
		inputURL     string
		expectedPath string
		expectedOK   bool
	}{
		{
			name:         "HTTP",
			inputURL:     "http://example.com/page.html",
			expectedPath: filepath.Join(siteOutputDir, "page.md"),
			expectedOK:   true,
		},
		{
			name:         "HTTPS",
			inputURL:     "https://example.com/page.html",
			expectedPath: filepath.Join(siteOutputDir, "page.md"),
			expectedOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.inputURL)
			path, ok := cp.getOutputPathForURL(parsed, siteCfg, siteOutputDir)
			if ok != tt.expectedOK {
				t.Errorf("getOutputPathForURL(%q) ok = %v, want %v", tt.inputURL, ok, tt.expectedOK)
			}
			if path != tt.expectedPath {
				t.Errorf("getOutputPathForURL(%q) path = %q, want %q", tt.inputURL, path, tt.expectedPath)
			}
		})
	}
}

func TestGetOutputPathForURL_PathPrefixVariations(t *testing.T) {
	cp := testContentProcessor()

	tests := []struct {
		name              string
		allowedPathPrefix string
		inputURL          string
		expectedPath      string
		expectedOK        bool
	}{
		{
			name:              "PrefixWithoutTrailingSlash",
			allowedPathPrefix: "/docs",
			inputURL:          "https://example.com/docs/page.html",
			expectedPath:      filepath.Join("/output", "page.md"),
			expectedOK:        true,
		},
		{
			name:              "DeepPrefix",
			allowedPathPrefix: "/api/v2/",
			inputURL:          "https://example.com/api/v2/users.html",
			expectedPath:      filepath.Join("/output", "users.md"),
			expectedOK:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			siteCfg := config.SiteConfig{
				AllowedDomain:     "example.com",
				AllowedPathPrefix: tt.allowedPathPrefix,
			}
			parsed, _ := url.Parse(tt.inputURL)
			path, ok := cp.getOutputPathForURL(parsed, siteCfg, "/output")
			if ok != tt.expectedOK {
				t.Errorf("getOutputPathForURL(%q) ok = %v, want %v", tt.inputURL, ok, tt.expectedOK)
			}
			if path != tt.expectedPath {
				t.Errorf("getOutputPathForURL(%q) path = %q, want %q", tt.inputURL, path, tt.expectedPath)
			}
		})
	}
}
