package discover

import (
	"net/url"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/detect"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	require.NoError(t, err)
	return u
}

func repeatPaths(prefix string, n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, prefix+"page"+string(rune('a'+i%26))+string(rune('a'+i/26))+"/")
	}
	return out
}

func withHost(host string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, "https://"+host+p)
	}
	return out
}

func TestClusterScope_VersionedLocaleTree(t *testing.T) {
	// RTD layout: /en/latest/ is ours, /en/stable/ and /en/1.0/ are siblings.
	paths := append(repeatPaths("/en/latest/", 40), repeatPaths("/en/stable/", 40)...)
	paths = append(paths, repeatPaths("/en/1.0/", 20)...)
	paths = append(paths, repeatPaths("/ja/latest/", 10)...)
	urls := withHost("proj.readthedocs.io", paths)

	scope := clusterScope(mustURL(t, "https://proj.readthedocs.io/en/latest/intro/"), urls)
	assert.Equal(t, "/en/latest/", scope.Prefix)
	assert.Equal(t, 40, scope.PrefixCount)
	assert.Contains(t, scope.SiblingVersions, "/en/stable/")
	assert.Contains(t, scope.SiblingVersions, "/en/1.0/")
	assert.Contains(t, scope.SiblingLocales, "/ja/")
}

func TestClusterScope_MultiProductDomain(t *testing.T) {
	// Cloudflare-style: many products share the domain; scope to the seed's.
	paths := append(repeatPaths("/workers/", 60), repeatPaths("/pages/", 80)...)
	paths = append(paths, repeatPaths("/dns/", 90)...)
	urls := withHost("developers.example.com", paths)

	scope := clusterScope(mustURL(t, "https://developers.example.com/workers/runtime-apis/fetch/"), urls)
	assert.Equal(t, "/workers/", scope.Prefix)
	assert.Equal(t, 60, scope.PrefixCount)
	assert.Equal(t, 230, scope.TotalCount)
}

func TestClusterScope_DocsSubtree(t *testing.T) {
	paths := append(repeatPaths("/docs/concepts/", 50), repeatPaths("/docs/tasks/", 50)...)
	paths = append(paths, repeatPaths("/blog/", 30)...)
	urls := withHost("kubernetes.example.io", paths)

	scope := clusterScope(mustURL(t, "https://kubernetes.example.io/docs/concepts/overview/"), urls)
	assert.Equal(t, "/docs/concepts/", scope.Prefix, "seed dir keeps enough pages, so scope stays tight to it")
}

func TestClusterScope_NoSitemap(t *testing.T) {
	scope := clusterScope(mustURL(t, "https://docs.example.com/guide/intro.html"), nil)
	assert.Equal(t, "/guide/", scope.Prefix)
	assert.Equal(t, 0, scope.PrefixCount)
	assert.Equal(t, defaultMaxDepth, scope.MaxDepth)
}

func TestClusterScope_RootSeed(t *testing.T) {
	urls := withHost("docs.example.com", repeatPaths("/", 30))
	scope := clusterScope(mustURL(t, "https://docs.example.com/"), urls)
	assert.Equal(t, "/", scope.Prefix)
	assert.Equal(t, 30, scope.PrefixCount)
}

func TestClusterScope_NestedVersionTreesMirroringTheDocs(t *testing.T) {
	// Docusaurus layout: current docs at /docs/, older and unreleased versions
	// nested inside it as mirrors of the same pages.
	paths := repeatPaths("/docs/", 30)
	paths = append(paths, repeatPaths("/docs/api/", 10)...)
	paths = append(paths, repeatPaths("/docs/next/", 30)...)
	paths = append(paths, repeatPaths("/docs/2.x/", 25)...)
	paths = append(paths, repeatPaths("/docs/3.9.2/", 30)...)
	paths = append(paths, "/docs/3.9.2/a/b/c/d/")
	urls := withHost("docusaurus.example.io", paths)

	scope := clusterScope(mustURL(t, "https://docusaurus.example.io/docs/pageaa/"), urls)
	assert.Equal(t, "/docs/", scope.Prefix)
	assert.ElementsMatch(t, []string{"/docs/next/", "/docs/2.x/", "/docs/3.9.2/"}, scope.SiblingVersions)
	assert.Equal(t, 40, scope.PrefixCount, "excluded version trees do not count toward the corpus")
	assert.Equal(t, 86, scope.ExcludedCount)
	assert.Equal(t, 3, scope.MaxDepth, "depth is measured on the current docs only")
}

