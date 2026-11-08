package mcp

import (
	"context"
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
	s, siteCfg, siteOut := newCrawlJobServerWith(t, siteKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docs/index.html" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><title>Doc</title></head><body><main id="c"><p>STABLE CONTENT</p></main></body></html>`)
	}))
	return s, siteCfg, &hits, siteOut
}

// newCrawlJobServerWith is newCrawlJobServer with a caller-supplied docs
// server; the start URL is /docs/index.html.
func newCrawlJobServerWith(t *testing.T, siteKey string, h http.Handler) (*Server, *config.SiteConfig, string) {
	t.Helper()

	srv := httptest.NewServer(h)
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
		JSONLOutputFilename: "pages.jsonl",
		Sites:               map[string]*config.SiteConfig{siteKey: siteCfg},
	}
	s := &Server{
		cfg:        &ServerConfig{AppConfig: appCfg, Logger: silentTestLogger()},
		log:        silentTestLogger(),
		jobManager: NewJobManager("", nil),
	}
	return s, siteCfg, filepath.Join(outDir, utils.SanitizeFilename(siteKey))
}

func runJob(t *testing.T, s *Server, siteCfg *config.SiteConfig, siteKey string, incremental bool) {
	t.Helper()
	job, err := s.jobManager.CreateJob(siteKey, incremental)
	require.NoError(t, err)
	s.runCrawlJob(job, siteCfg, siteKey, false)
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

// get_job_status must report pages as they finish, not only on the crawler's
// 30 second progress tick. Regression test for #18.
func TestGetJobStatus_ReportsLiveProgress(t *testing.T) {
	const siteKey = "docs"
	release := make(chan struct{})
	s, siteCfg, _ := newCrawlJobServerWith(t, siteKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/docs/index.html":
			_, _ = io.WriteString(w, `<html><head><title>Index</title></head><body><main id="c"><p>INDEX</p><a href="/docs/slow.html">slow</a></main></body></html>`)
		case "/docs/slow.html":
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, `<html><head><title>Slow</title></head><body><main id="c"><p>SLOW</p></main></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))

	job, err := s.jobManager.CreateJob(siteKey, false)
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runCrawlJob(job, siteCfg, siteKey, false)
	}()
	t.Cleanup(func() {
		close(release)
		<-done
	})

	var last map[string]any
	ok := assert.Eventually(t, func() bool {
		_, last = callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": job.ID})
		return last["status"] == string(JobStatusRunning) && last["pages_processed"] == 1.0
	}, 5*time.Second, 20*time.Millisecond, "index page done, slow page in flight")
	if !ok {
		t.Logf("last get_job_status: %v", last)
	}
}

// An incremental job that skips everything must say so: pages_processed
// excludes unchanged pages, so without pages_unchanged it looks like a no-op.
func TestRunCrawlJob_ReportsPagesUnchanged(t *testing.T) {
	const siteKey = "docs"
	s, siteCfg, _, _ := newCrawlJobServer(t, siteKey)

	runJob(t, s, siteCfg, siteKey, false)
	runJob(t, s, siteCfg, siteKey, true)

	jobs := s.jobManager.ListJobs()
	sortJobsNewestFirst(jobs)
	incremental := jobs[0]
	require.True(t, incremental.Incremental)

	_, status := callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": incremental.ID})
	assert.EqualValues(t, 1, status["pages_unchanged"], "get_job_status: %v", status)
	assert.EqualValues(t, 0, status["pages_processed"], "get_job_status: %v", status)

	_, described := callJSON(t, s.handleDescribeServer, map[string]any{})
	recent, ok := described["recent_jobs"].([]any)
	require.True(t, ok, "recent_jobs: %v", described["recent_jobs"])
	require.NotEmpty(t, recent)
	assert.EqualValues(t, 1, recent[0].(map[string]any)["pages_unchanged"], "recent_jobs[0]: %v", recent[0])
}

// get_freshness's running_job reports unchanged pages live, like
// get_job_status does.
func TestGetFreshness_RunningJobReportsPagesUnchanged(t *testing.T) {
	const siteKey = "docs"
	var block atomic.Bool
	release := make(chan struct{})
	s, siteCfg, _ := newCrawlJobServerWith(t, siteKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/docs/index.html":
			_, _ = io.WriteString(w, `<html><head><title>Index</title></head><body><main id="c"><p>INDEX</p><a href="/docs/slow.html">slow</a></main></body></html>`)
		case "/docs/slow.html":
			if block.Load() {
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = io.WriteString(w, `<html><head><title>Slow</title></head><body><main id="c"><p>SLOW</p></main></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))

	runJob(t, s, siteCfg, siteKey, false)

	block.Store(true)
	job, err := s.jobManager.CreateJob(siteKey, true)
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runCrawlJob(job, siteCfg, siteKey, false)
	}()
	t.Cleanup(func() {
		close(release)
		<-done
	})

	var last map[string]any
	ok := assert.Eventually(t, func() bool {
		_, last = callJSON(t, s.handleGetFreshness, map[string]any{"site_key": siteKey})
		running, _ := last["running_job"].(map[string]any)
		return running != nil && running["pages_unchanged"] == 1.0
	}, 5*time.Second, 20*time.Millisecond, "index page unchanged, slow page in flight")
	if !ok {
		t.Logf("last get_freshness: %v", last)
	}
}

