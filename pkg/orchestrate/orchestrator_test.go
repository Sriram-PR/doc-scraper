package orchestrate

import (
	"io"
	"testing"
	"time"

	"log/slog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil)).With("component", "orchestrate_test")
}

func minimalAppConfig(t *testing.T) *config.AppConfig {
	t.Helper()
	return &config.AppConfig{
		StateDir:           t.TempDir(),
		OutputBaseDir:      t.TempDir(),
		NumWorkers:         1,
		NumImageWorkers:    1,
		MaxRequests:        4,
		MaxRequestsPerHost: 2,
		HTTPClientSettings: config.HTTPClientConfig{Timeout: 5 * time.Second},
		Sites: map[string]*config.SiteConfig{
			"docs": {
				StartURLs:       []string{"https://example.com/"},
				AllowedDomain:   "example.com",
				ContentSelector: "body",
				MaxDepth:        1,
			},
		},
	}
}

func TestNewOrchestrator_SetsFields(t *testing.T) {
	cfg := minimalAppConfig(t)
	o := NewOrchestrator(cfg, []string{"docs"}, false, silentLogger())

	require.NotNil(t, o)
	assert.Equal(t, []string{"docs"}, o.siteKeys)
	assert.False(t, o.resume)
	assert.NotNil(t, o.ctx)
	assert.NotNil(t, o.cancel)
	assert.NotNil(t, o.globalSemaphore)
	assert.Empty(t, o.results)
}

func TestRun_EmptySiteList(t *testing.T) {
	cfg := minimalAppConfig(t)
	o := NewOrchestrator(cfg, nil, false, silentLogger())

	results := o.Run()
	assert.Empty(t, results)
}

func TestRun_UnknownSiteReturnsErrorResultWithoutNetwork(t *testing.T) {
	cfg := minimalAppConfig(t)
	o := NewOrchestrator(cfg, []string{"does_not_exist"}, false, silentLogger())

	results := o.Run()

	require.Len(t, results, 1)
	r := results[0]
	assert.Equal(t, "does_not_exist", r.SiteKey)
	assert.False(t, r.Success)
	require.Error(t, r.Error)
	assert.Contains(t, r.Error.Error(), "does_not_exist")
}

func TestCancel_AfterRunIsSafe(t *testing.T) {
	cfg := minimalAppConfig(t)
	o := NewOrchestrator(cfg, []string{"does_not_exist"}, false, silentLogger())
	_ = o.Run()
	o.Cancel() // must not panic when called post-Run
}

func TestGetProgress_MatchesResults(t *testing.T) {
	cfg := minimalAppConfig(t)
	o := NewOrchestrator(cfg, []string{"does_not_exist"}, false, silentLogger())
	_ = o.Run()

	progress := o.GetProgress()
	require.Len(t, progress, 1)
	assert.Equal(t, "does_not_exist", progress[0].SiteKey)
	assert.False(t, progress[0].IsRunning)
}
