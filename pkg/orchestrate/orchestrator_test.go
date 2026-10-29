package orchestrate

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/crawler"
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
	o := NewOrchestrator(context.Background(), cfg, []string{"docs"}, false, silentLogger())

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
	o := NewOrchestrator(context.Background(), cfg, nil, false, silentLogger())

	results := o.Run()
	assert.Empty(t, results)
}

func TestRun_UnknownSiteReturnsErrorResultWithoutNetwork(t *testing.T) {
	cfg := minimalAppConfig(t)
	o := NewOrchestrator(context.Background(), cfg, []string{"does_not_exist"}, false, silentLogger())

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
	o := NewOrchestrator(context.Background(), cfg, []string{"does_not_exist"}, false, silentLogger())
	_ = o.Run()
	o.Cancel() // must not panic when called post-Run
}

func TestRun_DeadSiteIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	cfg := &config.AppConfig{
		StateDir:      t.TempDir(),
		OutputBaseDir: t.TempDir(),
		Sites: map[string]*config.SiteConfig{
			"fixture": {StartURLs: []string{srv.URL + "/"}, AllowedDomain: u.Hostname(), ContentSelector: "main"},
		},
		HTTPClientSettings: config.HTTPClientConfig{AllowPrivateNetworks: true},
	}
	_, err := cfg.Validate()
	require.NoError(t, err)

	results := NewOrchestrator(context.Background(), cfg, []string{"fixture"}, false, slog.New(slog.NewTextHandler(io.Discard, nil))).Run()

	require.Len(t, results, 1)
	assert.False(t, results[0].Success)
	assert.True(t, results[0].Refused)
	assert.EqualValues(t, 1, results[0].Failures.Count(crawler.FailureGone))
}
