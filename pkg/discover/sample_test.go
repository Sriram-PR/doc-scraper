package discover

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleReport(t *testing.T, sitemap, disallows []string) *Report {
	t.Helper()
	return &Report{
		FinalURL: mustURL(t, "https://d.io/docs/intro"),
		Sitemap:  SitemapInfo{Found: len(sitemap) > 0, URLs: sitemap},
		Robots:   RobotsInfo{Disallows: disallows},
		Scope: ScopeInfo{
			Prefix:          "/docs/",
			SiblingVersions: []string{"/docs/1.0/"},
			SiblingLocales:  []string{"/docs/fr/"},
		},
	}
}

func TestShallowestSitemapPage(t *testing.T) {
	cases := []struct {
		name      string
		sitemap   []string
		disallows []string
		want      string
	}{
		{"shallowest wins", []string{"https://d.io/docs/a/b/c", "https://d.io/docs/z", "https://d.io/docs/a/b"}, nil, "/docs/z"},
		{"alphabetical tie-break", []string{"https://d.io/docs/b", "https://d.io/docs/a"}, nil, "/docs/a"},
		{"seed skipped with and without slash", []string{"https://d.io/docs/intro/", "https://d.io/docs/intro", "https://d.io/docs/x/y"}, nil, "/docs/x/y"},
		{"outside prefix skipped", []string{"https://d.io/blog/a", "https://d.io/docs/x/y"}, nil, "/docs/x/y"},
		{"version and locale trees skipped", []string{"https://d.io/docs/1.0/a", "https://d.io/docs/fr/a", "https://d.io/docs/x/y"}, nil, "/docs/x/y"},
		{"robots prefix rule", []string{"https://d.io/docs/a", "https://d.io/docs/b"}, []string{"/docs/a"}, "/docs/b"},
		{"robots wildcard rule", []string{"https://d.io/docs/a.html", "https://d.io/docs/b/c"}, []string{"/*.html$"}, "/docs/b/c"},
		{"non-HTML extension skipped", []string{"https://d.io/docs/guide.pdf", "https://d.io/docs/x/y"}, nil, "/docs/x/y"},
		{"other host skipped", []string{"https://other.io/docs/a", "https://d.io/docs/x/y"}, nil, "/docs/x/y"},
		{"www host counts as same site", []string{"https://www.d.io/docs/a"}, nil, "/docs/a"},
		{"nothing qualifies", []string{"https://d.io/blog/a"}, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shallowestSitemapPage(sampleReport(t, tc.sitemap, tc.disallows))
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.want, got.Path)
		})
	}
}

func TestFirstSeedLink(t *testing.T) {
	r := sampleReport(t, nil, nil)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<html><head>
<link href="/docs/head-link"></head><body>
<a href="#top">top</a>
<a href="mailto:a@d.io">mail</a>
<a href="/blog/x">blog</a>
<a href="https://other.io/docs/y">other</a>
<a href="/docs/intro">self</a>
<a href="guide/start#install">start</a>
<a href="/docs/later">later</a>
</body></html>`))
	require.NoError(t, err)
	r.Doc = doc

	got := firstSeedLink(r)
	require.NotNil(t, got)
	assert.Equal(t, "https://d.io/docs/guide/start", got.String())
}

func TestFirstSeedLink_NoneInScope(t *testing.T) {
	r := sampleReport(t, nil, nil)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<html><body><a href="/blog/x">blog</a></body></html>`))
	require.NoError(t, err)
	r.Doc = doc
	assert.Nil(t, firstSeedLink(r))
}

func TestSampleCandidates(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		sitemap []string
		want    []string
	}{
		{"seed link first, then sitemap", `<a href="/docs/guide/start">start</a>`, []string{"https://d.io/docs/a/b", "https://d.io/docs/z"}, []string{"/docs/guide/start", "/docs/z"}},
		{"same page listed once", `<a href="/docs/z/">z</a>`, []string{"https://d.io/docs/z"}, []string{"/docs/z/"}},
		{"sitemap only", `<a href="/blog/x">blog</a>`, []string{"https://d.io/docs/z"}, []string{"/docs/z"}},
		{"seed link only", `<a href="/docs/guide/start">start</a>`, nil, []string{"/docs/guide/start"}},
		{"neither", `<a href="/blog/x">blog</a>`, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := sampleReport(t, tc.sitemap, nil)
			doc, err := goquery.NewDocumentFromReader(strings.NewReader("<html><body>" + tc.body + "</body></html>"))
			require.NoError(t, err)
			r.Doc = doc
			var got []string
			for _, u := range sampleCandidates(r) {
				got = append(got, u.Path)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
