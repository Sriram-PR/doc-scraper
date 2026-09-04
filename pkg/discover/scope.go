package discover

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ScopeInfo is the crawl scope derived from clustering sitemap URLs around
// the seed: the proposed allowed_path_prefix with its supporting evidence,
// a depth estimate, and sibling version directories observed on the site.
type ScopeInfo struct {
	Prefix          string
	PrefixCount     int
	TotalCount      int
	MaxDepth        int
	SiblingVersions []string
	SiblingLocales  []string
}

const (
	minClusterPages = 5
	defaultMaxDepth = 5
	depthCap        = 10
	corpusNoteAt    = 2000
	corpusWarnAt    = 10000
)

func analyzeScope(r *Report) {
	r.Scope = clusterScope(r.FinalURL, r.Sitemap.URLs)

	if !r.Sitemap.Found {
		r.Warnings = append(r.Warnings, "no sitemap found; crawl scope and size are estimated from the URL alone")
	}
	switch n := r.Scope.PrefixCount; {
	case n >= corpusWarnAt:
		r.Warnings = append(r.Warnings, fmt.Sprintf("sitemap lists %d pages under %s; strongly consider a narrower allowed_path_prefix or lower max_depth", n, r.Scope.Prefix))
	case n >= corpusNoteAt:
		r.Warnings = append(r.Warnings, fmt.Sprintf("sitemap lists %d pages under %s; the first crawl will take a while", n, r.Scope.Prefix))
	}
	if len(r.Scope.SiblingVersions) > 0 && !strings.Contains(r.Scope.Prefix, "/"+r.Version.Segment+"/") {
		r.Warnings = append(r.Warnings, fmt.Sprintf("other doc versions share this scope (%s); drafted disallowed_path_patterns exclude them", strings.Join(r.Scope.SiblingVersions, ", ")))
	}
	if r.Robots.AIRestricted {
		r.Warnings = append(r.Warnings, r.Robots.AINote+"; confirm you have permission to crawl this site")
	}
}

// clusterScope picks allowed_path_prefix by walking down the seed URL's path
// segments: locale and version segments are always kept inside the prefix
// (scoping to one language and one version), and the walk stops at the first
// ordinary segment whose subtree still holds a meaningful share of the
// sitemap. Sibling version/locale directories observed at each junction are
// collected for disallowed_path_patterns.
func clusterScope(seed *url.URL, sitemapURLs []string) ScopeInfo {
	paths := hostPaths(seed, sitemapURLs)
	scope := ScopeInfo{TotalCount: len(paths), MaxDepth: defaultMaxDepth}

	segs := pathSegments(seedDir(seed.Path))
	prefix := "/"
	for _, seg := range segs {
		candidate := prefix + seg + "/"
		locale := isLocaleSegment(seg)
		_, version := isVersionSegment(seg)
		if locale || version {
			siblings := siblingDirs(paths, prefix, seg)
			if version {
				scope.SiblingVersions = append(scope.SiblingVersions, prefixAll(prefix, siblings)...)
			} else {
				scope.SiblingLocales = append(scope.SiblingLocales, prefixAll(prefix, siblings)...)
			}
			prefix = candidate
			continue
		}
		if len(paths) == 0 {
			// No sitemap to verify against: keep the first ordinary segment
			// only, since deeper segments are usually the page itself.
			prefix = candidate
			break
		}
		if countUnder(paths, candidate) < minClusterPages {
			break
		}
		prefix = candidate
	}
	scope.Prefix = prefix
	scope.PrefixCount = countUnder(paths, prefix)
	if len(paths) == 0 {
		scope.PrefixCount = 0
	} else {
		scope.MaxDepth = depthUnder(paths, prefix)
	}
	return scope
}

func hostPaths(seed *url.URL, urls []string) []string {
	var out []string
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || !sameSite(u.Hostname(), seed.Hostname()) {
			continue
		}
		out = append(out, u.Path)
	}
	return out
}

func sameSite(a, b string) bool {
	return strings.TrimPrefix(a, "www.") == strings.TrimPrefix(b, "www.")
}

// seedDir is the seed's directory path: a trailing filename (segment with an
// extension or a known page) is dropped so /docs/intro.html scopes to /docs/.
func seedDir(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	if strings.HasSuffix(p, "/") {
		return p
	}
	segs := pathSegments(p)
	last := segs[len(segs)-1]
	if strings.Contains(last, ".") {
		segs = segs[:len(segs)-1]
	}
	if len(segs) == 0 {
		return "/"
	}
	return "/" + strings.Join(segs, "/") + "/"
}

func countUnder(paths []string, prefix string) int {
	n := 0
	for _, p := range paths {
		if strings.HasPrefix(p, prefix) || p+"/" == prefix {
			n++
		}
	}
	return n
}

func depthUnder(paths []string, prefix string) int {
	maxRel := 0
	for _, p := range paths {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		if rel := len(pathSegments(strings.TrimPrefix(p, prefix))); rel > maxRel {
			maxRel = rel
		}
	}
	depth := maxRel + 1
	if depth < 3 {
		depth = 3
	}
	if depth > depthCap {
		depth = depthCap
	}
	return depth
}

// siblingDirs lists directories under prefix at the same level as keep, e.g.
// the other version or locale trees next to the one the seed lives in.
func siblingDirs(paths []string, prefix, keep string) []string {
	_, keepIsVersion := isVersionSegment(keep)
	keepIsLocale := isLocaleSegment(keep)
	seen := map[string]struct{}{}
	for _, p := range paths {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		seg, _, found := strings.Cut(rest, "/")
		if !found || seg == "" || seg == keep {
			continue
		}
		_, segIsVersion := isVersionSegment(seg)
		if (keepIsVersion && segIsVersion) || (keepIsLocale && isLocaleSegment(seg)) {
			seen[seg] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func prefixAll(prefix string, segs []string) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		out = append(out, prefix+s+"/")
	}
	return out
}
