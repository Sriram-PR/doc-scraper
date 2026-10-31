package discover

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDraft_LowercasesAllowedDomain(t *testing.T) {
	u, err := url.Parse("http://Docs.Example.COM/guide/")
	require.NoError(t, err)

	draft := BuildDraft(&Report{FinalURL: u}, "")

	assert.Equal(t, "docs.example.com", draft.Site.AllowedDomain)
}

// A robots.txt Sitemap line may spell the host differently from the page we
// started on; it is still the same site.
func TestFetchSitemap_RobotsSitemapHostCase(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maps/docs.xml" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>%s/docs/a</loc></url></urlset>`, srv.URL)
	}))
	t.Cleanup(srv.Close)

	seed := mustURL(t, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)+"/docs/")
	robots := []string{strings.Replace(srv.URL, "127.0.0.1", "LocalHost", 1) + "/maps/docs.xml"}

	info := testDiscoverer(srv).fetchSitemap(context.Background(), seed, robots)

	assert.True(t, info.Found)
	assert.Len(t, info.URLs, 1)
}
