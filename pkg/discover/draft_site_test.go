package discover

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

func TestDraftSite_OverridesBeatDiscoveredValues(t *testing.T) {
	srv, _ := siteServer(t, map[string]string{"/guide/intro": vpDoc}, []string{"/guide/intro"})
	res, err := testDiscoverer(srv).DraftSite(context.Background(), DraftRequest{
		URL:       srv.URL + "/guide/intro",
		SiteKey:   "vp",
		Overrides: map[string]any{"content_selector": "main.main", "max_depth": float64(2)},
	})
	require.NoError(t, err)
	assert.Equal(t, "vp", res.Draft.SiteKey)
	assert.Equal(t, "main.main", res.Draft.Site.ContentSelector)
	assert.Equal(t, 2, res.Draft.Site.MaxDepth)
	assert.Equal(t, "/guide/intro", mustURL(t, res.Preview.URL).Path)
	assert.Empty(t, res.Preview.Err)
}

func TestDraftSite_PreviewPagePrecedence(t *testing.T) {
	pages := map[string]string{"/": vpLanding, "/guide/intro": vpDoc, "/guide/other": vpDoc}
	srv, _ := siteServer(t, pages, []string{"/", "/guide/intro"})
	d := testDiscoverer(srv)

	sampled, err := d.DraftSite(context.Background(), DraftRequest{URL: srv.URL + "/"})
	require.NoError(t, err)
	assert.Equal(t, "/guide/intro", mustURL(t, sampled.Preview.URL).Path, "the sample page beats the seed")
	assert.True(t, sampled.Preview.Sampled)

	chosen, err := d.DraftSite(context.Background(), DraftRequest{URL: srv.URL + "/", PreviewURL: srv.URL + "/guide/other"})
	require.NoError(t, err)
	assert.Equal(t, "/guide/other", mustURL(t, chosen.Preview.URL).Path, "PreviewURL beats the sample page")
	assert.False(t, chosen.Preview.Sampled)
}

func TestDraftSite_BaseSendsNoDiscoveryRequests(t *testing.T) {
	srv, hits := siteServer(t, map[string]string{"/guide/intro": vpDoc}, []string{"/guide/intro"})
	base := &config.SiteConfig{
		StartURLs:       []string{srv.URL + "/guide/intro"},
		AllowedDomain:   mustURL(t, srv.URL).Hostname(),
		ContentSelector: ".vp-doc",
	}
	res, err := testDiscoverer(srv).DraftSite(context.Background(), DraftRequest{SiteKey: "vp", Base: base})
	require.NoError(t, err)
	assert.Equal(t, "vp", res.Draft.SiteKey)
	assert.Equal(t, ".vp-doc", res.Draft.Site.ContentSelector)
	assert.Positive(t, res.Preview.Chars)
	assert.Equal(t, 1, hits("/guide/intro"))
	for _, p := range []string{"/robots.txt", "/llms.txt", "/sitemap.xml"} {
		assert.Zero(t, hits(p), "no discovery request to %s", p)
	}
}

func TestDraftSite_ExtractionFailureStillReturnsDraft(t *testing.T) {
	srv, _ := siteServer(t, map[string]string{"/guide/intro": vpDoc}, nil)
	base := &config.SiteConfig{StartURLs: []string{srv.URL + "/guide/intro"}, AllowedDomain: "x", ContentSelector: ".vp-doc"}
	res, err := testDiscoverer(srv).DraftSite(context.Background(), DraftRequest{
		SiteKey: "vp", Base: base, Overrides: map[string]any{"content_selector": "#nope"},
	})
	require.NoError(t, err)
	assert.Equal(t, "#nope", res.Draft.Site.ContentSelector)
	assert.Contains(t, res.Preview.Err, "yielded no content")
}

func TestDraftSite_Errors(t *testing.T) {
	srv, _ := siteServer(t, map[string]string{"/guide/intro": vpDoc}, nil)
	d := testDiscoverer(srv)
	_, err := d.DraftSite(context.Background(), DraftRequest{})
	require.ErrorContains(t, err, "needs a URL or an existing site entry")

	_, err = d.DraftSite(context.Background(), DraftRequest{URL: srv.URL + "/guide/intro", Overrides: map[string]any{"bogus": 1.0}})
	require.ErrorContains(t, err, `"bogus"`)

	_, err = d.DraftSite(context.Background(), DraftRequest{URL: srv.URL + "/guide/intro", PreviewURL: "ftp://x/"})
	require.ErrorContains(t, err, "need an absolute http(s) URL")
}

func TestDraftSite_ClipsEchoedURL(t *testing.T) {
	srv, _ := siteServer(t, map[string]string{"/guide/intro": vpDoc}, nil)
	_, err := testDiscoverer(srv).DraftSite(context.Background(), DraftRequest{URL: "ftp://" + strings.Repeat("x", 10_000)})
	require.ErrorContains(t, err, "need an absolute http(s) URL")
	assert.Less(t, len(err.Error()), 1000)
}

var plainDoc = `<html><head><title>Guide</title></head><body><main><h1>Guide</h1><p>` +
	strings.Repeat("This guide explains how the demo tool is installed, configured and run. ", 10) +
	`</p><pre><code>demo --help</code></pre></main></body></html>`

func TestDraftSite_SelectorOverrideDropsReadabilityNotes(t *testing.T) {
	srv, _ := siteServer(t, map[string]string{"/guide/intro": plainDoc}, nil)
	d := testDiscoverer(srv)

	discovered, err := d.DraftSite(context.Background(), DraftRequest{URL: srv.URL + "/guide/intro"})
	require.NoError(t, err)
	require.Equal(t, "auto", discovered.Draft.Site.ContentSelector)
	require.Contains(t, strings.Join(discovered.Draft.Warnings, "\n"), "readability")

	res, err := d.DraftSite(context.Background(), DraftRequest{URL: srv.URL + "/guide/intro", Overrides: map[string]any{"content_selector": "main"}})
	require.NoError(t, err)
	assert.NotContains(t, strings.Join(res.Draft.Warnings, "\n"), "readability")
	assert.NotContains(t, strings.Join(res.Draft.Evidence, "\n"), "readability")
	assert.Contains(t, res.Draft.Evidence, "content_selector: set by override")
}

func TestDraftSite_AutoOverrideAddsReadabilityWarning(t *testing.T) {
	srv, _ := siteServer(t, map[string]string{"/guide/intro": vpDoc}, []string{"/guide/intro"})
	res, err := testDiscoverer(srv).DraftSite(context.Background(), DraftRequest{URL: srv.URL + "/guide/intro", Overrides: map[string]any{"content_selector": "auto"}})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(res.Draft.Warnings, "\n"), "readability")
	assert.Contains(t, res.Draft.Evidence, "content_selector: set by override")
}

func TestDraftSite_OverrideReplacesThatFieldsEvidence(t *testing.T) {
	srv, _ := siteServer(t, map[string]string{"/guide/intro": vpDoc, "/guide/a/b": vpDoc}, []string{"/guide/intro", "/guide/a/b"})
	res, err := testDiscoverer(srv).DraftSite(context.Background(), DraftRequest{URL: srv.URL + "/guide/intro", Overrides: map[string]any{"max_depth": float64(1)}})
	require.NoError(t, err)
	joined := strings.Join(res.Draft.Evidence, "\n")
	assert.NotContains(t, joined, "from sitemap path depth")
	assert.Contains(t, res.Draft.Evidence, "max_depth: set by override")
	assert.Contains(t, joined, "content_selector: vitepress detected", "other fields keep their evidence")
}
