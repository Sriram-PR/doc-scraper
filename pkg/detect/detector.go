package detect

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Framework identifies a detected documentation generator or hosting platform.
type Framework string

const (
	FrameworkUnknown        Framework = "unknown"
	FrameworkDocusaurus     Framework = "docusaurus"
	FrameworkVitePress      Framework = "vitepress"
	FrameworkVuePress       Framework = "vuepress"
	FrameworkStarlight      Framework = "starlight"
	FrameworkNextra         Framework = "nextra"
	FrameworkFumadocs       Framework = "fumadocs"
	FrameworkMintlify       Framework = "mintlify"
	FrameworkFern           Framework = "fern"
	FrameworkGitBook        Framework = "gitbook"
	FrameworkMkDocsMaterial Framework = "mkdocs-material"
	FrameworkMkDocsRTD      Framework = "mkdocs-rtd"
	FrameworkMkDocs         Framework = "mkdocs"
	FrameworkSphinx         Framework = "sphinx"
	FrameworkSphinxFuro     Framework = "sphinx-furo"
	FrameworkSphinxBook     Framework = "sphinx-book"
	FrameworkSphinxPyData   Framework = "sphinx-pydata"
	FrameworkSphinxRTD      Framework = "sphinx-rtd"
	FrameworkAntora         Framework = "antora"
	FrameworkDocsy          Framework = "docsy"
	FrameworkHugoBook       Framework = "hugo-book"
	FrameworkGeekdoc        Framework = "geekdoc"
	FrameworkJustTheDocs    Framework = "just-the-docs"
	FrameworkMdBook         Framework = "mdbook"
	FrameworkRustdoc        Framework = "rustdoc"
	FrameworkGodoc          Framework = "godoc"
	FrameworkJavadoc        Framework = "javadoc"
	FrameworkDoxygen        Framework = "doxygen"
	FrameworkTypeDoc        Framework = "typedoc"
	FrameworkWriterside     Framework = "writerside"
	FrameworkReadMe         Framework = "readme"
	FrameworkIntercom       Framework = "intercom"
	FrameworkDocus          Framework = "docus"
	FrameworkDocsify        Framework = "docsify"
	FrameworkSwaggerUI      Framework = "swagger-ui"
	FrameworkRedoc          Framework = "redoc"
	FrameworkScalar         Framework = "scalar"
	FrameworkDocument360    Framework = "document360"
	FrameworkJSShell        Framework = "js-shell"
)

// Confidence grades how much a detection should be trusted.
type Confidence string

const (
	ConfidenceHigh        Confidence = "high"
	ConfidenceMedium      Confidence = "medium"
	ConfidenceUnvalidated Confidence = "unvalidated"
	ConfidenceFallback    Confidence = "fallback"
	ConfidenceJSRendered  Confidence = "js-rendered"
)

// Source names the signal tier that decided the detection.
type Source string

const (
	SourceGenerator Source = "generator"
	SourceDOM       Source = "dom"
	SourceAsset     Source = "asset"
	SourceShell     Source = "shell"
	SourceNone      Source = "none"
)

// DetectionResult reports the detected framework and how to extract content
// from it. Selector and Exclude are comma-separated CSS lists (priority order,
// innermost first). Fallback true means no validated selector exists and
// readability extraction should be used. A js-rendered Confidence means the
// page is a script shell whose content never appears in static HTML at all.
type DetectionResult struct {
	Framework  Framework
	Selector   string
	Exclude    string
	Fallback   bool
	Confidence Confidence
	Source     Source
	Version    string
}

// minContentChars is the least visible text a candidate content selector must
// capture on the sampled page to count as validated.
const minContentChars = 200

type ContentDetector struct {
	cache *SelectorCache
	log   *slog.Logger
}

// NewContentDetector creates a new content detector with per-domain caching.
func NewContentDetector(log *slog.Logger) *ContentDetector {
	return &ContentDetector{
		cache: NewSelectorCache(),
		log:   log,
	}
}

