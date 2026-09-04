package detect

import (
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var filler = strings.Repeat("Documentation prose that fills the content area with enough text to validate. ", 5)

func parseDoc(t *testing.T, html string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	require.NoError(t, err)
	return doc
}

func newTestDetector() *ContentDetector {
	return NewContentDetector(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestIsAutoSelector(t *testing.T) {
	for sel, want := range map[string]bool{
		"auto": true, "AUTO": true, "Auto": true,
		"body": false, "article": false, "": false, "automatic": false,
	} {
		assert.Equal(t, want, IsAutoSelector(sel), sel)
	}
}

func TestDetectPage_GeneratorTier(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		framework Framework
		selector  string
	}{
		{"docusaurus", `<html><head><meta name="generator" content="Docusaurus v3.10.1"></head>
			<body><main><article><div class="theme-doc-markdown">` + filler + `</div></article></main></body></html>`,
			FrameworkDocusaurus, "article .theme-doc-markdown"},
		{"vitepress", `<html><head><meta name="generator" content="VitePress v2.0.0-alpha.17"></head>
			<body><div id="VPContent"><main class="main"><div class="vp-doc">` + filler + `</div></main></div></body></html>`,
			FrameworkVitePress, ".vp-doc"},
		{"starlight-double-generator", `<html><head><meta name="generator" content="Astro v7.2.10"><meta name="generator" content="Starlight v0.42.0"></head>
			<body><main data-pagefind-body><div class="sl-markdown-content">` + filler + `</div></main></body></html>`,
			FrameworkStarlight, "main[data-pagefind-body] .sl-markdown-content"},
		{"mintlify", `<html><head><meta name="generator" content="Mintlify"></head>
			<body><div id="content-area"><div id="content" class="mdx-content">` + filler + `</div></div></body></html>`,
			FrameworkMintlify, "#content-area .mdx-content"},
		{"gitbook", `<html><head><meta name="generator" content="GitBook (bcd1c41)"></head>
			<body><main><div class="contents">` + filler + `</div></main></body></html>`,
			FrameworkGitBook, "main div.contents"},
		{"fern", `<html><head><meta name="generator" content="https://buildwithfern.com"></head>
			<body><header id="fern-header"></header><main class="fern-main"><article><div class="fern-prose prose">` + filler + `</div></article></main></body></html>`,
			FrameworkFern, ".fern-prose"},
		{"mkdocs-material", `<html><head><meta name="generator" content="mkdocs-1.6.1, mkdocs-material-9.7.0"></head>
			<body><div class="md-content" data-md-component="content"><article class="md-content__inner">` + filler + `</article></div></body></html>`,
			FrameworkMkDocsMaterial, "article.md-content__inner"},
		{"zensical-uses-material-layout", `<html><head><meta name="generator" content="zensical-0.0.57"></head>
			<body><div class="md-content"><article class="md-content__inner">` + filler + `</article></div></body></html>`,
			FrameworkMkDocsMaterial, "article.md-content__inner"},
		{"antora", `<html><head><meta name="generator" content="Antora 3.2.0"></head>
			<body><main class="article"><article class="doc">` + filler + `</article></main></body></html>`,
			FrameworkAntora, "article.doc"},
		{"doxygen", `<html><head><meta name="generator" content="Doxygen 1.9.8"></head>
			<body><div id="top"><div id="titlearea">T</div><div class="contents">` + filler + `</div></div></body></html>`,
			FrameworkDoxygen, "div.contents"},
		{"rustdoc", `<html data-rustdoc-version="1.82"><head><meta name="generator" content="rustdoc"></head>
			<body><section id="main-content" class="content">` + filler + `</section></body></html>`,
			FrameworkRustdoc, "#main-content"},
		{"javadoc", `<html><head><meta name="generator" content="javadoc/ClassWriterImpl"></head>
			<body><main role="main">` + filler + `</main></body></html>`,
			FrameworkJavadoc, "main[role='main']"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := DetectPage(parseDoc(t, tt.html))
			assert.Equal(t, tt.framework, r.Framework)
			assert.Equal(t, SourceGenerator, r.Source)
			assert.Equal(t, ConfidenceHigh, r.Confidence)
			assert.False(t, r.Fallback)
			assert.True(t, validateSelector(parseDoc(t, tt.html), tt.selector))
			assert.Contains(t, r.Selector, tt.selector)
			assert.NotEmpty(t, r.Version)
		})
	}
}

func TestDetectPage_DOMTier(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		framework Framework
	}{
		{"fumadocs", `<html><body><div id="nd-docs-layout"><article id="nd-page"><div class="prose">` + filler + `</div></article></body></html>`, FrameworkFumadocs},
		{"nextra-gated-on-nextjs", `<html><head><meta name="generator" content="Next.js"></head>
			<body><div class="nextra-toc"></div><main data-pagefind-body="true">` + filler + `</main></body></html>`, FrameworkNextra},
		{"furo", `<html><head><link href="_static/styles/furo.css" rel="stylesheet"></head>
			<body><article id="furo-main-content" role="main">` + filler + `</article></body></html>`, FrameworkSphinxFuro},
		{"pydata", `<html><head><script src="_static/scripts/pydata-sphinx-theme.js"></script></head>
			<body><main id="main-content" class="bd-main"><article class="bd-article">` + filler + `</article></main></body></html>`, FrameworkSphinxPyData},
		{"sphinx-book-beats-pydata", `<html><head><script src="_static/scripts/sphinx-book-theme.js"></script></head>
			<body><div class="sbt-scroll-pixel-helper"></div><article class="bd-article">` + filler + `</article></body></html>`, FrameworkSphinxBook},
		{"sphinx-rtd", `<html><head><script src="_static/js/theme.js"></script></head>
			<body class="wy-body-for-nav"><div class="rst-content"><div itemprop="articleBody">` + filler + `</div></div></body></html>`, FrameworkSphinxRTD},
		{"sphinx-classic", `<html><head><script src="_static/documentation_options.js"></script></head>
			<body><div class="document"><div class="body"><section id="intro"><h1>Intro<a class="headerlink" href="#intro">#</a></h1>` + filler + `</section></div></div></body></html>`, FrameworkSphinx},
		{"mdbook", `<html><body><nav id="mdbook-sidebar"></nav><div id="mdbook-content"><main>` + filler + `</main></div></body></html>`, FrameworkMdBook},
		{"godoc", `<html><body><article class="go-Main-article"><div class="Documentation-content js-docContent">` + filler + `</div></article></body></html>`, FrameworkGodoc},
		{"docsy", `<html><body><div class="td-sidebar"></div><div class="td-content">` + filler + `</div></body></html>`, FrameworkDocsy},
		{"just-the-docs", `<html><head><meta name="generator" content="Jekyll v4.4.1"><link rel="stylesheet" href="/assets/css/just-the-docs-default.css"></head>
			<body><div class="side-bar"></div><div id="main-content" class="main-content"><main>` + filler + `</main></div></body></html>`, FrameworkJustTheDocs},
		{"readme", `<html><head><meta name="readme-deploy" content="5.400"></head>
			<body><article class="rm-Article"><div data-testid="RDMD" class="rm-Markdown markdown-body">` + filler + `</div></article></body></html>`, FrameworkReadMe},
		{"intercom", `<html><body><div class="article intercom-force-break"><div class="article_body">` + filler + `</div></div></body></html>`, FrameworkIntercom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := DetectPage(parseDoc(t, tt.html))
			assert.Equal(t, tt.framework, r.Framework)
			assert.False(t, r.Fallback)
			assert.NotEmpty(t, r.Selector)
		})
	}
}

