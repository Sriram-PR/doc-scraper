package mcp

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

// Under -race this fails if any handler reads the sites map without the lock.
func TestSites_PutWhileHandlersRead(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 50 {
			s.putSite(fmt.Sprintf("site%d", i), &config.SiteConfig{AllowedDomain: "x.example.com", AllowedPathPrefix: "/"})
		}
	}()
	for range 50 {
		callJSON(t, s.handleListSites, map[string]any{})
		callJSON(t, s.handleDescribeServer, map[string]any{})
		callJSON(t, s.handleGetFreshness, map[string]any{"site_key": "docs"})
	}
	wg.Wait()

	assert.Len(t, s.siteKeys(), 51)
	got, ok := s.site("site7")
	assert.True(t, ok)
	assert.Equal(t, "x.example.com", got.AllowedDomain)
}

func TestSites_PutCreatesMissingMap(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	s.cfg.AppConfig.Sites = nil
	s.putSite("a", &config.SiteConfig{AllowedDomain: "a.example.com"})
	assert.Equal(t, []string{"a"}, s.siteKeys())
}