// Detect returns the best content selector for the given document. Confirmed
// framework detections are cached per domain; shell and unknown results are
// re-evaluated per page since they are often page-specific (redirect stubs,
// error pages).
func (d *ContentDetector) Detect(doc *goquery.Document, pageURL *url.URL) DetectionResult {
	domain := pageURL.Hostname()

	if cached, ok := d.cache.Get(domain); ok {
		return cached
	}

	result := DetectPage(doc)
	switch {
	case result.Confidence == ConfidenceJSRendered:
		d.log.Info(fmt.Sprintf("Page at %s looks JavaScript-rendered (%s); static crawl may capture nothing", pageURL, result.Framework))
	case result.Framework == FrameworkUnknown:
		d.log.Info(fmt.Sprintf("No framework detected for domain %s, will use readability extraction", domain))
	case result.Fallback:
		d.log.Info(fmt.Sprintf("Detected framework %s for domain %s but no selector validated; using readability", result.Framework, domain))
		d.cache.Set(domain, result)
	default:
		d.log.Info(fmt.Sprintf("Detected framework %s for domain %s (%s, %s), using selector: %s",
			result.Framework, domain, result.Source, result.Confidence, result.Selector))
		d.cache.Set(domain, result)
	}
	return result
}

// DetectPage runs framework detection on a single parsed page with no caching.
func DetectPage(doc *goquery.Document) DetectionResult {
	facts := collectPageFacts(doc)

	if shell := detectShell(doc, facts); shell != "" {
		return DetectionResult{Framework: shell, Fallback: true, Confidence: ConfidenceJSRendered, Source: SourceShell}
	}

	best, score := matchSignature(doc, facts)
	if best == nil {
		// The generic empty-body probe runs only when no signature matched: a
		// page declaring an SSG generator is server-rendered even when it is a
		// near-empty landing page.
		if facts.bodyChars < minContentChars {
			return DetectionResult{Framework: FrameworkJSShell, Fallback: true, Confidence: ConfidenceJSRendered, Source: SourceShell}
		}
		return DetectionResult{Framework: FrameworkUnknown, Fallback: true, Confidence: ConfidenceFallback, Source: SourceNone}
	}

	result := DetectionResult{
		Framework: best.Framework,
		Selector:  best.Selector,
		Exclude:   best.Exclude,
		Source:    scoreSource(best, facts),
		Version:   generatorVersion(best, facts),
	}
	if !validateSelector(doc, best.Selector) {
		result.Fallback = true
		result.Selector = ""
		result.Exclude = ""
		result.Confidence = ConfidenceUnvalidated
		return result
	}
	if result.Source == SourceGenerator || score >= 6 {
		result.Confidence = ConfidenceHigh
	} else {
		result.Confidence = ConfidenceMedium
	}
	return result
}

type pageFacts struct {
	generators []string
	assets     []string
	bodyChars  int
}

func collectPageFacts(doc *goquery.Document) pageFacts {
	var f pageFacts
	doc.Find(`meta[name="generator"]`).Each(func(_ int, s *goquery.Selection) {
		if c, ok := s.Attr("content"); ok {
			f.generators = append(f.generators, strings.ToLower(strings.TrimSpace(c)))
		}
	})
	doc.Find("script[src]").Each(func(_ int, s *goquery.Selection) {
		if src, ok := s.Attr("src"); ok {
			f.assets = append(f.assets, src)
		}
	})
	doc.Find("link[href]").Each(func(_ int, s *goquery.Selection) {
		if href, ok := s.Attr("href"); ok {
			f.assets = append(f.assets, href)
		}
	})
	f.bodyChars = len(visibleText(doc.Find("body")))
	return f
}

func (f pageFacts) generatorContains(sub string) bool {
	for _, g := range f.generators {
		if strings.Contains(g, sub) {
			return true
		}
	}
	return false
}

