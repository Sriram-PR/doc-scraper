package process

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

// testContentProcessor returns a minimal ContentProcessor for testing
func testContentProcessor() *ContentProcessor {
	return &ContentProcessor{}
}

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
			path, ok := cp.getOutputPathForURL(parsed, &siteCfg, siteOutputDir)
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
			path, ok := cp.getOutputPathForURL(parsed, &siteCfg, siteOutputDir)
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
			path, ok := cp.getOutputPathForURL(parsed, &siteCfg, siteOutputDir)
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
			path, ok := cp.getOutputPathForURL(parsed, &siteCfg, siteOutputDir)
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
			path, ok := cp.getOutputPathForURL(parsed, &siteCfg, siteOutputDir)
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
			path, ok := cp.getOutputPathForURL(parsed, &siteCfg, "/output")
			if ok != tt.expectedOK {
				t.Errorf("getOutputPathForURL(%q) ok = %v, want %v", tt.inputURL, ok, tt.expectedOK)
			}
			if path != tt.expectedPath {
				t.Errorf("getOutputPathForURL(%q) path = %q, want %q", tt.inputURL, path, tt.expectedPath)
			}
		})
	}
}

// TestGetOutputPathForURL_PathTraversal verifies that URLs whose path segments
// would resolve outside the site output directory (via ".." traversal or
// dot-only segments) are rejected. Regression test for path traversal that
// could otherwise write files to arbitrary filesystem locations.
func TestGetOutputPathForURL_PathTraversal(t *testing.T) {
	cp := testContentProcessor()
	siteCfg := config.SiteConfig{
		AllowedDomain:     "example.com",
		AllowedPathPrefix: "/docs/",
	}
	siteOutputDir := "/output/site"

	tests := []struct {
		name     string
		inputURL string
	}{
		{"ParentTraversal", "https://example.com/docs/../../../etc/evil.md"},
		{"MixedTraversal", "https://example.com/docs/legit/../../../../etc/passwd"},
		{"DoubleDotSegment", "https://example.com/docs/..//page.html"},
		{"DoubleDotInDirPath", "https://example.com/docs/foo/../../../bar.html"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.inputURL)
			gotPath, ok := cp.getOutputPathForURL(parsed, &siteCfg, siteOutputDir)
			// Two acceptable outcomes: (a) function rejects URL outright (ok=false),
			// or (b) function returns a path that stays within siteOutputDir.
			if ok {
				cleanedDir := filepath.Clean(siteOutputDir)
				cleanedFull := filepath.Clean(gotPath)
				if cleanedFull != cleanedDir && !strings.HasPrefix(cleanedFull, cleanedDir+string(filepath.Separator)) {
					t.Errorf("getOutputPathForURL(%q) returned escape path %q (siteOutputDir=%q)", tt.inputURL, gotPath, siteOutputDir)
				}
			}
		})
	}
}

// cleanedHeading runs cleanupHTML over a fragment and returns the heading HTML.
func cleanedHeading(t *testing.T, fragment string) string {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<body>" + fragment + "</body>"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cp := testContentProcessor()
	body := doc.Find("body")
	cp.cleanupHTML(body)
	html, err := body.Html()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return strings.TrimSpace(html)
}

// Heading self-anchors must lose the link but keep the title text.
func TestCleanupHTML_UnwrapsHeadingSelfAnchors(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
		wantText string
	}{
		{
			name:     "mdBook",
			fragment: `<h1 id="command-line-apps"><a class="header" href="#command-line-apps">Command line apps in Rust</a></h1>`,
			wantText: "Command line apps in Rust",
		},
		{
			name:     "SphinxTocBackref",
			fragment: `<h2><a class="toc-backref" href="#id2">Exit Codes</a><a class="headerlink" href="#exit-codes">¶</a></h2>`,
			wantText: "Exit Codes",
		},
		{
			name:     "DockerStyleWrappedAnchor",
			fragment: `<h2 id="install"><a class="anchor-link" href="#install">Install Docker</a></h2>`,
			wantText: "Install Docker",
		},
		{
			name:     "HrefIdDiffersFromHeadingId",
			fragment: `<h3 id="real-id"><a href="#id7">Dynamic Defaults</a></h3>`,
			wantText: "Dynamic Defaults",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanedHeading(t, tt.fragment)
			if strings.Contains(got, "<a") {
				t.Errorf("anchor survived in %q", got)
			}
			if !strings.Contains(got, tt.wantText) {
				t.Errorf("heading text %q lost; got %q", tt.wantText, got)
			}
		})
	}
}

// A heading whose link points off-page is real content and must survive.
func TestCleanupHTML_KeepsNonSelfHeadingLinks(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
	}{
		{
			name:     "ChangelogCompareLink",
			fragment: `<h2><a href="https://github.com/o/r/compare/v9.4.0...v9.5.0">9.5.0</a> (2022-05-15)</h2>`,
		},
		{
			name:     "RelativePageLink",
			fragment: `<h2><a href="/guide/other.html">Other Guide</a></h2>`,
		},
		{
			name:     "AbsolutePathLink",
			fragment: `<h3><a href="https://example.com/spec">The Spec</a></h3>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanedHeading(t, tt.fragment)
			if !strings.Contains(got, "<a") {
				t.Errorf("legitimate heading link was stripped: %q", got)
			}
		})
	}
}

// A same-page anchor that is only part of the heading is a cross-reference, not
// a self-anchor, so it must survive.
func TestCleanupHTML_KeepsPartialHeadingAnchor(t *testing.T) {
	got := cleanedHeading(t, `<h2>See also <a href="#other">the other section</a></h2>`)
	if !strings.Contains(got, "<a") {
		t.Errorf("partial in-page heading anchor was stripped: %q", got)
	}
	if !strings.Contains(got, "See also") {
		t.Errorf("heading text lost: %q", got)
	}
}

// Docusaurus hash-links render as a zero-width space, so they are invisible but
// not whitespace.
func TestCleanupHTML_RemovesZeroWidthAnchors(t *testing.T) {
	got := cleanedHeading(t, "<h2 id=\"install\">Install<a class=\"hash-link\" href=\"#install\">\u200b</a></h2>")
	if strings.Contains(got, "<a") {
		t.Errorf("zero-width hash-link survived: %q", got)
	}
	if !strings.Contains(got, "Install") {
		t.Errorf("heading text lost: %q", got)
	}
}

func TestCleanupHTML_RemovesPermalinkAnchors(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
	}{
		{"SphinxHeaderlink", `<h2>Title<a class="headerlink" href="#title">¶</a></h2>`},
		{"MkDocsPermalink", `<h2>Title<a class="headerlink" title="Permanent link" href="#title">¶</a></h2>`},
		{"HashText", `<h2>Title<a href="#title">#</a></h2>`},
		{"GitBookIconOnly", `<h2>Title<a aria-label="Direct link" href="#title"></a></h2>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanedHeading(t, tt.fragment)
			if strings.Contains(got, "<a") {
				t.Errorf("permalink anchor survived: %q", got)
			}
			if !strings.Contains(got, "Title") {
				t.Errorf("heading text lost: %q", got)
			}
		})
	}
}

// Links in body copy are untouched by the heading rule.
func TestCleanupHTML_LeavesBodyLinksAlone(t *testing.T) {
	got := cleanedHeading(t, `<h2 id="x"><a href="#x">Heading</a></h2><p>See <a href="#x">this section</a> and <a href="/other">other</a>.</p>`)
	if strings.Count(got, "<a") != 2 {
		t.Errorf("expected the two body links to survive, got %q", got)
	}
}