func TestDetectPage_MentionsDoNotTrigger(t *testing.T) {
	// The old detector matched raw-HTML substrings, so a page merely
	// mentioning a framework was misdetected (mintlify.com -> gitbook).
	html := `<html><head><title>Comparison</title></head><body><main>
		<p>Unlike GitBook or Docusaurus, our product integrates mkdocs and readthedocs
		workflows seamlessly. ` + filler + `</p></main></body></html>`
	r := DetectPage(parseDoc(t, html))
	assert.Equal(t, FrameworkUnknown, r.Framework)
	assert.True(t, r.Fallback)
}

func TestDetectPage_UnvalidatedSelectorDemotesToFallback(t *testing.T) {
	// Generator says Docusaurus but the page has none of its content
	// containers (custom theme): framework is reported, selector is not.
	html := `<html><head><meta name="generator" content="Docusaurus v3.0.0"></head>
		<body><div class="totally-custom">` + filler + `</div></body></html>`
	r := DetectPage(parseDoc(t, html))
	assert.Equal(t, FrameworkDocusaurus, r.Framework)
	assert.True(t, r.Fallback)
	assert.Empty(t, r.Selector)
	assert.Equal(t, ConfidenceUnvalidated, r.Confidence)
}

func TestDetectPage_Shells(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		framework Framework
	}{
		{"docsify", `<html><body><div id="app"></div><script>window.$docsify = {name: 'x'}</script><script src="//cdn.jsdelivr.net/npm/docsify@4/lib/docsify.min.js"></script></body></html>`, FrameworkDocsify},
		{"swagger-ui", `<html><head><link rel="stylesheet" href="./swagger-ui.css"></head><body><div id="swagger-ui"></div><script src="./swagger-ui-bundle.js"></script></body></html>`, FrameworkSwaggerUI},
		{"redoc", `<html><body><redoc spec-url="/openapi.json"></redoc><script src="https://cdn.redoc.ly/redoc/latest/bundles/redoc.standalone.js"></script></body></html>`, FrameworkRedoc},
		{"scalar", `<html><body><div id="app"></div><script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script></body></html>`, FrameworkScalar},
		{"document360-empty-article", `<html><body><d360-article-content></d360-article-content><p>` + filler + `</p></body></html>`, FrameworkDocument360},
		{"near-empty-body", `<html><head><script src="/_next/static/chunks/main.js"></script></head><body><div id="root"></div></body></html>`, FrameworkJSShell},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := DetectPage(parseDoc(t, tt.html))
			assert.Equal(t, tt.framework, r.Framework)
			assert.True(t, r.Fallback)
			assert.Equal(t, ConfidenceJSRendered, r.Confidence)
			assert.Equal(t, SourceShell, r.Source)
		})
	}
}

