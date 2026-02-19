package detect

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// FrameworkSignature defines detection patterns for a documentation framework
type FrameworkSignature struct {
	Framework   Framework
	Selector    string   // CSS selector for main content
	Attributes  []string // HTML attributes to look for (e.g., "data-docusaurus")
	Classes     []string // CSS classes to look for
	Scripts     []string // Script src patterns to look for
	HTMLPatterns []string // Substring patterns to look for in raw HTML
}

// Matches returns true if the document matches this framework's signature
func (sig *FrameworkSignature) Matches(doc *goquery.Document, html string) bool {
	// Check for attribute presence
	for _, attr := range sig.Attributes {
		if doc.Find("[" + attr + "]").Length() > 0 {
			return true
		}
	}

	// Check for class presence
	for _, class := range sig.Classes {
		// Handle class patterns with wildcards
		if strings.Contains(class, "*") {
			// Convert glob to prefix match (e.g., "gitbook*" matches class starting with "gitbook")
			prefix := strings.TrimSuffix(class, "*")
			found := false
			doc.Find("[class]").Each(func(i int, s *goquery.Selection) {
				if found {
					return
				}
				classAttr, exists := s.Attr("class")
				if exists {
					for _, c := range strings.Fields(classAttr) {
						if strings.HasPrefix(c, prefix) {
							found = true
							return
						}
					}
				}
			})
			if found {
				return true
			}
		} else if doc.Find("." + class).Length() > 0 {
			return true
		}
	}

	// Check for script patterns
	for _, pattern := range sig.Scripts {
		found := false
		doc.Find("script[src]").Each(func(i int, s *goquery.Selection) {
			if found {
				return
			}
			src, exists := s.Attr("src")
			if exists && strings.Contains(src, pattern) {
				found = true
			}
		})
		if found {
			return true
		}
	}

	// Check for HTML patterns
	htmlLower := strings.ToLower(html)
	for _, pattern := range sig.HTMLPatterns {
		if strings.Contains(htmlLower, strings.ToLower(pattern)) {
			return true
		}
	}

	return false
}

// frameworkSignatures contains detection patterns for known documentation frameworks
// Order matters: more specific patterns should come first
var frameworkSignatures = []FrameworkSignature{
	// Docusaurus v2/v3
	{
		Framework: FrameworkDocusaurus,
		Selector:  "article[class*='theme-doc'], .theme-doc-markdown, article.markdown, main article",
		Attributes: []string{
			"data-docusaurus",
			"data-docusaurus-root-container",
		},
		Classes: []string{
			"docusaurus-wrapper",
			"theme-doc-markdown",
		},
		HTMLPatterns: []string{
			"__docusaurus",
			"docusaurus.io",
		},
	},

	// MkDocs Material
	{
		Framework: FrameworkMkDocs,
		Selector:  "article.md-content__inner, .md-content article, .md-content",
		Attributes: []string{
			"data-md-component",
			"data-md-color-scheme",
		},
		Classes: []string{
			"md-content",
			"md-main",
		},
		HTMLPatterns: []string{
			"mkdocs",
			"material for mkdocs",
		},
	},

	// ReadTheDocs (check before Sphinx since RTD often uses Sphinx)
	{
		Framework: FrameworkReadTheDocs,
		Selector:  ".rst-content, div[role='main'], .document",
		Classes: []string{
			"rst-content",
			"wy-nav-content",
		},
		Scripts: []string{
			"readthedocs",
			"rtd",
		},
		HTMLPatterns: []string{
			"readthedocs.org",
			"readthedocs.io",
			"sphinx-rtd-theme",
		},
	},

	// Sphinx (standalone)
	{
		Framework: FrameworkSphinx,
		Selector:  "div.document, div.body, article.bd-article, main.bd-main",
		Classes: []string{
			"sphinxsidebar",
			"sphinx-tabs",
		},
		Scripts: []string{
			"searchindex.js",
			"_static/sphinx",
		},
		HTMLPatterns: []string{
			"created using sphinx",
			"sphinx-doc.org",
			"_static/alabaster",
			"_static/pygments",
		},
	},

	// GitBook
	{
		Framework: FrameworkGitBook,
		Selector:  "section.normal.markdown-section, .page-inner section, main[class*='gitbook']",
		Classes: []string{
			"gitbook*",
			"markdown-section",
		},
		HTMLPatterns: []string{
			"gitbook",
			"gb-page",
		},
	},
}

// GetFrameworkSelector returns the recommended selector for a known framework
func GetFrameworkSelector(fw Framework) string {
	for _, sig := range frameworkSignatures {
		if sig.Framework == fw {
			return sig.Selector
		}
	}
	return ""
}
