package process

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
			path, ok := cp.OutputPathForURL(parsed, &siteCfg, siteOutputDir)
			assert.Equal(t, tt.expectedOK, ok, "OutputPathForURL(%q) ok", tt.inputURL)
			assert.Equal(t, tt.expectedPath, path, "OutputPathForURL(%q) path", tt.inputURL)
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
			path, ok := cp.OutputPathForURL(parsed, &siteCfg, siteOutputDir)
			assert.Equal(t, tt.expectedOK, ok, "OutputPathForURL(%q) ok", tt.inputURL)
			assert.Equal(t, tt.expectedPath, path, "OutputPathForURL(%q) path", tt.inputURL)
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
			expectedPath: filepath.Join(siteOutputDir, "index.md"),
			expectedOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, _ := url.Parse(tt.inputURL)
			path, ok := cp.OutputPathForURL(parsed, &siteCfg, siteOutputDir)
			assert.Equal(t, tt.expectedOK, ok, "OutputPathForURL(%q) ok", tt.inputURL)
			assert.Equal(t, tt.expectedPath, path, "OutputPathForURL(%q) path", tt.inputURL)
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
			path, ok := cp.OutputPathForURL(parsed, &siteCfg, siteOutputDir)
			assert.Equal(t, tt.expectedOK, ok, "OutputPathForURL(%q) ok", tt.inputURL)
			if ok {
				assert.Empty(t, path, "OutputPathForURL(%q) returned a path for out-of-scope URL", tt.inputURL)
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
			path, ok := cp.OutputPathForURL(parsed, &siteCfg, siteOutputDir)
			assert.Equal(t, tt.expectedOK, ok, "OutputPathForURL(%q) ok", tt.inputURL)
			assert.Equal(t, tt.expectedPath, path, "OutputPathForURL(%q) path", tt.inputURL)
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
			path, ok := cp.OutputPathForURL(parsed, &siteCfg, "/output")
			assert.Equal(t, tt.expectedOK, ok, "OutputPathForURL(%q) ok", tt.inputURL)
			assert.Equal(t, tt.expectedPath, path, "OutputPathForURL(%q) path", tt.inputURL)
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
			gotPath, ok := cp.OutputPathForURL(parsed, &siteCfg, siteOutputDir)
			// Two acceptable outcomes: (a) function rejects URL outright (ok=false),
			// or (b) function returns a path that stays within siteOutputDir.
			if ok {
				cleanedDir := filepath.Clean(siteOutputDir)
				cleanedFull := filepath.Clean(gotPath)
				assert.True(t, cleanedFull == cleanedDir || strings.HasPrefix(cleanedFull, cleanedDir+string(filepath.Separator)),
					"OutputPathForURL(%q) returned escape path %q (siteOutputDir=%q)", tt.inputURL, gotPath, siteOutputDir)
			}
		})
	}
}

// cleanedHeading runs cleanupHTML over a fragment and returns the heading HTML.
func cleanedHeading(t *testing.T, fragment string) string {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<body>" + fragment + "</body>"))
	require.NoError(t, err, "parse")
	cp := testContentProcessor()
	body := doc.Find("body")
	cp.cleanupHTML(body)
	html, err := body.Html()
	require.NoError(t, err, "render")
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
			assert.NotContains(t, got, "<a", "anchor survived")
			assert.Contains(t, got, tt.wantText, "heading text lost")
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
			assert.Contains(t, got, "<a", "legitimate heading link was stripped")
		})
	}
}

// A same-page anchor that is only part of the heading is a cross-reference, not
// a self-anchor, so it must survive.
func TestCleanupHTML_KeepsPartialHeadingAnchor(t *testing.T) {
	got := cleanedHeading(t, `<h2>See also <a href="#other">the other section</a></h2>`)
	assert.Contains(t, got, "<a", "partial in-page heading anchor was stripped")
	assert.Contains(t, got, "See also", "heading text lost")
}

