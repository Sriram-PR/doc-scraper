package discover

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/detect"
)

var fixturePage = `<!DOCTYPE html>
<html><head>
<title>Getting Started - Demo Docs</title>
<meta name="generator" content="mkdocs-1.6.1, mkdocs-material-9.7.0">
<link rel="canonical" href="%s/docs/en/stable/getting-started/">
</head><body>
<nav class="md-nav" data-md-component="navigation">sidebar</nav>
<div class="md-content" data-md-component="content">
<article class="md-content__inner">
<h1>Getting Started</h1>
<p>` + strings.Repeat("This is the real documentation content of the page, long enough to validate the selector against the fetched page. ", 4) + `</p>
<pre><code>pip install demo</code></pre>
</article>
</div>
</body></html>`

func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server

	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "User-agent: *\nDisallow: /internal/\n\nUser-agent: GPTBot\nDisallow: /\n\nSitemap: %s/sitemap.xml\n", srv.URL)
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
		for i := range 30 {
			fmt.Fprintf(w, "<url><loc>%s/docs/en/stable/page%d/</loc></url>", srv.URL, i)
		}
		for i := range 20 {
			fmt.Fprintf(w, "<url><loc>%s/docs/en/1.0/page%d/</loc></url>", srv.URL, i)
		}
		for i := range 10 {
			fmt.Fprintf(w, "<url><loc>%s/blog/post%d/</loc></url>", srv.URL, i)
		}
		fmt.Fprint(w, `</urlset>`)
	})
	mux.HandleFunc("/llms.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "# Demo Docs\n\n> Docs for demo.\n\n## Pages\n\n")
		for i := range 35 {
			fmt.Fprintf(w, "- [Page %d](%s/docs/en/stable/page%d/): a page\n", i, srv.URL, i)
		}
	})
	mux.HandleFunc("/docs/en/stable/getting-started/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, fixturePage, srv.URL)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testDiscoverer(srv *httptest.Server) *Discoverer {
	return &Discoverer{
		Client:    srv.Client(),
		UserAgent: "doc-scraper-test/1.0",
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestDiscoverer_Run(t *testing.T) {
	srv := fixtureServer(t)
	d := testDiscoverer(srv)

	r, err := d.Run(context.Background(), srv.URL+"/docs/en/stable/getting-started/")
	require.NoError(t, err)

	assert.Equal(t, detect.FrameworkMkDocsMaterial, r.Detection.Framework)
	assert.Equal(t, detect.ConfidenceHigh, r.Detection.Confidence)
	assert.True(t, r.Sitemap.Found)
	assert.Len(t, r.Sitemap.URLs, 60)
	assert.Equal(t, "/docs/en/stable/", r.Scope.Prefix)
	assert.Equal(t, 30, r.Scope.PrefixCount)
	assert.Contains(t, r.Scope.SiblingVersions, "/docs/en/1.0/")
	assert.True(t, r.LlmsTxt.Found)
	assert.Len(t, r.LlmsTxt.Links, 35)
	assert.Equal(t, "stable", r.Version.Segment)
	assert.True(t, r.Version.IsAlias)
	assert.Equal(t, "en", r.Locale.PathSegment)
	assert.Contains(t, r.Robots.Sitemaps, srv.URL+"/sitemap.xml")
	assert.Contains(t, r.Robots.Disallows, "/internal/")
	assert.True(t, r.Robots.AIRestricted)
}

func TestDiscoverer_RobotsBlocksSeed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /docs/\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, err := testDiscoverer(srv).Run(context.Background(), srv.URL+"/docs/page/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "robots.txt")
}

func TestDiscoverer_BotBlockIsDiagnosed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, err := testDiscoverer(srv).Run(context.Background(), srv.URL+"/docs/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bot protection")
}

func TestDiscoverer_GzipSitemap(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0"?><urlset><url><loc>https://example.com/docs/a/</loc></url></urlset>`)
		w.Header().Set("Content-Type", "application/gzip")
		gz := newGzip(&sb)
		_, _ = w.Write(gz)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	info := testDiscoverer(srv).fetchSitemap(context.Background(), mustURL(t, srv.URL+"/docs/"), nil)
	assert.True(t, info.Found)
	assert.Len(t, info.URLs, 1)
}

func TestDiscoverer_SitemapIndex(t *testing.T) {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0"?><sitemapindex><sitemap><loc>%s/s1.xml</loc></sitemap><sitemap><loc>%s/s2.xml</loc></sitemap><sitemap><loc>%s/s3.xml</loc></sitemap></sitemapindex>`,
			srv.URL, srv.URL, srv.URL)
	})
	shard := func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><urlset><url><loc>https://example.com/docs/x/</loc></url></urlset>`)
	}
	mux.HandleFunc("/s1.xml", shard)
	mux.HandleFunc("/s2.xml", shard)
	mux.HandleFunc("/s3.xml", shard)
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	info := testDiscoverer(srv).fetchSitemap(context.Background(), mustURL(t, srv.URL+"/docs/"), nil)
	assert.True(t, info.Found)
	assert.Equal(t, 3, info.ShardCount)
	assert.Len(t, info.URLs, 2, "only the first two shards are fetched")
	assert.True(t, info.Truncated)
}

func newGzip(sb *strings.Builder) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(sb.String()))
	_ = gz.Close()
	return buf.Bytes()
}