func (f pageFacts) assetContains(sub string) bool {
	for _, a := range f.assets {
		if strings.Contains(a, sub) {
			return true
		}
	}
	return false
}

func matchSignature(doc *goquery.Document, facts pageFacts) (*FrameworkSignature, int) {
	var best *FrameworkSignature
	bestScore := 0
	for i := range frameworkSignatures {
		sig := &frameworkSignatures[i]
		score := scoreSignature(doc, sig, facts)
		if score >= 2 && score > bestScore {
			best, bestScore = sig, score
		}
	}
	return best, bestScore
}

func scoreSignature(doc *goquery.Document, sig *FrameworkSignature, facts pageFacts) int {
	for _, veto := range sig.AssetVeto {
		if facts.assetContains(veto) {
			return 0
		}
	}
	score := 0
	for _, g := range sig.GenContains {
		if facts.generatorContains(g) {
			score += 4
			break
		}
	}
	for _, g := range sig.GenGates {
		if facts.generatorContains(g) {
			score++
			break
		}
	}
	for _, q := range sig.DOMAny {
		if doc.Find(q).Length() > 0 {
			score += 2
			break
		}
	}
	for _, a := range sig.AssetSubs {
		if facts.assetContains(a) {
			score++
			break
		}
	}
	return score
}

func scoreSource(sig *FrameworkSignature, facts pageFacts) Source {
	for _, g := range sig.GenContains {
		if facts.generatorContains(g) {
			return SourceGenerator
		}
	}
	for _, q := range sig.DOMAny {
		if q != "" {
			return SourceDOM
		}
	}
	return SourceAsset
}

func generatorVersion(sig *FrameworkSignature, facts pageFacts) string {
	for _, g := range sig.GenContains {
		for _, gen := range facts.generators {
			if strings.Contains(gen, g) {
				return gen
			}
		}
	}
	return ""
}

// validateSelector accepts a comma-separated selector list when any
// alternative matches at least one element holding minContentChars of visible
// text. A recognized framework whose selectors all come up empty is treated as
// a fallback case rather than trusted blindly.
func validateSelector(doc *goquery.Document, selector string) bool {
	for _, sel := range strings.Split(selector, ",") {
		if sel = strings.TrimSpace(sel); sel == "" {
			continue
		}
		found := doc.Find(sel)
		if found.Length() > 0 && len(visibleText(found.First())) >= minContentChars {
			return true
		}
	}
	return false
}

func detectShell(doc *goquery.Document, facts pageFacts) Framework {
	switch {
	case facts.assetContains("docsify.min.js") || facts.assetContains("docsify@") || scriptTextContains(doc, "$docsify"):
		return FrameworkDocsify
	case doc.Find("#swagger-ui").Length() > 0 &&
		(facts.assetContains("swagger-ui-bundle") || facts.assetContains("swagger-initializer") || facts.assetContains("swagger-ui.css")):
		return FrameworkSwaggerUI
	case doc.Find("redoc").Length() > 0 || facts.assetContains("redoc.standalone.js"):
		return FrameworkRedoc
	case facts.assetContains("@scalar/api-reference"):
		return FrameworkScalar
	case isEmptyElement(doc, "d360-article-content"):
		return FrameworkDocument360
	}
	return ""
}

func scriptTextContains(doc *goquery.Document, sub string) bool {
	found := false
	doc.Find("script:not([src])").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if strings.Contains(s.Text(), sub) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isEmptyElement(doc *goquery.Document, selector string) bool {
	s := doc.Find(selector)
	return s.Length() > 0 && len(strings.TrimSpace(s.Text())) == 0
}

func visibleText(sel *goquery.Selection) string {
	c := sel.Clone()
	c.Find("script, style, noscript").Remove()
	return strings.Join(strings.Fields(c.Text()), " ")
}

// IsAutoSelector reports whether the selector value indicates auto-detection.
func IsAutoSelector(selector string) bool {
	return strings.EqualFold(selector, "auto")
}