// Docusaurus hash-links render as a zero-width space, so they are invisible but
// not whitespace.
func TestCleanupHTML_RemovesZeroWidthAnchors(t *testing.T) {
	got := cleanedHeading(t, "<h2 id=\"install\">Install<a class=\"hash-link\" href=\"#install\">\u200b</a></h2>")
	assert.NotContains(t, got, "<a", "zero-width hash-link survived")
	assert.Contains(t, got, "Install", "heading text lost")
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
			assert.NotContains(t, got, "<a", "permalink anchor survived")
			assert.Contains(t, got, "Title", "heading text lost")
		})
	}
}

// Links in body copy are untouched by the heading rule.
func TestCleanupHTML_LeavesBodyLinksAlone(t *testing.T) {
	got := cleanedHeading(t, `<h2 id="x"><a href="#x">Heading</a></h2><p>See <a href="#x">this section</a> and <a href="/other">other</a>.</p>`)
	assert.Equal(t, 2, strings.Count(got, "<a"), "expected the two body links to survive, got %q", got)
}

func selectMainContentFixture(t *testing.T, html, selector string) (*goquery.Selection, string, error) {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	require.NoError(t, err, "parse fixture")
	u, _ := url.Parse("https://docs.example.com/page/")
	cp := NewContentProcessor(nil, &config.AppConfig{}, silentLog())
	return cp.SelectMainContent(doc, u, &config.SiteConfig{ContentSelector: selector}, silentLog())
}

func TestHasExtractableContent(t *testing.T) {
	tests := []struct {
		name string
		html string
		want bool
	}{
		{"Prose", `<article>Real documentation body.</article>`, true},
		{"Empty", `<article></article>`, false},
		{"WhitespaceOnly", `<article>   </article>`, false},
		{"NewlinesAndTabs", "<article>\n\t\n  </article>", false},
		{"ZeroWidthOnly", `<article>&#8203;&#65279;</article>`, false},
		{"NestedEmptyWrappers", `<article><div><span></span></div></article>`, false},
		{"ImageOnly", `<article><img src="/diagram.png"></article>`, true},
		{"TableOnly", `<article><table><tr><td></td></tr></table></article>`, true},
		{"CodeBlockOnly", `<article><pre></pre></article>`, true},
		{"SvgOnly", `<article><svg viewBox="0 0 1 1"></svg></article>`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(tt.html))
			require.NoError(t, err, "parse")
			assert.Equal(t, tt.want, hasExtractableContent(doc.Find("article")))
		})
	}
}

// The real MkDocs Material landing page: the content container is present and
// correctly detected, but holds a single space. Matching on element count alone
// saved this as a blank page.
func TestSelectMainContent_ExplicitSelectorMatchedButEmpty(t *testing.T) {
	html := `<html><head><title>Material for MkDocs</title></head><body>
		<nav class="md-nav">Getting started Setup Reference</nav>
		<article class="md-content__inner md-typeset"> </article>
	</body></html>`

	_, _, err := selectMainContentFixture(t, html, "article.md-content__inner")
	require.Error(t, err, "expected an error for a matched-but-empty selector (page would be saved blank)")
	require.ErrorContains(t, err, "yielded no content", "error should name the empty-content condition")
}

func TestSelectMainContent_ExplicitSelectorNotFoundStillErrors(t *testing.T) {
	html := `<html><head><title>T</title></head><body><div class="other">x</div></body></html>`

	_, _, err := selectMainContentFixture(t, html, "article.md-content__inner")
	require.Error(t, err, "expected an error when the selector matches nothing")
}

func TestSelectMainContent_ExplicitSelectorWithContentSucceeds(t *testing.T) {
	html := `<html><head><title>T</title></head><body>
		<article class="md-content__inner">Configure retries with max_retries.</article>
	</body></html>`

	content, title, err := selectMainContentFixture(t, html, "article.md-content__inner")
	require.NoError(t, err)
	assert.Equal(t, "T", title)
	assert.Contains(t, content.Text(), "max_retries", "content missing real body")
}

