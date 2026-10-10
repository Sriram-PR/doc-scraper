package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// newCrawlJobServer builds a Server whose single site points at an httptest
// docs server, with real (temp) state and output dirs so runCrawlJob can run
// end to end without touching the network.
func newCrawlJobServer(t *testing.T, siteKey string) (*Server, *config.SiteConfig, *atomic.Int32, string) {
	t.Helper()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docs/index.html" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><title>Doc</title></head><body><main id="c"><p>STABLE CONTENT</p></main></body></html>`)
	}))
	t.Cleanup(srv.Close)

	host := strings.TrimPrefix(srv.URL, "http://")
	host = host[:strings.LastIndex(host, ":")]
	siteCfg := &config.SiteConfig{
		StartURLs:         []string{srv.URL + "/docs/index.html"},
		AllowedDomain:     host,
		AllowedPathPrefix: "/docs",
		ContentSelector:   "#c",
	}
	outDir := t.TempDir()
	appCfg := &config.AppConfig{
		NumWorkers:         2,
		MaxRequests:        4,
		MaxRequestsPerHost: 2,
		OutputBaseDir:      outDir,
		StateDir:           t.TempDir(),
		DefaultUserAgent:   "test-agent",
		MaxRetries:         1,
		HTTPClientSettings: config.HTTPClientConfig{
			Timeout:              10 * time.Second,
			AllowPrivateNetworks: true,
		},
		EnableJSONLOutput:   true,
		JSONLOutputFilename: "pages.jsonl",
		Sites:               map[string]*config.SiteConfig{siteKey: siteCfg},
	}
	s := &Server{
		cfg:        &ServerConfig{AppConfig: appCfg, Logger: silentTestLogger()},
		log:        silentTestLogger(),
		jobManager: NewJobManager("", nil),
	}
	return s, siteCfg, &hits, filepath.Join(outDir, utils.SanitizeFilename(siteKey))
}

func runJob(t *testing.T, s *Server, siteCfg *config.SiteConfig, siteKey string, incremental bool) {
	t.Helper()
	job, err := s.jobManager.CreateJob(siteKey, incremental)
	require.NoError(t, err)
	s.runCrawlJob(job, siteCfg, siteKey)
	got := s.jobManager.GetJob(job.ID)
	require.NotNil(t, got)
	require.Equal(t, JobStatusCompleted, got.Status, "job error: %s", got.ErrorMessage)
}

func firstMarkdownFile(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			return filepath.Join(dir, e.Name())
		}
	}
	t.Fatalf("no .md file in %s", dir)
	return ""
}

// An incremental MCP job must run against the previous crawl's visited DB so
// unchanged pages are skipped, like `crawl -incremental` does on the CLI.
// Regression test for #22: the job always started fresh, so nothing was ever
// skipped.
func TestRunCrawlJob_IncrementalSkipsUnchangedPages(t *testing.T) {
	const siteKey = "docs"
	s, siteCfg, hits, siteOut := newCrawlJobServer(t, siteKey)

	runJob(t, s, siteCfg, siteKey, false)
	md := firstMarkdownFile(t, siteOut)

	// Pin the output's mtime far in the past: a rewrite moves it to "now",
	// a skip leaves it alone. Immune to coarse filesystem clocks.
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(md, old, old))
	require.EqualValues(t, 1, hits.Load())

	runJob(t, s, siteCfg, siteKey, true)

	assert.EqualValues(t, 2, hits.Load(), "incremental still re-fetches the page to compare it")
	info, err := os.Stat(firstMarkdownFile(t, siteOut))
	require.NoError(t, err)
	assert.True(t, info.ModTime().Equal(old),
		"unchanged page was rewritten by an incremental job (mtime %v, want %v)", info.ModTime(), old)
}

// A non-incremental job keeps its fresh, staged behavior: it rebuilds the
// output from scratch.
func TestRunCrawlJob_FullCrawlStillRebuilds(t *testing.T) {
	const siteKey = "docs"
	s, siteCfg, _, siteOut := newCrawlJobServer(t, siteKey)

	runJob(t, s, siteCfg, siteKey, false)
	md := firstMarkdownFile(t, siteOut)
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(md, old, old))

	runJob(t, s, siteCfg, siteKey, false)

	info, err := os.Stat(firstMarkdownFile(t, siteOut))
	require.NoError(t, err)
	assert.False(t, info.ModTime().Equal(old), "a full crawl must rewrite the output")
}