func TestClusterScope_NestedLocaleTreesAtRoot(t *testing.T) {
	// Starlight layout: English at the root, translations under locale dirs.
	paths := append(repeatPaths("/", 20), repeatPaths("/guides/", 10)...)
	paths = append(paths, repeatPaths("/de/", 20)...)
	paths = append(paths, repeatPaths("/de/guides/", 10)...)
	paths = append(paths, repeatPaths("/pt-br/", 20)...)
	urls := withHost("starlight.example.dev", paths)

	scope := clusterScope(mustURL(t, "https://starlight.example.dev/"), urls)
	assert.Equal(t, "/", scope.Prefix)
	assert.ElementsMatch(t, []string{"/de/", "/pt-br/"}, scope.SiblingLocales)
	assert.Equal(t, 30, scope.PrefixCount)
}

func TestClusterScope_ShapedDirsWithTheirOwnPagesAreKept(t *testing.T) {
	// /docs/v1/ and /docs/it/ look like a version and a locale but hold pages
	// found nowhere else, so they are sections, not copies.
	paths := repeatPaths("/docs/", 30)
	paths = append(paths, "/docs/v1/users/", "/docs/v1/orders/", "/docs/v1/auth/", "/docs/it/ops/", "/docs/it/oncall/")
	urls := withHost("api.example.com", paths)

	scope := clusterScope(mustURL(t, "https://api.example.com/docs/pageaa/"), urls)
	assert.Empty(t, scope.SiblingVersions)
	assert.Empty(t, scope.SiblingLocales)
	assert.Equal(t, 35, scope.PrefixCount)
}

func TestDisallowPatterns_ExcludeNestedTrees(t *testing.T) {
	r := &Report{Scope: ScopeInfo{Prefix: "/docs/", SiblingVersions: []string{"/docs/next/", "/docs/2.x/"}, SiblingLocales: []string{"/docs/"}}}
	got, evidence := disallowPatterns(r)
	assert.Equal(t, []string{`^/docs/next/`, `^/docs/2\.x/`}, got, "never excludes the prefix itself")
	assert.Empty(t, evidence)
}

const (
	docsVersionPattern = `^/docs/v?\d+(?:\.\d+)*(?:\.x)?/`
	rootLocalePattern  = `^/(?:ar|bn|cs|da|de|el|en|es|fa|fi|fr|he|hi|hu|id|it|ja|ko|nl|no|pl|pt|ro|ru|sk|sv|th|tr|uk|vi|zh)(?:[-_][a-zA-Z]{2,4})?/`
)