// An image-only content region is legitimate and must survive the empty check.
func TestSelectMainContent_ExplicitSelectorImageOnlySurvives(t *testing.T) {
	html := `<html><head><title>Architecture</title></head><body>
		<article class="md-content__inner"><img src="/architecture.svg" alt=""></article>
	</body></html>`

	content, _, err := selectMainContentFixture(t, html, "article.md-content__inner")
	require.NoError(t, err, "image-only content region must not be treated as empty")
	assert.Equal(t, 1, content.Find("img").Length(), "expected the image to be preserved")
}

// Auto mode must route a matched-but-empty container to readability rather than
// saving the blank container.
func TestSelectMainContent_AutoMatchedButEmptyFallsBackToReadability(t *testing.T) {
	html := `<html><head><title>Material for MkDocs</title></head>
		<body data-md-component="skip">
		<article class="md-content__inner md-typeset"> </article>
		<div><p>` + strings.Repeat("Readability needs a decent block of prose to score a candidate. ", 12) + `</p></div>
	</body></html>`

	content, _, err := selectMainContentFixture(t, html, "auto")
	require.NoError(t, err, "expected readability fallback to recover content")
	assert.Contains(t, content.Text(), "Readability needs a decent block", "fallback did not return the prose block")
}

// The detector caches its selector per domain after validating it on the first
// page, so a later page whose container is empty (MkDocs Material's landing
// page) reaches extraction unvalidated and must fall back to readability.
func TestSelectMainContent_AutoCachedSelectorEmptyOnLaterPageFallsBack(t *testing.T) {
	cp := NewContentProcessor(nil, &config.AppConfig{}, silentLog())
	siteCfg := &config.SiteConfig{ContentSelector: "auto"}
	head := `<head><meta name="generator" content="mkdocs-1.6.0, mkdocs-material-9.5.0"><title>Docs</title></head>`
	extract := func(pageURL, html string) string {
		t.Helper()
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
		require.NoError(t, err)
		u, err := url.Parse(pageURL)
		require.NoError(t, err)
		content, _, err := cp.SelectMainContent(doc, u, siteCfg, silentLog())
		require.NoError(t, err)
		return content.Text()
	}

	guide := extract("https://docs.example.com/guide/", `<html>`+head+`<body data-md-color-scheme="default">
		<div class="md-content"><article class="md-content__inner md-typeset"><p>`+
		strings.Repeat("Configure retries with max_retries and a backoff delay. ", 6)+`</p></article></div></body></html>`)
	require.Contains(t, guide, "max_retries", "first page should extract through the detected selector")

	landing := extract("https://docs.example.com/", `<html>`+head+`<body data-md-color-scheme="default">
		<div class="md-content"><article class="md-content__inner md-typeset"> </article></div>
		<div><p>`+strings.Repeat("Readability needs a decent block of prose to score a candidate. ", 12)+`</p></div>
		</body></html>`)
	assert.Contains(t, landing, "Readability needs a decent block", "empty cached container should fall back to readability")
}

// processPage runs ProcessAndSaveContent over html served at pageURL, with
// images fetched through fetcher, and returns the saved markdown.
func processPage(t *testing.T, fetcher *stubFetcher, pageURL, html string) (string, string) {
	t.Helper()
	outDir := t.TempDir()
	ip := newImageProcessorWithWorkers(t, newFakeImageStore(), fetcher, 2)
	cp := NewContentProcessor(ip, &config.AppConfig{}, silentLog())
	siteCfg := &config.SiteConfig{AllowedDomain: "e.com", AllowedPathPrefix: "/docs/"}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	require.NoError(t, err)
	u, err := url.Parse(pageURL)
	require.NoError(t, err)

	saved, md, _, err := cp.ProcessAndSaveContent(doc.Find("body"), "Page", u, siteCfg, outDir, 0, silentLog(), context.Background())
	require.NoError(t, err)
	onDisk, err := os.ReadFile(saved)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), string(md), "saved file should contain the returned markdown")
	return string(md), outDir
}

