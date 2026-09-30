package crawler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

type hookServer struct {
	*httptest.Server
	mu      sync.Mutex
	onHit   func(path string)
	version string
}

func newHookServer(t *testing.T, pageNames ...string) *hookServer {
	t.Helper()
	hs := &hookServer{version: "v1"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hs.mu.Lock()
		hook, version := hs.onHit, hs.version
		hs.mu.Unlock()
		if hook != nil {
			hook(r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/docs/index.html" {
			body := "<html><head><title>Index</title></head><body><p>index " + version + "</p>"
			for _, n := range pageNames {
				body += fmt.Sprintf(`<a href="/docs/%s.html">%s</a>`, n, n)
			}
			_, _ = io.WriteString(w, body+"</body></html>")
			return
		}
		for _, n := range pageNames {
			if r.URL.Path == "/docs/"+n+".html" {
				_, _ = io.WriteString(w, fmt.Sprintf("<html><head><title>%s</title></head><body><p>%s %s body text</p></body></html>", n, n, version))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	hs.Server = srv
	return hs
}

func (hs *hookServer) setHook(f func(string)) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.onHit = f
}

func (hs *hookServer) setVersion(v string) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.version = v
}

type stageEnv struct {
	appCfg  *config.AppConfig
	siteCfg *config.SiteConfig
	idx     *index.Index
	srv     *hookServer
}

func newStageEnv(t *testing.T, pageNames ...string) *stageEnv {
	t.Helper()
	srv := newHookServer(t, pageNames...)
	appCfg := newTestAppConfig(t)
	appCfg.NumWorkers = 1
	appCfg.MaxRequestsPerHost = 1
	idx, err := index.OpenAt(appCfg.StateDir, 10, silentLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	return &stageEnv{appCfg: appCfg, siteCfg: baseSiteConfig(srv.Server, "/docs/index.html"), idx: idx, srv: srv}
}

func (e *stageEnv) crawl(t *testing.T, ctx context.Context, cancel context.CancelFunc, resume bool) error {
	t.Helper()
	logger := silentLogger()
	stage, store, err := OpenStagedStore(ctx, e.appCfg, testSiteKey, resume, logger)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	httpClient := fetch.NewClient(e.appCfg.HTTPClientSettings, logger)
	fetcher := fetch.NewFetcher(httpClient, e.appCfg, logger)
	rl := fetch.NewRateLimiter(e.appCfg.DefaultDelayPerHost, logger)
	c, err := NewCrawlerWithOptions(e.appCfg, e.siteCfg, testSiteKey, logger, store, fetcher, rl, ctx, cancel, resume, &CrawlerOptions{Index: e.idx, Stage: stage})
	require.NoError(t, err)
	return c.Run(resume)
}

func (e *stageEnv) fullCrawl(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, e.crawl(t, ctx, cancel, false))
}

func (e *stageEnv) interruptedCrawl(t *testing.T, resume bool, cancelOn string) error {
	t.Helper()
	ctx, parentCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer parentCancel()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.srv.setHook(func(p string) {
		if p == cancelOn {
			cancel()
		}
	})
	defer e.srv.setHook(nil)
	return e.crawl(t, ctx, cancel, resume)
}

func (e *stageEnv) liveJSONL() string {
	return filepath.Join(siteOutputDir(e.appCfg, e.siteCfg), e.appCfg.JSONLOutputFilename)
}

func (e *stageEnv) latestCrawl(t *testing.T) *index.LatestCrawl {
	t.Helper()
	lc, err := e.idx.GetLatestCrawl(context.Background(), testSiteKey)
	require.NoError(t, err)
	return lc
}

func (e *stageEnv) stagingOut() string { return e.appCfg.SiteStagingOutputDir(testSiteKey) }

func (e *stageEnv) stagingDB() string {
	return storage.StagingVisitedDBPath(e.appCfg.StateDir, testSiteKey)
}

func (e *stageEnv) liveVisitedCount(t *testing.T) int {
	t.Helper()
	store, err := storage.NewBadgerStoreAt(context.Background(), storage.VisitedDBPath(e.appCfg.StateDir, testSiteKey), true, silentLogger())
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	n, err := store.GetVisitedCount()
	require.NoError(t, err)
	return n
}

func (e *stageEnv) liveContains(t *testing.T, needle string) bool {
	t.Helper()
	b, err := os.ReadFile(e.liveJSONL())
	require.NoError(t, err)
	return strings.Contains(string(b), needle)
}

func TestInterruptedFreshRecrawlKeepsLiveCorpus(t *testing.T) {
	e := newStageEnv(t, "a", "b", "c", "d")
	e.fullCrawl(t)

	before, err := countPriorPageRecords(e.liveJSONL())
	require.NoError(t, err)
	require.EqualValues(t, 5, before)
	require.EqualValues(t, 1, e.latestCrawl(t).ID)
	visited := e.liveVisitedCount(t)
	chunksBefore, err := e.idx.ChunkedHashes(context.Background(), testSiteKey)
	require.NoError(t, err)

	e.srv.setVersion("v2")
	err = e.interruptedCrawl(t, false, "/docs/c.html")
	require.ErrorIs(t, err, context.Canceled)

	after, err := countPriorPageRecords(e.liveJSONL())
	require.NoError(t, err)
	assert.EqualValues(t, 5, after, "interrupted re-crawl must not shrink the live corpus")
	assert.False(t, e.liveContains(t, "v2 body"), "live corpus must not absorb staged pages")
	assert.Equal(t, visited, e.liveVisitedCount(t), "live visited DB untouched")
	lc := e.latestCrawl(t)
	assert.EqualValues(t, 1, lc.ID, "interrupted crawl must not be recorded in history")
	assert.Equal(t, 5, lc.TotalPages)
	chunksAfter, err := e.idx.ChunkedHashes(context.Background(), testSiteKey)
	require.NoError(t, err)
	assert.Equal(t, chunksBefore, chunksAfter, "search chunks unchanged")
	assert.DirExists(t, e.stagingOut(), "staging kept for resume")
	assert.DirExists(t, e.stagingDB())
}

func TestFreshRecrawlSwapsInNewCorpus(t *testing.T) {
	e := newStageEnv(t, "a", "b")
	e.fullCrawl(t)
	e.srv.setVersion("v2")
	e.fullCrawl(t)

	assert.True(t, e.liveContains(t, "v2 body"))
	assert.False(t, e.liveContains(t, "v1 body"))
	assert.NoDirExists(t, e.stagingOut())
	assert.NoDirExists(t, e.stagingDB())
	assert.NoDirExists(t, siteOutputDir(e.appCfg, e.siteCfg)+backupSuffix)
	lc := e.latestCrawl(t)
	assert.EqualValues(t, 2, lc.ID)
	assert.Equal(t, index.ModeFull, lc.Mode)
	assert.Equal(t, 3, lc.TotalPages)
}

func TestResumeContinuesStagedCrawlAndSwaps(t *testing.T) {
	e := newStageEnv(t, "a", "b", "c", "d")
	e.fullCrawl(t)
	e.srv.setVersion("v2")

	require.ErrorIs(t, e.interruptedCrawl(t, false, "/docs/c.html"), context.Canceled)
	require.EqualValues(t, 1, e.latestCrawl(t).ID)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, e.crawl(t, ctx, cancel, true))

	assert.NoDirExists(t, e.stagingOut())
	assert.NoDirExists(t, e.stagingDB())
	assert.False(t, e.liveContains(t, "v1 body"))
	n, err := countPriorPageRecords(e.liveJSONL())
	require.NoError(t, err)
	assert.EqualValues(t, 5, n)
	lc := e.latestCrawl(t)
	assert.EqualValues(t, 2, lc.ID, "exactly one history row for the completed run")
	assert.Equal(t, index.ModeResume, lc.Mode)
	assert.Equal(t, 5, lc.TotalPages)
}

func TestFreshCrawlDiscardsStaleStaging(t *testing.T) {
	e := newStageEnv(t, "a", "b", "c", "d")
	e.fullCrawl(t)
	require.ErrorIs(t, e.interruptedCrawl(t, false, "/docs/c.html"), context.Canceled)
	stale := filepath.Join(e.stagingOut(), "stale.txt")
	require.NoError(t, os.WriteFile(stale, []byte("x"), 0o644))

	e.srv.setVersion("v2")
	e.fullCrawl(t)

	assert.NoFileExists(t, filepath.Join(siteOutputDir(e.appCfg, e.siteCfg), "stale.txt"))
	assert.NoDirExists(t, e.stagingOut())
	lc := e.latestCrawl(t)
	assert.EqualValues(t, 2, lc.ID)
	assert.Equal(t, index.ModeFull, lc.Mode)
}

func TestInterruptedInPlaceResumeRecordsNoHistoryButKeepsSearchInSync(t *testing.T) {
	e := newStageEnv(t, "a", "b", "c", "d")
	e.fullCrawl(t)
	e.srv.setVersion("v2")
	e.appCfg.EnableIncremental = true

	require.ErrorIs(t, e.interruptedCrawl(t, true, "/docs/c.html"), context.Canceled)

	assert.NoDirExists(t, e.stagingOut(), "resume without staging works in place")
	assert.EqualValues(t, 1, e.latestCrawl(t).ID, "interrupted run records no history")
	live, err := countPriorPageRecords(e.liveJSONL())
	require.NoError(t, err)
	chunks, err := e.idx.ChunkedHashes(context.Background(), testSiteKey)
	require.NoError(t, err)
	assert.Len(t, chunks, int(live), "search index matches the live corpus")
}

func TestAllPagesFailingFreshCrawlKeepsLiveCorpus(t *testing.T) {
	e := newStageEnv(t, "a", "b")
	e.fullCrawl(t)
	e.srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := e.crawl(t, ctx, cancel, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zero successful pages")
	assert.True(t, e.liveContains(t, "v1 body"))
	assert.EqualValues(t, 1, e.latestCrawl(t).ID)

	err = e.crawl(t, ctx, cancel, true)
	require.Error(t, err, "resuming an all-failed staged crawl must not swap in an empty corpus")
	assert.True(t, e.liveContains(t, "v1 body"))
}

func TestStageCommitRollsBackOnFailure(t *testing.T) {
	tmp := t.TempDir()
	write := func(dir, name string) {
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))
	}
	s := &Stage{
		Staged:   true,
		log:      silentLogger(),
		liveOut:  filepath.Join(tmp, "out"),
		stageOut: filepath.Join(tmp, "out.staging"),
		liveDB:   filepath.Join(tmp, "missing-parent", "db"),
		stageDB:  filepath.Join(tmp, "db.staging"),
	}
	write(s.liveOut, "old")
	write(s.stageOut, "new")
	write(s.stageDB, "newdb")

	require.Error(t, s.Commit())
	assert.FileExists(t, filepath.Join(s.liveOut, "old"), "old live corpus restored")
	assert.FileExists(t, filepath.Join(s.stageOut, "new"), "staging kept for retry")
	assert.FileExists(t, filepath.Join(s.stageDB, "newdb"))
	assert.NoDirExists(t, s.liveOut+backupSuffix)
}

func TestPlanStageRestoresInterruptedSwap(t *testing.T) {
	appCfg := newTestAppConfig(t)
	live := appCfg.SiteOutputDir(testSiteKey)
	require.NoError(t, os.MkdirAll(live+backupSuffix, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(live+backupSuffix, "old"), []byte("x"), 0o644))

	_, err := PlanStage(appCfg, testSiteKey, true, silentLogger())
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(live, "old"))
	assert.NoDirExists(t, live+backupSuffix)
}

func TestPlanStageDiscardsHalfPromotedStaging(t *testing.T) {
	appCfg := newTestAppConfig(t)
	live := appCfg.SiteOutputDir(testSiteKey)
	stageDB := storage.StagingVisitedDBPath(appCfg.StateDir, testSiteKey)
	require.NoError(t, os.MkdirAll(live, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(live, "page.md"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(stageDB, 0o755))

	s, err := PlanStage(appCfg, testSiteKey, true, silentLogger())
	require.NoError(t, err)
	assert.False(t, s.Staged, "a staged DB without its output must not be resumed into an empty staging dir")
	assert.Equal(t, live, s.OutputDir())
	assert.NoDirExists(t, stageDB)
	assert.FileExists(t, filepath.Join(live, "page.md"))
}
