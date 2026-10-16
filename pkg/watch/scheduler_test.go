package watch

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

func mustHostname(srvURL string) string {
	u, _ := url.Parse(srvURL)
	return u.Hostname()
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
}

func schedulerConfig(t *testing.T, srvURL string) *config.AppConfig {
	t.Helper()
	tmp := t.TempDir()
	cfg := &config.AppConfig{
		StateDir:      filepath.Join(tmp, "state"),
		OutputBaseDir: filepath.Join(tmp, "out"),
		Sites: map[string]*config.SiteConfig{
			"fixture": {
				StartURLs:       []string{srvURL + "/"},
				AllowedDomain:   mustHostname(srvURL),
				ContentSelector: "main",
				MaxDepth:        1,
			},
		},
		HTTPClientSettings: config.HTTPClientConfig{AllowPrivateNetworks: true},
		EnableIncremental:  true,
	}
	_, err := cfg.Validate()
	require.NoError(t, err)
	return cfg
}

func TestCalculateTickInterval(t *testing.T) {
	for interval, want := range map[time.Duration]time.Duration{
		30 * time.Minute:  3 * time.Minute,
		5 * time.Minute:   time.Minute,
		24 * time.Hour:    10 * time.Minute,
		100 * time.Minute: 10 * time.Minute,
	} {
		s := &Scheduler{interval: interval}
		assert.Equal(t, want, s.calculateTickInterval(), interval.String())
	}
}

func TestGetDueSites(t *testing.T) {
	cfg := &config.AppConfig{StateDir: t.TempDir()}
	s := NewScheduler(cfg, []string{"fresh", "recent", "stale"}, time.Hour, discardLog())

	s.stateManager.UpdateSiteState("recent", true, PageCounts{Saved: 5}, "")
	s.stateManager.UpdateSiteState("stale", true, PageCounts{Saved: 5}, "")
	st, ok := s.stateManager.GetSiteState("stale")
	require.True(t, ok)
	st.LastRunTime = time.Now().Add(-2 * time.Hour)
	s.stateManager.state.Sites["stale"] = st

	due := s.getDueSites()
	assert.ElementsMatch(t, []string{"fresh", "stale"}, due, "never-run and overdue sites are due; recent is not")
}

func TestSchedulerRunAndStop(t *testing.T) {
	cfg := &config.AppConfig{StateDir: t.TempDir()}
	s := NewScheduler(cfg, []string{"quiet"}, time.Hour, discardLog()).WithIndex(nil)
	s.stateManager.UpdateSiteState("quiet", true, PageCounts{Saved: 1}, "")

	done := make(chan error, 1)
	go func() { done <- s.Run() }()
	time.Sleep(100 * time.Millisecond)
	s.Stop()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop")
	}
}

func TestRunDueSites_CrawlsAndPersistsState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>W</title></head><body><main><h1>Watched</h1><p>Content for the watch scheduler crawl test.</p></main></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := schedulerConfig(t, srv.URL)
	s := NewScheduler(cfg, []string{"fixture"}, time.Hour, discardLog())
	require.NoError(t, s.stateManager.Load())

	s.runDueSites()
	s.wg.Wait()

	state, ok := s.stateManager.GetSiteState("fixture")
	require.True(t, ok, "site state recorded after scheduled crawl")
	assert.True(t, state.LastRunSuccess)
	assert.Positive(t, state.PagesSaved)

	_, err := os.Stat(filepath.Join(cfg.StateDir, stateFileName))
	require.NoError(t, err, "watch state persisted to disk")

	assert.Empty(t, s.getDueSites(), "freshly crawled site is no longer due")
}

func TestRunDueSites_DeadSiteRecordedAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	s := NewScheduler(schedulerConfig(t, srv.URL), []string{"fixture"}, time.Hour, discardLog())
	require.NoError(t, s.stateManager.Load())

	s.runDueSites()
	s.wg.Wait()

	state, ok := s.stateManager.GetSiteState("fixture")
	require.True(t, ok)
	assert.False(t, state.LastRunSuccess, "nothing could be fetched")
	assert.Contains(t, state.ErrorMessage, "zero successful pages")
	assert.Zero(t, state.PagesSaved)
	assert.EqualValues(t, 1, state.PagesFailed)
}

// TestRunDueSites_ShutdownIsNotRecorded: a crawl cut short by watch stopping
// is not a site failure. Recording it would schedule the next run a full
// interval out and leave a first crawl's partial corpus in place until then.
func TestRunDueSites_ShutdownIsNotRecorded(t *testing.T) {
	started := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	quick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Q</title></head><body><main><p>Quick page.</p></main></body></html>`)
	}))
	t.Cleanup(quick.Close)

	cfg := schedulerConfig(t, srv.URL)
	cfg.Sites["quick"] = &config.SiteConfig{
		StartURLs:       []string{quick.URL + "/"},
		AllowedDomain:   mustHostname(quick.URL),
		ContentSelector: "main",
		MaxDepth:        1,
	}
	s := NewScheduler(cfg, []string{"fixture", "quick"}, time.Hour, discardLog())
	require.NoError(t, s.stateManager.Load())

	s.runDueSites()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("crawl never fetched the start page")
	}
	// Let the quick site finish before stopping, so only fixture is interrupted.
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(cfg.OutputBaseDir, "quick", "pages.jsonl"))
		return err == nil && strings.Contains(string(b), `"crawl_meta"`)
	}, 10*time.Second, 10*time.Millisecond)
	s.Stop()
	s.wg.Wait()

	_, ok := s.stateManager.GetSiteState("fixture")
	assert.False(t, ok, "an interrupted run leaves no state, so the next start runs the site at once")
	quickState, ok := s.stateManager.GetSiteState("quick")
	require.True(t, ok, "a site that finished before the shutdown is still recorded")
	assert.True(t, quickState.LastRunSuccess)
	assert.Equal(t, []string{"fixture"}, s.getDueSites())
}

func TestRunDueSites_RecordsUnchangedPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>W</title></head><body><main><h1>Watched</h1><p>Content for the watch scheduler crawl test.</p></main></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := schedulerConfig(t, srv.URL)
	cfg.EnableIncremental = true
	s := NewScheduler(cfg, []string{"fixture"}, time.Hour, discardLog())
	require.NoError(t, s.stateManager.Load())

	s.runDueSites()
	s.wg.Wait()
	s.interval = 0 // due again immediately
	s.runDueSites()
	s.wg.Wait()

	state, ok := s.stateManager.GetSiteState("fixture")
	require.True(t, ok)
	assert.EqualValues(t, 0, state.PagesSaved, "second run changed nothing")
	assert.EqualValues(t, 1, state.PagesUnchanged, "second run re-checked the page")
}

func TestLogScheduleAndNextRun(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := &config.AppConfig{StateDir: t.TempDir()}
	s := NewScheduler(cfg, []string{"seen", "unseen"}, time.Hour, log)
	s.stateManager.UpdateSiteState("seen", false, PageCounts{Saved: 3}, "boom")

	s.logSchedule()
	out := buf.String()
	assert.Contains(t, out, "seen")
	assert.Contains(t, out, "failed")
	assert.Contains(t, out, "will run immediately")

	buf.Reset()
	s.logNextRun()
	assert.Contains(t, buf.String(), "Next crawl:")
}
