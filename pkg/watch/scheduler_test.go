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

	s.stateManager.UpdateSiteState("recent", true, 5, "")
	s.stateManager.UpdateSiteState("stale", true, 5, "")
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
	s.stateManager.UpdateSiteState("quiet", true, 1, "")

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
	assert.Positive(t, state.PagesProcessed)

	_, err := os.Stat(filepath.Join(cfg.StateDir, stateFileName))
	require.NoError(t, err, "watch state persisted to disk")

	assert.Empty(t, s.getDueSites(), "freshly crawled site is no longer due")
}

func TestLogScheduleAndNextRun(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := &config.AppConfig{StateDir: t.TempDir()}
	s := NewScheduler(cfg, []string{"seen", "unseen"}, time.Hour, log)
	s.stateManager.UpdateSiteState("seen", false, 3, "boom")

	s.logSchedule()
	out := buf.String()
	assert.Contains(t, out, "seen")
	assert.Contains(t, out, "failed")
	assert.Contains(t, out, "will run immediately")

	buf.Reset()
	s.logNextRun()
	assert.Contains(t, buf.String(), "Next crawl:")
}
