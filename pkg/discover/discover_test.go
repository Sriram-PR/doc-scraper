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
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/detect"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/pagebudget"
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

func urlsetHandler(locs ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><urlset>`)
		for _, l := range locs {
			fmt.Fprintf(w, "<url><loc>%s</loc></url>", l)
		}
		fmt.Fprint(w, `</urlset>`)
	}
}

func TestDiscoverer_SitemapUnderProjectPath(t *testing.T) {
	cases := []struct {
		name, route, seedPath string
	}{
		{"mkdocs on project pages", "/proj/sitemap.xml", "/proj/guide/intro/"},
		{"astro index on project pages", "/proj/sitemap-index.xml", "/proj/guide/"},
		{"seed is a file", "/book/sitemap.xml", "/book/index.html"},
		{"second level", "/group/proj/sitemap.xml", "/group/proj/docs/page/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc(tc.route, urlsetHandler("https://example.com/proj/a/"))
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			info := testDiscoverer(srv).fetchSitemap(context.Background(), mustURL(t, srv.URL+tc.seedPath), nil)
			assert.True(t, info.Found)
			assert.Equal(t, srv.URL+tc.route, info.SitemapURL)
		})
	}
}

func TestDiscoverer_RootSitemapWinsOverProjectPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", urlsetHandler("https://example.com/root/"))
	mux.HandleFunc("/proj/sitemap.xml", urlsetHandler("https://example.com/proj/"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	info := testDiscoverer(srv).fetchSitemap(context.Background(), mustURL(t, srv.URL+"/proj/guide/"), nil)
	assert.Equal(t, srv.URL+"/sitemap.xml", info.SitemapURL)
}

func TestDiscoverer_SitemapProbeIsBounded(t *testing.T) {
	var requests []string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	info := testDiscoverer(srv).fetchSitemap(context.Background(), mustURL(t, srv.URL+"/a/b/c/d/e/"), nil)
	assert.False(t, info.Found)
	assert.Len(t, requests, 9, "root plus two ancestor directories, three names each: %v", requests)
	assert.NotContains(t, requests, "/a/b/c/sitemap.xml")
}

func newGzip(sb *strings.Builder) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(sb.String()))
	_ = gz.Close()
	return buf.Bytes()
}

func fileServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for route, file := range routes {
		b, err := os.ReadFile(file)
		require.NoError(t, err)
		mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(b) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverer_StaticStubIsNotReportedAsJSRendered(t *testing.T) {
	srv := fileServer(t, map[string]string{"/stub/": "../detect/testdata/example.com.html"})
	r, err := testDiscoverer(srv).Run(context.Background(), srv.URL+"/stub/")
	require.NoError(t, err)

	assert.Equal(t, detect.FrameworkUnknown, r.Detection.Framework)
	joined := strings.Join(r.Warnings, "\n")
	assert.NotContains(t, joined, "JavaScript-rendered")
	assert.Contains(t, joined, "very little content")
}

func TestDiscoverer_EmptyContainerIsNotReportedAsValidated(t *testing.T) {
	srv := fileServer(t, map[string]string{"/mkdocs-material/": "../detect/testdata/mkdocs-material-landing.html"})
	r, err := testDiscoverer(srv).Run(context.Background(), srv.URL+"/mkdocs-material/")
	require.NoError(t, err)

	draft := BuildDraft(r, "")
	assert.Equal(t, "auto", draft.Site.ContentSelector)
	assert.NotContains(t, strings.Join(draft.Evidence, "\n"), "validated on the fetched page")
	assert.Contains(t, strings.Join(draft.Warnings, "\n"), "readability")
}

const vpLanding = `<!DOCTYPE html><html><head><title>Demo</title>
<meta name="generator" content="VitePress v1.6.4"></head><body>
<div id="VPContent"><div class="VPHome"><h1>Demo</h1><p>Fast docs.</p>
<div class="vp-doc"><p>Short intro.</p></div></div></div></body></html>`

var vpDoc = `<!DOCTYPE html><html><head><title>Intro</title>
<meta name="generator" content="VitePress v1.6.4"></head><body>
<div id="VPContent"><main class="main"><div class="vp-doc"><h1>Intro</h1>
<p>` + strings.Repeat("Real documentation body text with plenty of length for validation. ", 6) + `</p>
</div></main></div></body></html>`

// siteServer serves pages by exact path, a sitemap of the given paths, and
// counts hits per path. A page value "redirect-off-host" redirects to the
// same server under the hostname localhost, which is a different site, and
// "redirect:<path>" redirects to that path on the same server.
func siteServer(t *testing.T, pages map[string]string, sitemap []string) (*httptest.Server, func(string) int) {
	t.Helper()
	var mu sync.Mutex
	hits := map[string]int{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/sitemap.xml" {
			fmt.Fprint(w, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
			for _, p := range sitemap {
				fmt.Fprintf(w, "<url><loc>%s%s</loc></url>", srv.URL, p)
			}
			fmt.Fprint(w, `</urlset>`)
			return
		}
		page, ok := pages[r.URL.Path]
		switch {
		case !ok:
			http.NotFound(w, r)
		case page == "redirect-off-host":
			http.Redirect(w, r, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)+"/elsewhere", http.StatusFound)
		case strings.HasPrefix(page, "redirect:"):
			http.Redirect(w, r, strings.TrimPrefix(page, "redirect:"), http.StatusFound)
		default:
			fmt.Fprint(w, page)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func(p string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[p]
	}
}

func TestDiscoverer_LandingSeedAdoptsSelectorFromSample(t *testing.T) {
	srv, _ := siteServer(t, map[string]string{"/": vpLanding, "/guide/intro": vpDoc}, []string{"/", "/guide/intro"})
	r, err := testDiscoverer(srv).Run(context.Background(), srv.URL+"/")
	require.NoError(t, err)

	assert.Equal(t, detect.FrameworkVitePress, r.Detection.Framework)
	assert.Equal(t, ".vp-doc, main.main, #VPContent", r.Detection.Selector)
	assert.Equal(t, detect.ConfidenceHigh, r.Detection.Confidence)
	require.NotNil(t, r.Sample)
	assert.Equal(t, "/guide/intro", r.Sample.URL.Path)
	assert.NotNil(t, r.Sample.Doc)
	assert.Empty(t, r.SampleNote)
	assert.NotContains(t, strings.Join(r.Warnings, "\n"), "very little content")
}

func TestDiscoverer_LandingSeedKeepsFallbackWhenSampleFails(t *testing.T) {
	cases := []struct {
		name     string
		pages    map[string]string
		sitemap  []string
		wantNote string
	}{
		{"sample 404", map[string]string{"/": vpLanding}, []string{"/", "/guide/intro"}, "checked /guide/intro: "},
		{"other framework", map[string]string{"/": vpLanding, "/guide/intro": fmt.Sprintf(fixturePage, "https://x.example")}, []string{"/", "/guide/intro"},
			"checked /guide/intro: detected mkdocs-material there"},
		{"still unvalidated", map[string]string{"/": vpLanding, "/guide/intro": vpLanding}, []string{"/", "/guide/intro"},
			"checked /guide/intro: the vitepress selector matched nothing there either"},
		{"redirect off host", map[string]string{"/": vpLanding, "/guide/intro": "redirect-off-host", "/elsewhere": vpDoc}, []string{"/", "/guide/intro"},
			"checked /guide/intro: redirected to localhost"},
		{"no candidate", map[string]string{"/": vpLanding}, []string{"/"}, "no other page in scope to check the selector on"},
		{"redirect out of scope", map[string]string{"/docs/": vpLanding, "/docs/intro": "redirect:/blog/post", "/blog/post": vpDoc}, []string{"/docs/", "/docs/intro", "/docs/x/1", "/docs/x/2", "/docs/x/3"},
			"checked /docs/intro: redirected to /blog/post"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := siteServer(t, tc.pages, tc.sitemap)
			seed := "/"
			if _, ok := tc.pages["/docs/"]; ok {
				seed = "/docs/"
			}
			r, err := testDiscoverer(srv).Run(context.Background(), srv.URL+seed)
			require.NoError(t, err)

			assert.Nil(t, r.Sample)
			assert.True(t, r.Detection.Fallback)
			assert.Empty(t, r.Detection.Selector)
			assert.Contains(t, r.SampleNote, tc.wantNote)
		})
	}
}

func TestDiscoverer_ValidatedSeedFetchesNoSample(t *testing.T) {
	srv, hits := siteServer(t, map[string]string{"/guide/intro": vpDoc, "/guide/other": vpDoc}, []string{"/guide/intro", "/guide/other"})
	r, err := testDiscoverer(srv).Run(context.Background(), srv.URL+"/guide/intro")
	require.NoError(t, err)

	assert.False(t, r.Detection.Fallback)
	assert.Nil(t, r.Sample)
	assert.Empty(t, r.SampleNote)
	assert.Equal(t, 0, hits("/guide/other"))
}

var vpLandingLinks = strings.Replace(vpLanding, "<p>Fast docs.</p>",
	`<p>Fast docs.</p><a href="/guide/deep/start">Get started</a><a href="/guide/other">Other</a>`, 1)

func TestDiscoverer_LandingSeedTriesSitemapAfterSeedLink(t *testing.T) {
	srv, hits := siteServer(t, map[string]string{"/": vpLandingLinks, "/intro": vpDoc, "/guide/other": vpDoc},
		[]string{"/", "/guide/deep/start", "/intro"})
	r, err := testDiscoverer(srv).Run(context.Background(), srv.URL+"/")
	require.NoError(t, err)

	require.NotNil(t, r.Sample)
	assert.Equal(t, "/intro", r.Sample.URL.Path)
	assert.Equal(t, ".vp-doc, main.main, #VPContent", r.Detection.Selector)
	assert.Empty(t, r.SampleNote)
	assert.Equal(t, 1, hits("/guide/deep/start"), "the seed's first link is tried first")
	assert.Equal(t, 0, hits("/guide/other"), "only the first seed link is a candidate")
}

func TestDiscoverer_LandingSeedNotesEveryFailedCandidate(t *testing.T) {
	srv, _ := siteServer(t, map[string]string{"/": vpLandingLinks, "/intro": vpLanding},
		[]string{"/", "/guide/deep/start", "/intro"})
	r, err := testDiscoverer(srv).Run(context.Background(), srv.URL+"/")
	require.NoError(t, err)

	assert.Nil(t, r.Sample)
	assert.True(t, r.Detection.Fallback)
	assert.Regexp(t, `^checked /guide/deep/start: .*HTTP 404; checked /intro: the vitepress selector matched nothing there either$`, r.SampleNote)
}

// contentServer serves each route's body as HTML. (fileServer above maps
// routes to files on disk instead.)
func contentServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for route, body := range routes {
		mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, body)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDraftSite_TooComplexSeedFails(t *testing.T) {
	dense := "<html><body><main>" + strings.Repeat("<p>a</p>", pagebudget.MaxPageUnits/2+1) + "</main></body></html>"
	srv := contentServer(t, map[string]string{"/docs/": dense})

	_, err := testDiscoverer(srv).DraftSite(context.Background(), DraftRequest{URL: srv.URL + "/docs/"})
	require.Error(t, err)
	assert.ErrorIs(t, err, pagebudget.ErrPageTooComplex)
}

func TestDraftSite_ReleasesItsReservation(t *testing.T) {
	srv := contentServer(t, map[string]string{"/docs/": `<html><head><title>T</title></head><body><main><h1>T</h1><p>hello</p></main></body></html>`})
	for range 3 {
		_, _ = testDiscoverer(srv).DraftSite(context.Background(), DraftRequest{URL: srv.URL + "/docs/"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, release, err := pagebudget.Shared.Reserve(ctx, pagebudget.MaxPageUnits)
	require.NoError(t, err, "every DraftSite call returned its reservation")
	release()
}

func TestParseSitemapStream_SkipsOverlongLoc(t *testing.T) {
	in := `<urlset><url><loc>https://example.com/` + strings.Repeat("a", 3000) + `</loc></url><url><loc>https://example.com/ok</loc></url></urlset>`
	doc := parseSitemapStream(strings.NewReader(in), 10)
	assert.Equal(t, []string{"https://example.com/ok"}, doc.locs)
}

