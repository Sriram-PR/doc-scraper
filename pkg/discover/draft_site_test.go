package discover

import (
	"context"
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
