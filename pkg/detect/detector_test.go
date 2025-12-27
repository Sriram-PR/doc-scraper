package detect

import (
	"net/url"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
)

func TestIsAutoSelector(t *testing.T) {
	tests := []struct {
		selector string
		want     bool
	}{
		{"auto", true},
		{"AUTO", true},
		{"Auto", true},
		{"body", false},
		{"article", false},
		{"", false},
		{"automatic", false},
	}

	for _, tt := range tests {
		t.Run(tt.selector, func(t *testing.T) {
			got := IsAutoSelector(tt.selector)
			if got != tt.want {
				t.Errorf("IsAutoSelector(%q) = %v, want %v", tt.selector, got, tt.want)
			}
		})
	}
}

func TestDetectDocusaurus(t *testing.T) {
	html := `<!DOCTYPE html>
<html data-docusaurus>
<head><title>Docusaurus Test</title></head>
<body>
<div class="docusaurus-wrapper">
<article class="theme-doc-markdown">
<h1>Hello World</h1>
<p>This is Docusaurus content.</p>
</article>
</div>
</body>
</html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("Failed to parse HTML: %v", err)
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	detector := NewContentDetector(log)

	pageURL, _ := url.Parse("https://docusaurus.io/docs/")
	result := detector.Detect(doc, pageURL)

	if result.Framework != FrameworkDocusaurus {
		t.Errorf("Expected framework %s, got %s", FrameworkDocusaurus, result.Framework)
	}
	if result.Fallback {
		t.Error("Expected Fallback to be false for detected framework")
	}
	if result.Selector == "" {
		t.Error("Expected non-empty selector")
	}
}

func TestDetectMkDocs(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head><title>MkDocs Test</title></head>
<body>
<div class="md-content" data-md-component="content">
<article class="md-content__inner">
<h1>MkDocs Documentation</h1>
<p>This is MkDocs Material content.</p>
</article>
</div>
</body>
</html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("Failed to parse HTML: %v", err)
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	detector := NewContentDetector(log)

	pageURL, _ := url.Parse("https://squidfunk.github.io/mkdocs-material/")
	result := detector.Detect(doc, pageURL)

	if result.Framework != FrameworkMkDocs {
		t.Errorf("Expected framework %s, got %s", FrameworkMkDocs, result.Framework)
	}
}

func TestDetectSphinx(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head>
<title>Sphinx Test</title>
<script src="_static/sphinx_highlight.js"></script>
</head>
<body>
<div class="sphinxsidebar">Sidebar</div>
<div class="document">
<div class="body">
<h1>Sphinx Documentation</h1>
<p>Created using Sphinx.</p>
</div>
</div>
</body>
</html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("Failed to parse HTML: %v", err)
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	detector := NewContentDetector(log)

	pageURL, _ := url.Parse("https://www.sphinx-doc.org/en/master/")
	result := detector.Detect(doc, pageURL)

	if result.Framework != FrameworkSphinx {
		t.Errorf("Expected framework %s, got %s", FrameworkSphinx, result.Framework)
	}
}

func TestDetectReadTheDocs(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head>
<title>RTD Test</title>
<script src="https://readthedocs.org/static/javascript/readthedocs-doc-embed.js"></script>
</head>
<body>
<div class="wy-nav-content">
<div class="rst-content">
<h1>ReadTheDocs Documentation</h1>
<p>Hosted on ReadTheDocs.</p>
</div>
</div>
</body>
</html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("Failed to parse HTML: %v", err)
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	detector := NewContentDetector(log)

	pageURL, _ := url.Parse("https://example.readthedocs.io/en/latest/")
	result := detector.Detect(doc, pageURL)

	if result.Framework != FrameworkReadTheDocs {
		t.Errorf("Expected framework %s, got %s", FrameworkReadTheDocs, result.Framework)
	}
}

func TestDetectGitBook(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head><title>GitBook Test</title></head>
<body>
<main class="gitbook-root">
<section class="normal markdown-section">
<h1>GitBook Documentation</h1>
<p>This is GitBook content.</p>
</section>
</main>
</body>
</html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("Failed to parse HTML: %v", err)
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	detector := NewContentDetector(log)

	pageURL, _ := url.Parse("https://docs.gitbook.com/")
	result := detector.Detect(doc, pageURL)

	if result.Framework != FrameworkGitBook {
		t.Errorf("Expected framework %s, got %s", FrameworkGitBook, result.Framework)
	}
}

func TestDetectUnknownFallback(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head><title>Unknown Site</title></head>
<body>
<div id="content">
<h1>Some Article</h1>
<p>This is a generic website with no known framework.</p>
<p>It has multiple paragraphs of content that should be extracted.</p>
</div>
</body>
</html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("Failed to parse HTML: %v", err)
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	detector := NewContentDetector(log)

	pageURL, _ := url.Parse("https://example.com/page")
	result := detector.Detect(doc, pageURL)

	if result.Framework != FrameworkUnknown {
		t.Errorf("Expected framework %s, got %s", FrameworkUnknown, result.Framework)
	}
	if !result.Fallback {
		t.Error("Expected Fallback to be true for unknown framework")
	}
}

func TestCaching(t *testing.T) {
	html := `<!DOCTYPE html>
<html data-docusaurus>
<head><title>Test</title></head>
<body>
<article class="theme-doc-markdown">Content</article>
</body>
</html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("Failed to parse HTML: %v", err)
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	detector := NewContentDetector(log)

	pageURL1, _ := url.Parse("https://example.com/page1")
	pageURL2, _ := url.Parse("https://example.com/page2")

	// First detection should detect framework
	result1 := detector.Detect(doc, pageURL1)
	if result1.Framework != FrameworkDocusaurus {
		t.Errorf("First detection: expected %s, got %s", FrameworkDocusaurus, result1.Framework)
	}

	// Second detection for same domain should use cache
	result2 := detector.Detect(doc, pageURL2)
	if result2.Framework != FrameworkDocusaurus {
		t.Errorf("Cached detection: expected %s, got %s", FrameworkDocusaurus, result2.Framework)
	}

	// Verify cache size
	if detector.cache.Size() != 1 {
		t.Errorf("Expected cache size 1, got %d", detector.cache.Size())
	}
}

func TestSelectorCache(t *testing.T) {
	cache := NewSelectorCache()

	// Test empty cache
	if _, ok := cache.Get("example.com"); ok {
		t.Error("Expected empty cache to return false")
	}

	// Test set and get
	result := DetectionResult{
		Framework: FrameworkDocusaurus,
		Selector:  "article",
		Fallback:  false,
	}
	cache.Set("example.com", result)

	got, ok := cache.Get("example.com")
	if !ok {
		t.Error("Expected to find cached result")
	}
	if got.Framework != result.Framework {
		t.Errorf("Expected framework %s, got %s", result.Framework, got.Framework)
	}

	// Test size
	if cache.Size() != 1 {
		t.Errorf("Expected size 1, got %d", cache.Size())
	}

	// Test clear
	cache.Clear()
	if cache.Size() != 0 {
		t.Errorf("Expected size 0 after clear, got %d", cache.Size())
	}
}
