package discover

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
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

const readabilityWarning = "extraction falls back to readability; review the preview carefully, code blocks are at risk"

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
		AllowedDomain:     strings.ToLower(r.FinalURL.Hostname()),
		AllowedPathPrefix: r.Scope.Prefix,
		MaxDepth:          r.Scope.MaxDepth,
	}

	switch {
	case selectorOverride != "":
		d.Site.ContentSelector = selectorOverride
		d.Evidence = append(d.Evidence, "content_selector: provided via -selector")
	case r.Detection.Selector != "":
		d.Site.ContentSelector = r.Detection.Selector
		where := "validated on the fetched page"
		if r.Sample != nil {
			where = fmt.Sprintf("validated on %s; the seed page is a landing layout", r.Sample.URL.Path)
		}
		d.Evidence = append(d.Evidence, fmt.Sprintf("content_selector: %s detected via %s (%s confidence), %s",
			r.Detection.Framework, r.Detection.Source, r.Detection.Confidence, where))
	default:
		d.Site.ContentSelector = "auto"
		line := "content_selector: no framework recognized; \"auto\" will use readability extraction"
		if r.Detection.Framework != detect.FrameworkUnknown {
			line = fmt.Sprintf("content_selector: %s recognized but no selector matched this page; \"auto\" will use readability extraction", r.Detection.Framework)
		}
		if r.SampleNote != "" {
			line += "; " + r.SampleNote
		}
		d.Evidence = append(d.Evidence, line)
		d.Warnings = append(d.Warnings, readabilityWarning)
	}

	if r.CrossHost {
		d.Evidence = append(d.Evidence, fmt.Sprintf("allowed_domain: %s (after redirect from %s)", r.FinalURL.Hostname(), hostOf(r.SeedURL)))
	}
	if r.Sitemap.Found {
		covers := fmt.Sprintf("allowed_path_prefix: %s covers %d of %d sitemap URLs", r.Scope.Prefix, r.Scope.PrefixCount, r.Scope.TotalCount)
		if r.Scope.ExcludedCount > 0 {
			covers += fmt.Sprintf(", not counting %d in excluded version/locale trees", r.Scope.ExcludedCount)
		}
		d.Evidence = append(d.Evidence, covers)
		d.Evidence = append(d.Evidence, fmt.Sprintf("max_depth: %d from sitemap path depth under the prefix", r.Scope.MaxDepth))
	} else {
		d.Evidence = append(d.Evidence, fmt.Sprintf("allowed_path_prefix: %s from the URL path (no sitemap to verify against)", r.Scope.Prefix))
	}
	if r.LlmsTxt.Found && len(r.LlmsTxt.Links) >= llmsTxtCorroborationMin {
		d.Evidence = append(d.Evidence, fmt.Sprintf("llms.txt present with %d links, corroborating the docs scope", len(r.LlmsTxt.Links)))
	}

	patterns, generalized := disallowPatterns(r)
	d.Site.DisallowedPathPatterns = patterns
	if len(patterns) > 0 {
		d.Evidence = append(d.Evidence, "disallowed_path_patterns: sibling version/locale trees observed in the sitemap")
		d.Evidence = append(d.Evidence, generalized...)
	}
	return d
}

const versionTreeBody = `v?\d+(?:\.\d+)*(?:\.x)?/`

// localeTreeBody matches the segments isLocaleSegment accepts. It is built on
// call because iso639 is filled in init.
func localeTreeBody() string {
	return "(?:" + strings.Join(slices.Sorted(maps.Keys(iso639)), "|") + `)(?:[-_][a-zA-Z]{2,4})?/`
}

// disallowPatterns excludes sibling version and locale trees, including those
// nested inside the path prefix. Two or more trees of one kind under one
// parent become a single pattern when it matches all of them and no kept
// sitemap path, so trees the site adds later are excluded too; evidence has a
// line for each such pattern. Other trees get anchored literal patterns.
func disallowPatterns(r *Report) (patterns, evidence []string) {
	versions := excludedTrees(r.Scope.SiblingVersions, r.Scope.Prefix)
	locales := excludedTrees(r.Scope.SiblingLocales, r.Scope.Prefix)
	kept := keptPaths(r, append(append([]string{}, versions...), locales...))
	for _, kind := range []struct {
		name, body string
		trees      []string
	}{
		{"version", versionTreeBody, versions},
		{"locale", localeTreeBody(), locales},
	} {
		groups := generalizeTrees(kind.trees, kind.name == "version", kind.body, kept)
		emitted := map[string]bool{}
		for _, tree := range kind.trees {
			parent, _ := splitTree(tree)
			g, ok := groups[parent]
			if !ok || !slices.Contains(g.members, tree) {
				patterns = append(patterns, "^"+regexp.QuoteMeta(tree))
				continue
			}
			if !emitted[parent] {
				emitted[parent] = true
				patterns = append(patterns, g.pattern)
				evidence = append(evidence, fmt.Sprintf(
					"disallowed_path_patterns: %d %s trees under %s generalized; the pattern matches none of the %d kept sitemap URLs",
					len(g.members), kind.name, parent, len(kept)-1))
			}
		}
	}
	return patterns, evidence
}

func excludedTrees(trees []string, prefix string) []string {
	var out []string
	for _, tree := range trees {
		if !strings.HasPrefix(prefix, tree) {
			out = append(out, tree)
		}
	}
	return out
}

// keptPaths is the prefix plus every same-host sitemap path outside the
// excluded trees.
func keptPaths(r *Report, excluded []string) []string {
	kept := []string{r.Scope.Prefix}
	if r.FinalURL == nil {
		return kept
	}
	for _, p := range hostPaths(r.FinalURL, r.Sitemap.URLs) {
		if !slices.ContainsFunc(excluded, func(tree string) bool { return strings.HasPrefix(p, tree) }) {
			kept = append(kept, p)
		}
	}
	return kept
}

type treeGroup struct {
	pattern string
	members []string
}

// generalizeTrees returns, by parent directory, the groups whose pattern
// matches every member tree and no kept path.
func generalizeTrees(trees []string, versions bool, body string, kept []string) map[string]treeGroup {
	byParent := map[string][]string{}
	for _, tree := range trees {
		parent, seg := splitTree(tree)
		if alias, _ := isVersionSegment(seg); versions && alias {
			continue
		}
		byParent[parent] = append(byParent[parent], tree)
	}
	groups := map[string]treeGroup{}
	for parent, members := range byParent {
		if len(members) < 2 {
			continue
		}
		re, err := regexp.Compile("^" + regexp.QuoteMeta(parent) + body)
		if err != nil ||
			slices.ContainsFunc(members, func(m string) bool { return !re.MatchString(m) }) ||
			slices.ContainsFunc(kept, re.MatchString) {
			continue
		}
		groups[parent] = treeGroup{pattern: re.String(), members: members}
	}
	return groups
}

// splitTree splits "/docs/3.9.2/" into "/docs/" and "3.9.2".
func splitTree(tree string) (parent, seg string) {
	t := strings.TrimSuffix(tree, "/")
	i := strings.LastIndex(t, "/")
	return t[:i+1], t[i+1:]
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
