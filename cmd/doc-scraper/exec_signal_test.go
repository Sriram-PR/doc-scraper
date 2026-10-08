//go:build !windows

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkglog "github.com/Sriram-PR/doc-scraper/v2/pkg/log"
)

// signalServer serves a three-page site and, once armed, sends SIGINT to this
// process when /guide/ is requested.
func signalServer(t *testing.T) (srv *httptest.Server, arm func()) {
	t.Helper()
	var mu sync.Mutex
	armed := false
	var once sync.Once
	inner := crawlFixtureServer(t)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fire := armed && strings.HasPrefix(r.URL.Path, "/guide")
		mu.Unlock()
		if fire {
			once.Do(func() { _ = syscall.Kill(os.Getpid(), syscall.SIGINT) })
			time.Sleep(200 * time.Millisecond)
		}
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() { mu.Lock(); armed = true; mu.Unlock() }
}

func liveCorpusFiles(t *testing.T, outDir string) int {
	t.Helper()
	return countFiles(t, filepath.Join(outDir, "fixture"), "*.md")
}

func TestExecuteCrawl_InterruptedExitsNonzeroAndKeepsCorpus(t *testing.T) {
	srv, arm := signalServer(t)
	cfgPath, outDir, stateDir := writeCrawlConfig(t, srv.URL, "")

	require.Equal(t, 0, executeCrawl(cfgPath, "fixture", "error", pkglog.FormatText, "", false, false, false))
	before := liveCorpusFiles(t, outDir)
	require.GreaterOrEqual(t, before, 3)

	arm()
	code := executeCrawl(cfgPath, "fixture", "error", pkglog.FormatText, "", false, false, false)
	assert.Equal(t, 130, code)
	assert.Equal(t, before, liveCorpusFiles(t, outDir), "interrupted fresh re-crawl keeps the live corpus")
	assert.DirExists(t, filepath.Join(outDir, "fixture.staging"))
	assert.DirExists(t, filepath.Join(stateDir, "fixture_visited_db.staging"))
}

func TestExecuteParallelCrawl_InterruptedExitsNonzeroAndKeepsCorpus(t *testing.T) {
	srv, arm := signalServer(t)
	extra := fmt.Sprintf(`  second:
    start_urls: ["%s/api/"]
    allowed_domain: "%s"
    content_selector: "main"
    max_depth: 1
`, srv.URL, hostOnly(srv.URL))
	cfgPath, outDir, _ := writeCrawlConfig(t, srv.URL, extra)

	keys := []string{"fixture", "second"}
	require.Equal(t, 0, executeParallelCrawl(cfgPath, keys, false, "error", pkglog.FormatText, "", false, false, false))
	before := liveCorpusFiles(t, outDir)

	arm()
	code := executeParallelCrawl(cfgPath, keys, false, "error", pkglog.FormatText, "", false, false, false)
	assert.Equal(t, 130, code)
	assert.Equal(t, before, liveCorpusFiles(t, outDir))
}

func TestExecuteCrawl_NoForcedExitAfterInterruptedCrawlReturns(t *testing.T) {
	srv, arm := signalServer(t)
	cfgPath, _, _ := writeCrawlConfig(t, srv.URL, "")

	var exits atomic.Int32
	origGrace, origExit := shutdownGrace, forceExit
	shutdownGrace, forceExit = time.Second, func(int) { exits.Add(1) }
	t.Cleanup(func() { shutdownGrace, forceExit = origGrace, origExit })

	arm()
	require.Equal(t, 130, executeCrawl(cfgPath, "fixture", "error", pkglog.FormatText, "", false, false, false))

	time.Sleep(1500 * time.Millisecond)
	assert.Zero(t, exits.Load(), "grace-period watchdog must not outlive the crawl")
}