func TestStreamSitemap_GzipCappedAfterInflate(t *testing.T) {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	_, _ = zw.Write([]byte(`<urlset><url><loc>https://example.com/docs/a</loc></url><url><loc>`))
	chunk := []byte(strings.Repeat("a", 1<<20))
	for range 256 {
		_, _ = zw.Write(chunk)
	}
	_, _ = zw.Write([]byte(`</loc></url></urlset>`))
	_ = zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(buf.Bytes()) }))
	t.Cleanup(srv.Close)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	doc, ok := testDiscoverer(srv).streamSitemap(context.Background(), srv.URL+"/sitemap.xml", "127.0.0.1", 50000)
	runtime.ReadMemStats(&after)

	require.True(t, ok)
	assert.Equal(t, []string{"https://example.com/docs/a"}, doc.locs)
	assert.True(t, doc.truncated)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(300<<20), "256 MiB inflated must stop at the cap")
}

// offHost rewrites a test server URL to the same listener under the name
// localhost, which is a different hostname from 127.0.0.1.
func offHost(srv *httptest.Server) string {
	return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
}

func TestFetchSitemap_SkipsOffHostSitemaps(t *testing.T) {
	var offHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		offHits.Add(1)
		_, _ = io.WriteString(w, `<urlset><url><loc>https://example.com/docs/x</loc></url></urlset>`)
	}))
	t.Cleanup(other.Close)

	t.Run("robots", func(t *testing.T) {
		srv := contentServer(t, map[string]string{})
		info := testDiscoverer(srv).fetchSitemap(context.Background(), mustURL(t, srv.URL+"/docs/"), []string{offHost(other) + "/sitemap.xml"})
		assert.False(t, info.Found)
	})
	t.Run("shard", func(t *testing.T) {
		srv := contentServer(t, map[string]string{
			"/sitemap.xml": `<sitemapindex><sitemap><loc>` + offHost(other) + `/s1.xml</loc></sitemap></sitemapindex>`,
		})
		info := testDiscoverer(srv).fetchSitemap(context.Background(), mustURL(t, srv.URL+"/docs/"), nil)
		assert.Empty(t, info.URLs)
	})
	t.Run("redirect", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, offHost(other)+"/sitemap.xml", http.StatusFound)
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		info := testDiscoverer(srv).fetchSitemap(context.Background(), mustURL(t, srv.URL+"/docs/"), nil)
		assert.False(t, info.Found)
	})
	// The HTTP client follows the redirect before the host check sees it, so
	// the off-host server gets exactly that one request; robots and shard
	// sitemaps on another host are never requested.
	assert.EqualValues(t, 1, offHits.Load())
}