func TestProcessAndSaveContent_RewritesImages(t *testing.T) {
	fetcher := &stubFetcher{responses: map[string]*http.Response{
		"https://e.com/img/ok.png":  imageResponse("PNG", "image/png", ""),
		"https://e.com/img/fig.png": imageResponse("PNG", "image/png", ""),
	}}
	md, outDir := processPage(t, fetcher, "https://e.com/docs/guide/page.html", `<body>
		<p>Intro</p>
		<img src="/img/ok.png" alt="Diagram">
		<figure><img src="/img/fig.png" alt="ignored"><figcaption> Real caption </figcaption></figure>
		<img src="/img/broken.png" alt="Broken">
		<img src="data:image/png;base64,AAAA" alt="Inline">
		<img src="ftp://e.com/x.png" alt="Ftp">
		<img src="http://[::1" alt="Unparseable">
	</body>`)

	okLocal := regexp.MustCompile(`!\[Diagram\]\(\.\./images/ok_[0-9a-f]{8}\.png\)`).FindString(md)
	require.NotEmpty(t, okLocal, "downloaded image should point at its local copy relative to the page, got:\n%s", md)
	rel := strings.TrimSuffix(strings.TrimPrefix(okLocal, "![Diagram]("), ")")
	assert.FileExists(t, filepath.Join(outDir, "guide", filepath.FromSlash(rel)), "relative image path should resolve from the page's directory")

	assert.Regexp(t, `!\[Real caption\]\(\.\./images/fig_[0-9a-f]{8}\.png\)`, md, "figcaption should become the alt text")
	assert.NotContains(t, md, "broken.png", "failed downloads are removed")
	assert.NotContains(t, md, "Unparseable", "images with fatal errors are removed")
	assert.Contains(t, md, "data:image/png;base64,AAAA", "skipped images are left in place")
	assert.Contains(t, md, "ftp://e.com/x.png", "skipped images are left in place")
}

func TestProcessAndSaveContent_RewritesInternalLinks(t *testing.T) {
	md, _ := processPage(t, &stubFetcher{}, "https://e.com/docs/guide/page.html", `<body>
		<a href="/docs/other.html#part">Other</a>
		<a href="#local">Local</a>
		<a href="//cdn.example.org/lib.js">CDN</a>
		<a href="//e.com/docs/proto.html">ProtoInScope</a>
		<a href="/blog/post.html">RootRel</a>
		<a href="../../blog/post.html?a=1#x">DotRel</a>
		<a href="https://elsewhere.org/x">External</a>
		<a href="mailto:a@e.com">Mail</a>
		<a href="http://[::1">Broken</a>
	</body>`)

	assert.Contains(t, md, "[Other](../other.md#part)", "in-scope links become relative .md paths and keep the fragment")
	assert.Contains(t, md, "[Local](#local)")
	assert.Contains(t, md, "[CDN](https://cdn.example.org/lib.js)", "protocol-relative links take the page's scheme")
	assert.Contains(t, md, "[ProtoInScope](../proto.md)")
	assert.Contains(t, md, "[RootRel](https://e.com/blog/post.html)", "out-of-scope relative links become absolute so they resolve offline")
	assert.Contains(t, md, "[DotRel](https://e.com/blog/post.html?a=1#x)")
	assert.Contains(t, md, "[External](https://elsewhere.org/x)")
	assert.Contains(t, md, "[Mail](mailto:a@e.com)")
	assert.Contains(t, md, "[Broken](http://%5B::1)", "unparseable links are kept, not dropped (the converter percent-encodes the bracket)")
}