func TestDetectPage_SSGLandingPageIsNotAShell(t *testing.T) {
	// A hero landing page can have under 200 chars of text, but a generator
	// meta proves server rendering: report the framework (unvalidated), never
	// js-shell.
	html := `<html><head><meta name="generator" content="Docusaurus v3.7.0"></head>
		<body><div id="__docusaurus" data-docusaurus><h1>Product</h1><a href="/docs">Get started</a></div></body></html>`
	r := DetectPage(parseDoc(t, html))
	assert.Equal(t, FrameworkDocusaurus, r.Framework)
	assert.Equal(t, ConfidenceUnvalidated, r.Confidence)
	assert.True(t, r.Fallback)
}

func TestDetectPage_ExcludeCarried(t *testing.T) {
	html := `<html><body><article class="go-Main-article"><div class="Documentation-content">
		<div class="Documentation-toc">toc entries</div>` + filler + `</div></article></body></html>`
	r := DetectPage(parseDoc(t, html))
	assert.Equal(t, FrameworkGodoc, r.Framework)
	assert.Contains(t, r.Exclude, ".Documentation-toc")
}

func TestDetect_CachesConfirmedOnly(t *testing.T) {
	d := newTestDetector()
	u1, _ := url.Parse("https://cached.example.com/a")
	u2, _ := url.Parse("https://cached.example.com/b")

	confirmed := parseDoc(t, `<html><head><meta name="generator" content="Docusaurus v3.0.0"></head>
		<body><main><article><div class="theme-doc-markdown">`+filler+`</div></article></main></body></html>`)
	r1 := d.Detect(confirmed, u1)
	require.Equal(t, FrameworkDocusaurus, r1.Framework)

	unrelated := parseDoc(t, `<html><body><main>`+filler+`</main></body></html>`)
	r2 := d.Detect(unrelated, u2)
	assert.Equal(t, FrameworkDocusaurus, r2.Framework, "second page on same domain served from cache")

	uShell, _ := url.Parse("https://shell.example.com/x")
	shell := parseDoc(t, `<html><body><div id="root"></div></body></html>`)
	rs := d.Detect(shell, uShell)
	require.Equal(t, FrameworkJSShell, rs.Framework)

	real := parseDoc(t, `<html><head><meta name="generator" content="Antora 3.2.0"></head>
		<body><article class="doc">`+filler+`</article></body></html>`)
	rr := d.Detect(real, uShell)
	assert.Equal(t, FrameworkAntora, rr.Framework, "shell result must not poison the domain cache")
}

func TestSelectorCache(t *testing.T) {
	cache := NewSelectorCache()
	_, ok := cache.Get("example.com")
	assert.False(t, ok)

	cache.Set("example.com", DetectionResult{Framework: FrameworkDocusaurus, Selector: "article"})
	got, ok := cache.Get("example.com")
	assert.True(t, ok)
	assert.Equal(t, FrameworkDocusaurus, got.Framework)
}
