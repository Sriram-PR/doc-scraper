package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

// lossEnv is a staged-crawl harness over a site of an index page plus
// pages p1..pN, each with a #c content region.
type lossEnv struct {
	appCfg  *config.AppConfig
	siteCfg *config.SiteConfig
	idx     *index.Index
	ms      *mutableDocServer
	url     string
	pages   int
}

func pageBody(n int) string {
	return fmt.Sprintf(`<html><head><title>P%d</title></head><body><main id="c"><p>page %d body</p></main></body></html>`, n, n)
}

func newLossEnv(t *testing.T, pages int) *lossEnv {
	t.Helper()
	srv, ms := newMutableDocServer(t)
	var links strings.Builder
	for i := 1; i <= pages; i++ {
		fmt.Fprintf(&links, `<a href="/docs/p%d.html">p%d</a>`, i, i)
		ms.set(fmt.Sprintf("/docs/p%d.html", i), pageBody(i))
	}
	ms.set("/docs/index.html", `<html><head><title>Index</title></head><body><main id="c"><p>index</p>`+links.String()+`</main></body></html>`)
	appCfg := newTestAppConfig(t)
	appCfg.InitialRetryDelay = 10 * time.Millisecond
	appCfg.MaxRetryDelay = 20 * time.Millisecond
	idx, err := index.OpenAt(appCfg.StateDir, 10, silentLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	siteCfg := baseSiteConfig(srv, "/docs/index.html")
	siteCfg.ContentSelector = "#c"
	return &lossEnv{appCfg: appCfg, siteCfg: siteCfg, idx: idx, ms: ms, url: srv.URL, pages: pages}
}

func (e *lossEnv) crawl(t *testing.T, resume, allowShrink bool) (CrawlerProgress, error) {
	t.Helper()
	crawlLog := newCrawlLog(t)
	logger := crawlLog.logger()
	ctx, cancel := crawlLog.deadlineContext(t, e.appCfg)
	defer cancel()
	stage, store, err := OpenStagedStore(ctx, e.appCfg, testSiteKey, resume, logger)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	httpClient := fetch.NewClient(e.appCfg.HTTPClientSettings, logger)
	fetcher := fetch.NewFetcher(httpClient, e.appCfg, logger)
	rl := fetch.NewRateLimiter(e.appCfg.DefaultDelayPerHost, logger)
	c, err := NewCrawlerWithOptions(e.appCfg, e.siteCfg, testSiteKey, logger, store, fetcher, rl, ctx, cancel, resume,
		&CrawlerOptions{Index: e.idx, Stage: stage, AllowShrink: allowShrink})
	require.NoError(t, err)
	err = c.Run(resume)
	return c.GetProgress(), err
}

func (e *lossEnv) liveJSONLPages(t *testing.T) int64 {
	t.Helper()
	n, err := countPriorPageRecords(e.liveJSONLPath())
	require.NoError(t, err)
	return n
}

func (e *lossEnv) liveJSONLPath() string {
	return siteOutputDir(e.appCfg, e.siteCfg) + string(os.PathSeparator) + e.appCfg.JSONLOutputFilename
}

func (e *lossEnv) liveMarkdownFiles(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(siteOutputDir(e.appCfg, e.siteCfg))
	require.NoError(t, err)
	n := 0
	for _, de := range entries {
		if strings.HasSuffix(de.Name(), ".md") {
			n++
		}
	}
	return n
}

func (e *lossEnv) latestCrawl(t *testing.T) *index.LatestCrawl {
	t.Helper()
	lc, err := e.idx.GetLatestCrawl(context.Background(), testSiteKey)
	require.NoError(t, err)
	return lc
}

func (e *lossEnv) failPages(from, to, code int) {
	for i := from; i <= to; i++ {
		e.ms.setStatus(fmt.Sprintf("/docs/p%d.html", i), code)
	}
}

func requireLossy(t *testing.T, err error, reason string) *LossyCrawlError {
	t.Helper()
	var lossy *LossyCrawlError
	require.True(t, errors.As(err, &lossy), "want *LossyCrawlError, got %v", err)
	assert.Equal(t, reason, lossy.Reason)
	return lossy
}

func TestLossGuard_TransientFailuresKeepLiveCorpusUntilResume(t *testing.T) {
	e := newLossEnv(t, 20) // 21 pages with the index
	_, err := e.crawl(t, false, false)
	require.NoError(t, err)
	require.EqualValues(t, 21, e.liveJSONLPages(t))

	e.failPages(6, 20, http.StatusInternalServerError)
	_, err = e.crawl(t, false, false)

	lossy := requireLossy(t, err, RefusedTemporaryFailures)
	assert.Equal(t, 15, lossy.Transient)
	assert.Equal(t, 21, lossy.Attempted)
	assert.True(t, lossy.StagingKept)
	assert.EqualValues(t, 21, e.liveJSONLPages(t), "live corpus untouched")
	assert.EqualValues(t, 1, e.latestCrawl(t).ID, "refused run not recorded")
	assert.DirExists(t, e.appCfg.SiteStagingOutputDir(testSiteKey), "staging kept for resume")

	e.failPages(6, 20, 0)
	_, err = e.crawl(t, true, false)
	require.NoError(t, err)
	assert.EqualValues(t, 21, e.liveJSONLPages(t))
	assert.EqualValues(t, 2, e.latestCrawl(t).ID)
	assert.NoDirExists(t, e.appCfg.SiteStagingOutputDir(testSiteKey))
}

func TestLossGuard_EmptyCrawlWithNoindexPageRefused(t *testing.T) {
	e := newLossEnv(t, 4)
	_, err := e.crawl(t, false, false)
	require.NoError(t, err)
	require.EqualValues(t, 5, e.liveJSONLPages(t))

	e.ms.set("/docs/p4.html", `<html><head><title>P4</title><meta name="robots" content="noindex"></head><body><main id="c">p4</main></body></html>`)
	e.siteCfg.ContentSelector = ".nonexistent"
	_, err = e.crawl(t, false, false)

	requireLossy(t, err, RefusedEmpty)
	assert.ErrorContains(t, err, "zero successful pages")
	assert.EqualValues(t, 5, e.liveJSONLPages(t))
	assert.Equal(t, 5, e.liveMarkdownFiles(t))
}

func TestLossGuard_ShrinkRefusedUnlessAllowed(t *testing.T) {
	e := newLossEnv(t, 20)
	_, err := e.crawl(t, false, false)
	require.NoError(t, err)

	for i := 7; i <= 20; i++ {
		e.ms.set(fmt.Sprintf("/docs/p%d.html", i), fmt.Sprintf(`<html><head><title>P%d</title></head><body><p>moved</p></body></html>`, i))
	}
	_, err = e.crawl(t, false, false)
	lossy := requireLossy(t, err, RefusedShrink)
	assert.Equal(t, 7, lossy.NewPages)
	assert.Equal(t, 21, lossy.Baseline)
	assert.EqualValues(t, 21, e.liveJSONLPages(t))

	_, err = e.crawl(t, false, true)
	require.NoError(t, err)
	assert.EqualValues(t, 7, e.liveJSONLPages(t))
	assert.Equal(t, 7, e.latestCrawl(t).TotalPages)

	_, err = e.crawl(t, false, false)
	require.NoError(t, err, "the next crawl compares against the accepted, smaller corpus")
}

func TestLossGuard_IncrementalTransientFailuresNotRecorded(t *testing.T) {
	e := newLossEnv(t, 20)
	_, err := e.crawl(t, false, false)
	require.NoError(t, err)

	e.appCfg.EnableIncremental = true
	e.failPages(6, 20, http.StatusInternalServerError)
	e.ms.remove("/docs/p5.html") // a 404 page would be dropped from disk if the run were published
	_, err = e.crawl(t, true, false)

	lossy := requireLossy(t, err, RefusedTemporaryFailures)
	assert.False(t, lossy.StagingKept, "incremental runs in place")
	assert.Equal(t, 21, e.liveMarkdownFiles(t), "old files kept, including the 404 page's")
	assert.EqualValues(t, 21, e.liveJSONLPages(t))
	assert.EqualValues(t, 1, e.latestCrawl(t).ID, "refused incremental run not recorded")
}

func TestLossGuard_TransientFailureCarriesOldPageForward(t *testing.T) {
	e := newLossEnv(t, 20)
	_, err := e.crawl(t, false, false)
	require.NoError(t, err)
	oldHash := jsonlHashOf(t, e.liveJSONLPath(), e.url+"/docs/p10.html")

	e.failPages(10, 10, http.StatusServiceUnavailable)
	p, err := e.crawl(t, false, false)

	require.NoError(t, err)
	assert.EqualValues(t, 1, p.PagesCarriedForward)
	assert.EqualValues(t, 21, e.liveJSONLPages(t))
	assert.Equal(t, oldHash, jsonlHashOf(t, e.liveJSONLPath(), e.url+"/docs/p10.html"))
	diff, err := e.idx.DiffPrevious(context.Background(), testSiteKey, 100, 0)
	require.NoError(t, err)
	for _, entry := range diff.Entries {
		assert.NotEqual(t, "removed", entry.Kind, "carried page must not show as removed: %s", entry.URL)
	}
}

func jsonlHashOf(t *testing.T, path, pageURL string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, line := range strings.Split(string(b), "\n") {
		var rec struct {
			URL         string `json:"url"`
			ContentHash string `json:"content_hash"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.URL == pageURL {
			return rec.ContentHash
		}
	}
	t.Fatalf("no record for %s", pageURL)
	return ""
}
