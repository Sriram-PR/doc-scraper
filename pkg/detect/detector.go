package detect

import (
	"net/url"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
)

// Framework represents a detected documentation framework
type Framework string

const (
	FrameworkUnknown     Framework = "unknown"
	FrameworkDocusaurus  Framework = "docusaurus"
	FrameworkMkDocs      Framework = "mkdocs"
	FrameworkSphinx      Framework = "sphinx"
	FrameworkGitBook     Framework = "gitbook"
	FrameworkReadTheDocs Framework = "readthedocs"
)

// DetectionResult contains the result of content selector detection
type DetectionResult struct {
	Framework Framework // Detected framework (or unknown)
	Selector  string    // CSS selector for main content
	Fallback  bool      // True if using readability fallback
}

// ContentDetector detects the appropriate content selector for a page.
type ContentDetector struct {
	cache *SelectorCache
	log   *logrus.Entry
}

// NewContentDetector creates a new content detector with per-domain caching.
func NewContentDetector(log *logrus.Entry) *ContentDetector {
	return &ContentDetector{
		cache: NewSelectorCache(),
		log:   log,
	}
}

// Detect returns the best content selector for the given document, using cache then framework
// detection then readability fallback.
func (d *ContentDetector) Detect(doc *goquery.Document, pageURL *url.URL) DetectionResult {
	domain := pageURL.Hostname()

	if cached, ok := d.cache.Get(domain); ok {
		d.log.Debugf("Using cached selector for domain %s: %s (framework: %s)", domain, cached.Selector, cached.Framework)
		return cached
	}

	result := d.detectFramework(doc)
	if result.Framework != FrameworkUnknown {
		d.log.Infof("Detected framework %s for domain %s, using selector: %s", result.Framework, domain, result.Selector)
		d.cache.Set(domain, result)
		return result
	}

	// No framework matched; readability extraction will be used (no CSS selector needed).
	result = DetectionResult{
		Framework: FrameworkUnknown,
		Selector:  "", // Empty selector signals readability fallback
		Fallback:  true,
	}
	d.log.Infof("No framework detected for domain %s, will use readability extraction", domain)
	d.cache.Set(domain, result)
	return result
}

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

// IsAutoSelector reports whether the selector value indicates auto-detection.
func IsAutoSelector(selector string) bool {
	return selector == "auto" || selector == "AUTO" || selector == "Auto"
}