// cancel_crawl returns before the cancelled crawl has closed its store, and a
// fresh crawl discards staging; crawl_site must wait for the old crawl or the
// two crawls share the site's files (this crashed the server with SIGBUS).
func TestCrawlSite_RestartRightAfterCancel(t *testing.T) {
	s, _, _, _ := newCrawlJobServer(t, "docs")

	// Bounds are generous because a single small crawl can take several seconds
	// on Windows CI runners.
	for range 10 {
		_, first := callJSON(t, s.handleCrawlSite, map[string]any{"site_key": "docs"})
		require.Equal(t, "started", first["status"])
		_, _ = callJSON(t, s.handleCancelCrawl, map[string]any{"job_id": first["job_id"]})

		_, second := callJSON(t, s.handleCrawlSite, map[string]any{"site_key": "docs"})
		require.Equal(t, "started", second["status"])
		id := second["job_id"].(string)
		require.Eventually(t, func() bool {
			return isTerminalStatus(s.jobManager.GetJob(id).Status)
		}, time.Minute, 5*time.Millisecond)
		job := s.jobManager.GetJob(id)
		require.Equal(t, JobStatusCompleted, job.Status, job.ErrorMessage)
		waitForSiteReleased(t, s, "docs")
	}
}

func waitForSiteReleased(t *testing.T, s *Server, siteKey string) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.jobManager.mu.RLock()
		defer s.jobManager.mu.RUnlock()
		return s.jobManager.workers[siteKey] == nil
	}, time.Minute, 5*time.Millisecond)
}

// Shutdown closes the index after cancelling jobs; it must first let the
// cancelled crawls finish closing their stores and index writes.
func TestShutdown_WaitsForCancelledCrawls(t *testing.T) {
	inFlight := make(chan struct{}, 1)
	s, _, _ := newCrawlJobServerWith(t, "docs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case inFlight <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))

	_, started := callJSON(t, s.handleCrawlSite, map[string]any{"site_key": "docs"})
	require.Equal(t, "started", started["status"])
	select {
	case <-inFlight:
	case <-time.After(time.Minute):
		t.Fatal("crawl never requested a page")
	}

	require.NoError(t, s.Shutdown(context.Background()))

	s.jobManager.mu.RLock()
	defer s.jobManager.mu.RUnlock()
	assert.Empty(t, s.jobManager.workers, "Shutdown returned while a crawl was still running")
}

func TestRunCrawlJob_ReportsCountsAndFailures(t *testing.T) {
	const siteKey = "docs"
	s, siteCfg, _ := newCrawlJobServerWith(t, siteKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docs/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><title>Doc</title></head><body><main id="c"><p>X</p><a href="/docs/gone.html">g</a></main></body></html>`)
	}))
	job, err := s.jobManager.CreateJob(siteKey, false)
	require.NoError(t, err)
	s.runCrawlJob(job, siteCfg, siteKey, false)

	_, got := callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": job.ID})
	assert.Equal(t, string(JobStatusCompleted), got["status"])
	assert.EqualValues(t, 1, got["pages_saved"])
	assert.EqualValues(t, 1, got["pages_failed"])
	assert.EqualValues(t, 0, got["pages_carried_forward"])
	failures, ok := got["failures"].([]any)
	require.True(t, ok, "failures list present: %v", got)
	require.Len(t, failures, 1)
	assert.Equal(t, "gone", failures[0].(map[string]any)["class"])
	assert.NotContains(t, got, "refused")
}

func TestRunCrawlJob_RefusedJob(t *testing.T) {
	const siteKey = "docs"
	s, siteCfg, _ := newCrawlJobServerWith(t, siteKey, http.NotFoundHandler())
	job, err := s.jobManager.CreateJob(siteKey, false)
	require.NoError(t, err)
	s.runCrawlJob(job, siteCfg, siteKey, false)

	_, got := callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": job.ID})
	assert.Equal(t, string(JobStatusFailed), got["status"])
	assert.Equal(t, "empty", got["refused"])
	assert.Contains(t, got["error_message"], "crawl kept back")
	assert.Contains(t, got["error_message"], "crawl_site with incremental:true")
}

func TestRunCrawlJob_AllowShrinkPublishesTransientLoss(t *testing.T) {
	const siteKey = "docs"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docs/index.html" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><title>Doc</title></head><body><main id="c"><p>X</p>`+
			`<a href="/docs/a.html">a</a><a href="/docs/b.html">b</a><a href="/docs/c.html">c</a></main></body></html>`)
	})
	for _, tc := range []struct {
		allowShrink bool
		want        JobStatus
	}{{false, JobStatusFailed}, {true, JobStatusCompleted}} {
		s, siteCfg, _ := newCrawlJobServerWith(t, siteKey, handler)
		job, err := s.jobManager.CreateJob(siteKey, false)
		require.NoError(t, err)
		s.runCrawlJob(job, siteCfg, siteKey, tc.allowShrink)

		_, got := callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": job.ID})
		assert.Equal(t, string(tc.want), got["status"], "allow_shrink=%v: %v", tc.allowShrink, got["error_message"])
	}
}