func TestStripBreadcrumbs(t *testing.T) {
	long := strings.Repeat("Section ", 30)
	tests := []struct {
		name    string
		html    string
		removed int
		gone    []string
		kept    []string
	}{
		{"spring container wrapping a nav",
			`<div class="breadcrumbs-container"><nav class="breadcrumbs" aria-label="breadcrumbs"><ul><li><a href="/">Spring Framework</a></li></ul></nav></div><h1>Overview</h1><p>Body.</p>`,
			1, []string{"Spring Framework"}, []string{"Overview", "Body."}},
		{"docker nav id",
			`<nav id="breadcrumbs"><a href="/">Home</a> / Get started</nav><h1>Get started</h1>`,
			1, []string{"Home"}, []string{"Get started"}},
		{"go.dev ordered list",
			`<ol class="SiteBreadcrumb"><li><a href="/doc/">Documentation</a></li><li><a href="/doc/effective_go">Effective Go</a></li></ol><h1>The title</h1>`,
			1, []string{"Documentation"}, []string{"The title"}},
		{"primer aria label before the h1",
			`<nav aria-label="Breadcrumbs"><a href="/">Product UI</a><a href="/c">Components</a></nav><h1>Breadcrumbs</h1><p>Use breadcrumbs to show hierarchy.</p>`,
			1, []string{"Product UI"}, []string{"Use breadcrumbs to show hierarchy."}},
		{"schema.org list",
			`<ul itemscope itemtype="https://schema.org/BreadcrumbList"><li><a href="/">Docs</a></li></ul><h2>Install</h2>`,
			1, []string{"Docs"}, []string{"Install"}},
		{"nav inside an oversized marked wrapper",
			`<div class="breadcrumb-area"><nav class="breadcrumb"><a href="/">Root</a></nav><p>` + long + `</p></div><h1>Title</h1>`,
			1, []string{"Root"}, []string{"Section Section", "Title"}},
		{"shadcn api headings",
			`<h2>API Reference</h2><h3 id="breadcrumb-list">BreadcrumbList</h3><p>Props.</p>`,
			0, nil, []string{"BreadcrumbList", "Props."}},
		{"bootstrap demo after a heading",
			`<h1>Breadcrumb</h1><p>Indicate the current page.</p><nav aria-label="breadcrumb"><ol class="breadcrumb">` +
				`<li class="breadcrumb-item"><a href="#">Home</a></li><li class="breadcrumb-item active">Library</li></ol></nav>`,
			0, nil, []string{"Home", "Library"}},
		{"heading anchor and span are not containers",
			`<a aria-label="Link to this section: Default breadcrumb" href="#default">#</a><span id="breadcrumb-demo">Demo span</span><h2 id="default">Default breadcrumb</h2>`,
			0, nil, []string{"Demo span", "Default breadcrumb"}},
		{"long marked block before any heading",
			`<div class="breadcrumb-intro"><p>` + long + `</p></div><h1>Title</h1>`,
			0, nil, []string{"Section Section"}},
		{"headings in a sidebar or template don't end the scan",
			`<aside><h2>Product UI navigation</h2></aside><template><h3>What can I help you with?</h3></template>` +
				`<nav aria-label="Breadcrumbs"><a href="/c">Components</a></nav><h1>Breadcrumbs</h1>`,
			1, []string{"Components"}, []string{"Product UI navigation", "Breadcrumbs"}},
		{"an h1 inside the article header still ends the scan",
			`<article><header><h1>Breadcrumb</h1></header><nav aria-label="breadcrumb"><a href="#">Home</a></nav></article>`,
			0, nil, []string{"Home"}},
		{"marked wrapper holding the page heading",
			`<div class="page-header has-breadcrumbs"><nav class="breadcrumbs"><a href="/">Home</a></nav><h1>Title</h1><p>Short intro.</p></div>`,
			1, []string{"Home"}, []string{"Title", "Short intro."}},
		{"trail with a hidden heading of its own",
			`<nav class="breadcrumbs"><h2 class="sr-only">You are here</h2><a href="/">Home</a></nav><h1>Title</h1>`,
			1, []string{"Home", "You are here"}, []string{"Title"}},
		{"no headings at all",
			`<nav class="breadcrumbs"><a href="/">Home</a></nav><p>A page without headings.</p>`,
			1, []string{"Home"}, []string{"A page without headings."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := goquery.NewDocumentFromReader(strings.NewReader("<html><body><main>" + tt.html + "</main></body></html>"))
			require.NoError(t, err)
			scope := doc.Find("main")
			assert.Equal(t, tt.removed, stripBreadcrumbs(scope))
			text := scope.Text()
			for _, g := range tt.gone {
				assert.NotContains(t, text, g)
			}
			for _, k := range tt.kept {
				assert.Contains(t, text, k)
			}
		})
	}
}

