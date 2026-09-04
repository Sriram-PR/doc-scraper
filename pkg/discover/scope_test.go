package discover

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
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
