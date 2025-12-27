package detect

import (
	"net/url"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
)

// Framework represents a detected documentation framework
type Framework string

const (
	FrameworkUnknown    Framework = "unknown"
	FrameworkDocusaurus Framework = "docusaurus"
	FrameworkMkDocs     Framework = "mkdocs"
	FrameworkSphinx     Framework = "sphinx"
	FrameworkGitBook    Framework = "gitbook"
	FrameworkReadTheDocs Framework = "readthedocs"
)

// DetectionResult contains the result of content selector detection
type DetectionResult struct {
	Framework Framework // Detected framework (or unknown)
	Selector  string    // CSS selector for main content
	Fallback  bool      // True if using readability fallback
}

// ContentDetector detects the appropriate content selector for a page
type ContentDetector struct {
	cache *SelectorCache
	log   *logrus.Logger
}

// NewContentDetector creates a new content detector with caching
func NewContentDetector(log *logrus.Logger) *ContentDetector {
	return &ContentDetector{
		cache: NewSelectorCache(),
		log:   log,
	}
}

// Detect determines the best content selector for the given document
// It first checks the cache, then tries framework detection, and falls back to readability
func (d *ContentDetector) Detect(doc *goquery.Document, pageURL *url.URL) DetectionResult {
	domain := pageURL.Hostname()

	// Check cache first
	if cached, ok := d.cache.Get(domain); ok {
		d.log.Debugf("Using cached selector for domain %s: %s (framework: %s)", domain, cached.Selector, cached.Framework)
		return cached
	}

	// Try framework detection
	result := d.detectFramework(doc)
	if result.Framework != FrameworkUnknown {
		d.log.Infof("Detected framework %s for domain %s, using selector: %s", result.Framework, domain, result.Selector)
		d.cache.Set(domain, result)
		return result
	}

	// Fall back to readability-based extraction
	// For readability, we don't use a CSS selector - we extract content directly
	result = DetectionResult{
		Framework: FrameworkUnknown,
		Selector:  "", // Empty selector signals readability fallback
		Fallback:  true,
	}
	d.log.Infof("No framework detected for domain %s, will use readability extraction", domain)
	d.cache.Set(domain, result)
	return result
}

// detectFramework attempts to identify the documentation framework from HTML signatures
func (d *ContentDetector) detectFramework(doc *goquery.Document) DetectionResult {
	html, _ := doc.Html()

	for _, sig := range frameworkSignatures {
		if sig.Matches(doc, html) {
			return DetectionResult{
				Framework: sig.Framework,
				Selector:  sig.Selector,
				Fallback:  false,
			}
		}
	}

	return DetectionResult{
		Framework: FrameworkUnknown,
		Selector:  "",
		Fallback:  true,
	}
}

// IsAutoSelector returns true if the selector value indicates auto-detection
func IsAutoSelector(selector string) bool {
	return selector == "auto" || selector == "AUTO" || selector == "Auto"
}
