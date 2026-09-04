package discover

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/detect"
)

// Draft is the proposed site entry plus the evidence backing each field, so
// the user confirms an explained draft rather than a black-box guess.
type Draft struct {
	SiteKey    string
	Site       config.SiteConfig
	Framework  detect.Framework
	Confidence detect.Confidence
	Source     detect.Source
	Version    string
	PageCount  int
	Evidence   []string
	Warnings   []string
}

// BuildDraft assembles the draft config from a discovery report.
// selectorOverride, when non-empty, replaces the detected content selector.
func BuildDraft(r *Report, selectorOverride string) *Draft {
	d := &Draft{
		SiteKey:    deriveSiteKey(r.FinalURL.Hostname()),
		Framework:  r.Detection.Framework,
		Confidence: r.Detection.Confidence,
		Source:     r.Detection.Source,
		Version:    r.Detection.Version,
		PageCount:  r.Scope.PrefixCount,
		Warnings:   r.Warnings,
	}

	d.Site = config.SiteConfig{
		StartURLs:         []string{r.FinalURL.String()},
		AllowedDomain:     r.FinalURL.Hostname(),
		AllowedPathPrefix: r.Scope.Prefix,
		MaxDepth:          r.Scope.MaxDepth,
	}

	switch {
	case selectorOverride != "":
		d.Site.ContentSelector = selectorOverride
		d.Evidence = append(d.Evidence, "content_selector: provided via -selector")
	case r.Detection.Selector != "":
		d.Site.ContentSelector = r.Detection.Selector
		d.Evidence = append(d.Evidence, fmt.Sprintf("content_selector: %s detected via %s (%s confidence), validated on the fetched page",
			r.Detection.Framework, r.Detection.Source, r.Detection.Confidence))
	default:
		d.Site.ContentSelector = "auto"
		if r.Detection.Framework != detect.FrameworkUnknown {
			d.Evidence = append(d.Evidence, fmt.Sprintf("content_selector: %s recognized but no selector matched this page; \"auto\" will use readability extraction", r.Detection.Framework))
		} else {
			d.Evidence = append(d.Evidence, "content_selector: no framework recognized; \"auto\" will use readability extraction")
		}
		d.Warnings = append(d.Warnings, "extraction falls back to readability; review the preview carefully, code blocks are at risk")
	}

	if r.CrossHost {
		d.Evidence = append(d.Evidence, fmt.Sprintf("allowed_domain: %s (after redirect from %s)", r.FinalURL.Hostname(), hostOf(r.SeedURL)))
	}
	if r.Sitemap.Found {
		d.Evidence = append(d.Evidence, fmt.Sprintf("allowed_path_prefix: %s covers %d of %d sitemap URLs", r.Scope.Prefix, r.Scope.PrefixCount, r.Scope.TotalCount))
		d.Evidence = append(d.Evidence, fmt.Sprintf("max_depth: %d from sitemap path depth under the prefix", r.Scope.MaxDepth))
	} else {
		d.Evidence = append(d.Evidence, fmt.Sprintf("allowed_path_prefix: %s from the URL path (no sitemap to verify against)", r.Scope.Prefix))
	}
	if r.LlmsTxt.Found && len(r.LlmsTxt.Links) >= llmsTxtCorroborationMin {
		d.Evidence = append(d.Evidence, fmt.Sprintf("llms.txt present with %d links, corroborating the docs scope", len(r.LlmsTxt.Links)))
	}

	d.Site.DisallowedPathPatterns = disallowPatterns(r)
	if len(d.Site.DisallowedPathPatterns) > 0 {
		d.Evidence = append(d.Evidence, "disallowed_path_patterns: sibling version/locale trees observed in the sitemap")
	}
	return d
}

// disallowPatterns excludes sibling version and locale trees that the path
// prefix does not already fence out, as anchored literal patterns built from
// directories actually observed in the sitemap.
func disallowPatterns(r *Report) []string {
	var out []string
	for _, sib := range append(append([]string{}, r.Scope.SiblingVersions...), r.Scope.SiblingLocales...) {
		if strings.HasPrefix(r.Scope.Prefix, sib) || strings.HasPrefix(sib, r.Scope.Prefix) {
			continue
		}
		out = append(out, "^"+regexp.QuoteMeta(sib))
	}
	return out
}

var siteKeyStrip = map[string]struct{}{"www": {}, "docs": {}, "doc": {}, "documentation": {}, "help": {}, "developer": {}, "developers": {}}

func deriveSiteKey(host string) string {
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if _, skip := siteKeyStrip[l]; !skip && l != "" {
			return sanitizeKey(l) + "_docs"
		}
	}
	return sanitizeKey(strings.Join(labels, "_")) + "_docs"
}

var keyCharRe = regexp.MustCompile(`[^a-z0-9_]+`)

func sanitizeKey(s string) string {
	s = keyCharRe.ReplaceAllString(strings.ToLower(s), "_")
	return strings.Trim(s, "_")
}

func hostOf(rawURL string) string {
	if i := strings.Index(rawURL, "://"); i >= 0 {
		rest := rawURL[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	return rawURL
}