const springPage = `<html><head><title>Overview</title><meta name="generator" content="Antora 3.1.9"></head><body>
	<article class="doc"><div class="breadcrumbs-container"><nav class="breadcrumbs" aria-label="breadcrumbs"><ul><li><a href="/">Spring Framework</a></li><li>Overview</li></ul></nav></div>
	<h1>Overview</h1><p>` + "The Spring Framework provides a comprehensive programming and configuration model for modern Java applications. " + `</p>
	<p>` + "It focuses on the plumbing of enterprise applications so that teams can focus on application-level business logic. " + `</p></article></body></html>`

func TestSelectMainContent_ExplicitSelectorStripsBreadcrumbs(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(springPage))
	require.NoError(t, err)
	u, _ := url.Parse("https://docs.spring.io/spring-framework/reference/")
	cp := NewContentProcessor(nil, &config.AppConfig{}, silentLog())

	content, _, err := cp.SelectMainContent(doc, u, &config.SiteConfig{ContentSelector: "article.doc"}, silentLog())
	require.NoError(t, err)
	assert.Zero(t, content.Find(".breadcrumbs-container").Length())
	assert.Contains(t, content.Text(), "comprehensive programming")
	assert.Equal(t, 1, doc.Find(".breadcrumbs-container").Length(), "the input page is not modified")
}

func TestSelectMainContent_AutoDetectedSelectorStripsBreadcrumbs(t *testing.T) {
	content, _, err := selectMainContentFixture(t, springPage, "auto")
	require.NoError(t, err)
	assert.Zero(t, content.Find("nav.breadcrumbs").Length())
	assert.Contains(t, content.Text(), "comprehensive programming")
}

func TestSelectMainContent_BreadcrumbOnlyRegionIsEmpty(t *testing.T) {
	html := `<html><head><title>T</title></head><body><article class="doc"><nav aria-label="breadcrumbs"><a href="/">Home</a></nav></article></body></html>`
	_, _, err := selectMainContentFixture(t, html, "article.doc")
	require.ErrorContains(t, err, "yielded no content")
}

var goDevPage = `<html><head><title>Effective Go</title></head><body><header><nav><a href="/">Go</a> <a href="/doc/">Docs</a></nav></header>` +
	`<main id="main-content"><article class="Doc Article"><ol class="SiteBreadcrumb"><li><a href="/doc/">Documentation</a></li><li><a href="/doc/effective_go">Effective Go</a></li></ol>` +
	`<h1>Effective Go</h1><p>` + strings.Repeat("Go is a new language that borrows ideas from existing languages but has unusual properties. ", 12) + `</p>` +
	`<h2 id="introduction">Introduction</h2><p>` + strings.Repeat("To write Go well, it is important to understand its properties and idioms. ", 10) + `</p></article></main></body></html>`

func TestSelectMainContent_ReadabilityPathStripsBreadcrumbs(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(goDevPage))
	require.NoError(t, err)
	u, _ := url.Parse("https://go.dev/doc/effective_go")
	cp := NewContentProcessor(nil, &config.AppConfig{}, silentLog())

	content, _, err := cp.SelectMainContent(doc, u, &config.SiteConfig{ContentSelector: "auto"}, silentLog())
	require.NoError(t, err)
	assert.NotContains(t, content.Text(), "Documentation")
	assert.Contains(t, content.Text(), "unusual properties")
	assert.Equal(t, 1, doc.Find("ol.SiteBreadcrumb").Length(), "the input page is not modified")
}

func TestStripBreadcrumbs_ChromeScopeKeepsItsHeadings(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(
		`<html><body><aside class="content"><h1>Breadcrumb</h1><nav aria-label="breadcrumb"><a href="#">Home</a></nav></aside></body></html>`))
	require.NoError(t, err)
	scope := doc.Find("aside").Clone()
	assert.Zero(t, stripBreadcrumbs(scope))
	assert.Contains(t, scope.Text(), "Home")
}