func TestDisallowPatterns_Generalize(t *testing.T) {
	cases := []struct {
		name         string
		prefix       string
		versions     []string
		locales      []string
		sitemap      []string
		wantPatterns []string
		wantEvidence []string
	}{
		{
			name:         "version trees generalize, alias stays literal",
			prefix:       "/docs/",
			versions:     []string{"/docs/next/", "/docs/2.x/", "/docs/3.0.1/", "/docs/3.9.2/"},
			sitemap:      []string{"/docs/intro", "/docs/2.x/intro", "/docs/3.0.1/intro"},
			wantPatterns: []string{`^/docs/next/`, docsVersionPattern},
			wantEvidence: []string{"disallowed_path_patterns: 3 version trees under /docs/ generalized; the pattern matches none of the 1 kept sitemap URLs"},
		},
		{
			name:         "locale trees generalize",
			prefix:       "/",
			locales:      []string{"/es/", "/ja/", "/pt-br/"},
			sitemap:      []string{"/guide/a", "/reference/b", "/es/guide/a"},
			wantPatterns: []string{rootLocalePattern},
			wantEvidence: []string{"disallowed_path_patterns: 3 locale trees under / generalized; the pattern matches none of the 2 kept sitemap URLs"},
		},
		{
			name:         "a kept page the pattern matches keeps literals",
			prefix:       "/docs/",
			versions:     []string{"/docs/1.0/", "/docs/1.1/"},
			sitemap:      []string{"/docs/intro", "/docs/0.11/a"},
			wantPatterns: []string{`^/docs/1\.0/`, `^/docs/1\.1/`},
		},
		{
			name:         "a prefix the pattern matches keeps literals",
			prefix:       "/en/5.1/",
			versions:     []string{"/en/4.2/", "/en/5.0/"},
			wantPatterns: []string{`^/en/4\.2/`, `^/en/5\.0/`},
		},
		{
			name:         "a single tree stays literal",
			prefix:       "/docs/",
			versions:     []string{"/docs/15/"},
			sitemap:      []string{"/docs/intro"},
			wantPatterns: []string{`^/docs/15/`},
		},
		{
			name:         "trees the pattern does not match stay literal",
			prefix:       "/docs/",
			versions:     []string{"/docs/V1/", "/docs/V2/"},
			sitemap:      []string{"/docs/intro"},
			wantPatterns: []string{`^/docs/V1/`, `^/docs/V2/`},
		},
		{
			name:     "each kind and parent is checked on its own",
			prefix:   "/en/stable/",
			versions: []string{"/en/1.0/", "/en/2.0/"},
			locales:  []string{"/de/", "/fr/"},
			sitemap:  []string{"/en/stable/a"},
			// /en/stable/a is locale-shaped under /, so only the version group generalizes.
			wantPatterns: []string{`^/en/v?\d+(?:\.\d+)*(?:\.x)?/`, `^/de/`, `^/fr/`},
			wantEvidence: []string{"disallowed_path_patterns: 2 version trees under /en/ generalized; the pattern matches none of the 1 kept sitemap URLs"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Report{
				FinalURL: mustURL(t, "https://d.io"+tc.prefix),
				Sitemap:  SitemapInfo{Found: len(tc.sitemap) > 0, URLs: withHost("d.io", tc.sitemap)},
				Scope:    ScopeInfo{Prefix: tc.prefix, SiblingVersions: tc.versions, SiblingLocales: tc.locales},
			}
			patterns, evidence := disallowPatterns(r)
			assert.Equal(t, tc.wantPatterns, patterns)
			assert.Equal(t, tc.wantEvidence, evidence)
		})
	}
}

func TestBuildDraft_GeneralizedPatternEvidenceFollowsSiblingLine(t *testing.T) {
	r := &Report{
		FinalURL: mustURL(t, "https://d.io/"),
		Sitemap:  SitemapInfo{Found: true, URLs: withHost("d.io", []string{"/guide/a", "/es/guide/a", "/ja/guide/a"})},
		Scope:    ScopeInfo{Prefix: "/", SiblingLocales: []string{"/es/", "/ja/"}},
	}
	d := BuildDraft(r, "")
	assert.Equal(t, []string{rootLocalePattern}, d.Site.DisallowedPathPatterns)
	i := slices.Index(d.Evidence, "disallowed_path_patterns: sibling version/locale trees observed in the sitemap")
	require.GreaterOrEqual(t, i, 0)
	require.Greater(t, len(d.Evidence), i+1)
	assert.Equal(t, "disallowed_path_patterns: 2 locale trees under / generalized; the pattern matches none of the 1 kept sitemap URLs", d.Evidence[i+1])
}

func TestSeedDir(t *testing.T) {
	assert.Equal(t, "/docs/", seedDir("/docs/intro.html"))
	assert.Equal(t, "/docs/intro/", seedDir("/docs/intro/"))
	assert.Equal(t, "/docs/intro/", seedDir("/docs/intro"), "extensionless last segment is kept as a directory")
	assert.Equal(t, "/", seedDir("/"))
	assert.Equal(t, "/", seedDir("/index.html"))
}

func TestVersionAndLocaleSegments(t *testing.T) {
	for _, seg := range []string{"latest", "stable", "v2", "3.14", "1.0.x", "v1.2.3"} {
		_, ok := isVersionSegment(seg)
		assert.True(t, ok, seg)
	}
	for _, seg := range []string{"guide", "api", "v", "docs"} {
		_, ok := isVersionSegment(seg)
		assert.False(t, ok, seg)
	}
	assert.True(t, isLocaleSegment("en"))
	assert.True(t, isLocaleSegment("zh-cn"))
	assert.True(t, isLocaleSegment("pt_BR"))
	assert.False(t, isLocaleSegment("docs"))
	assert.False(t, isLocaleSegment("v2"))
	assert.False(t, isLocaleSegment("go"), "not an ISO 639-1 language")
}

func TestBuildDraft_EvidenceCountsExcludedTrees(t *testing.T) {
	r := &Report{
		FinalURL: mustURL(t, "https://docusaurus.example.io/docs/intro"),
		Sitemap:  SitemapInfo{Found: true},
		Scope:    ScopeInfo{Prefix: "/docs/", PrefixCount: 40, TotalCount: 200, ExcludedCount: 86, MaxDepth: 3, SiblingVersions: []string{"/docs/next/"}},
	}
	d := BuildDraft(r, "")
	assert.Contains(t, d.Evidence, "allowed_path_prefix: /docs/ covers 40 of 200 sitemap URLs, not counting 86 in excluded version/locale trees")
	assert.Equal(t, []string{"^/docs/next/"}, d.Site.DisallowedPathPatterns)
}

func TestBuildDraft_EvidenceNamesSamplePage(t *testing.T) {
	r := &Report{
		FinalURL:  mustURL(t, "https://vitepress.example/"),
		Detection: detect.DetectionResult{Framework: detect.FrameworkVitePress, Selector: ".vp-doc", Confidence: detect.ConfidenceHigh, Source: detect.SourceGenerator},
		Sample:    &SamplePage{URL: mustURL(t, "https://vitepress.example/guide/intro")},
		Scope:     ScopeInfo{Prefix: "/"},
	}
	d := BuildDraft(r, "")
	assert.Equal(t, ".vp-doc", d.Site.ContentSelector)
	assert.Contains(t, d.Evidence, "content_selector: vitepress detected via generator (high confidence), validated on /guide/intro; the seed page is a landing layout")
}

func TestBuildDraft_SeedValidatedEvidenceUnchanged(t *testing.T) {
	r := &Report{
		FinalURL:  mustURL(t, "https://vitepress.example/guide/intro"),
		Detection: detect.DetectionResult{Framework: detect.FrameworkVitePress, Selector: ".vp-doc", Confidence: detect.ConfidenceHigh, Source: detect.SourceGenerator},
		Scope:     ScopeInfo{Prefix: "/guide/"},
	}
	d := BuildDraft(r, "")
	assert.Contains(t, d.Evidence, "content_selector: vitepress detected via generator (high confidence), validated on the fetched page")
}

func TestBuildDraft_AutoEvidenceCarriesSampleNote(t *testing.T) {
	r := &Report{
		FinalURL:   mustURL(t, "https://vitepress.example/"),
		Detection:  detect.DetectionResult{Framework: detect.FrameworkVitePress, Fallback: true, Confidence: detect.ConfidenceUnvalidated, Source: detect.SourceGenerator},
		SampleNote: "checked /guide/intro: detected mkdocs there",
		Scope:      ScopeInfo{Prefix: "/"},
	}
	d := BuildDraft(r, "")
	assert.Equal(t, "auto", d.Site.ContentSelector)
	assert.Contains(t, d.Evidence, `content_selector: vitepress recognized but no selector matched this page; "auto" will use readability extraction; checked /guide/intro: detected mkdocs there`)
}
