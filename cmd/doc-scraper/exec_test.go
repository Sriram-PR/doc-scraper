package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	pkglog "github.com/Sriram-PR/doc-scraper/v2/pkg/log"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/taskspec"
)

func hostOnly(srvURL string) string {
	u, _ := url.Parse(srvURL)
	return u.Hostname()
}

func crawlFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	page := func(title, body, links string) string {
		return fmt.Sprintf(`<!DOCTYPE html><html><head><title>%s</title></head>
<body><nav><a href="/">home</a></nav><main><h1>%s</h1><p>%s</p>%s</main></body></html>`, title, title, body, links)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimSuffix(r.URL.Path, "/") {
		case "":
			fmt.Fprint(w, page("Home", "Welcome to the test docs corpus with enough words to matter.",
				`<a href="/guide/">guide</a> <a href="/api/">api</a>`))
		case "/guide":
			fmt.Fprint(w, page("Guide", "The guide page explains everything about the fixture site.", ""))
		case "/api":
			fmt.Fprint(w, page("API", "The api page lists the fixture endpoints in loving detail.", ""))
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeCrawlConfig(t *testing.T, srvURL string, extraSites string) (cfgPath, outDir, stateDir string) {
	t.Helper()
	tmp := t.TempDir()
	outDir = filepath.Join(tmp, "out")
	stateDir = filepath.Join(tmp, "state")
	cfgPath = filepath.Join(tmp, "config.yaml")
	host := hostOnly(srvURL)
	content := fmt.Sprintf(`
state_dir: '%s'
output_base_dir: '%s'
num_workers: 2
max_retries: 1
initial_retry_delay: 10ms
max_retry_delay: 20ms
enable_jsonl_output: true
http_client_settings:
  allow_private_networks: true
sites:
  fixture:
    start_urls: ["%s/"]
    allowed_domain: "%s"
    content_selector: "main"
    max_depth: 2
%s`, filepath.ToSlash(stateDir), filepath.ToSlash(outDir), srvURL, host, extraSites)
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o644))
	return cfgPath, outDir, stateDir
}

func countFiles(t *testing.T, root, pattern string) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if ok, _ := filepath.Match(pattern, filepath.Base(path)); ok {
				n++
			}
		}
		return nil
	})
	return n
}

func TestExecuteCrawl_EndToEnd(t *testing.T) {
	srv := crawlFixtureServer(t)
	cfgPath, outDir, stateDir := writeCrawlConfig(t, srv.URL, "")

	code := executeCrawl(cfgPath, "fixture", "error", pkglog.FormatText, "", false, false, false)
	require.Equal(t, 0, code)

	assert.GreaterOrEqual(t, countFiles(t, outDir, "*.md"), 3, "all three pages saved as markdown")
	assert.GreaterOrEqual(t, countFiles(t, outDir, "*.jsonl"), 1, "JSONL corpus written")
	_, err := os.Stat(filepath.Join(stateDir, "index.db"))
	require.NoError(t, err, "crawl-history index created")

	code = executeCrawl(cfgPath, "fixture", "error", pkglog.FormatText, "", true, true, false)
	assert.Equal(t, 0, code, "incremental follow-up crawl succeeds")
}

func TestExecuteCrawl_UnreachableSiteFails(t *testing.T) {
	srv := crawlFixtureServer(t)
	cfgPath, _, _ := writeCrawlConfig(t, srv.URL, "")
	srv.Close()

	// Individual page failures are non-fatal, but a crawl where every
	// attempted page failed must not exit 0 with an empty corpus.
	code := executeCrawl(cfgPath, "fixture", "error", pkglog.FormatText, "", false, false, false)
	assert.Equal(t, 1, code)
}

func TestExecuteParallelCrawl_EndToEnd(t *testing.T) {
	srv := crawlFixtureServer(t)
	extra := fmt.Sprintf(`  second:
    start_urls: ["%s/guide/"]
    allowed_domain: "%s"
    content_selector: "main"
    max_depth: 1
`, srv.URL, hostOnly(srv.URL))
	cfgPath, outDir, _ := writeCrawlConfig(t, srv.URL, extra)

	code := executeParallelCrawl(cfgPath, []string{"fixture", "second"}, false, "error", pkglog.FormatText, "", false, false, false)
	require.Equal(t, 0, code)
	assert.DirExists(t, filepath.Join(outDir, "fixture"))
	assert.DirExists(t, filepath.Join(outDir, "second"))
}

func TestDispatchTaskSpec_Crawl(t *testing.T) {
	srv := crawlFixtureServer(t)
	cfgPath, outDir, _ := writeCrawlConfig(t, srv.URL, "")

	spec := &taskspec.TaskSpec{Command: taskspec.CommandCrawl, Config: cfgPath, Site: "fixture", Loglevel: "error"}
	require.NoError(t, spec.Validate())
	code := dispatchTaskSpec(spec)
	assert.Equal(t, 0, code)
	assert.DirExists(t, filepath.Join(outDir, "fixture"))
}

func TestResolveSiteKeys(t *testing.T) {
	keys, warning, ok := resolveSiteKeys("a", "", false)
	assert.True(t, ok)
	assert.Equal(t, []string{"a"}, keys)
	assert.Empty(t, warning)

	keys, warning, ok = resolveSiteKeys("a", "b, c ,", false)
	assert.True(t, ok)
	assert.Equal(t, []string{"b", "c"}, keys)
	assert.Contains(t, warning, "-sites")

	keys, warning, ok = resolveSiteKeys("a", "", true)
	assert.True(t, ok)
	assert.Nil(t, keys)
	assert.Contains(t, warning, "all-sites")

	_, _, ok = resolveSiteKeys("", "", false)
	assert.False(t, ok)
}

func TestLogHelpers(t *testing.T) {
	assert.Equal(t, pkglog.FormatJSON, logFormatFor(true))
	assert.Equal(t, pkglog.FormatText, logFormatFor(false))

	log := setupLogger("debug", pkglog.FormatText)
	assert.True(t, log.Enabled(t.Context(), slog.LevelDebug))
	log = setupLogger("not-a-level", pkglog.FormatText)
	assert.False(t, log.Enabled(t.Context(), slog.LevelDebug), "invalid level falls back to info")

	logAppConfig(&config.AppConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestApplyIncrementalOverride(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &config.AppConfig{}
	applyIncrementalOverride(cfg, true, false, log)
	assert.True(t, cfg.EnableIncremental)

	cfg = &config.AppConfig{EnableIncremental: true}
	applyIncrementalOverride(cfg, true, true, log)
	assert.False(t, cfg.EnableIncremental, "-full wins over -incremental")
}

func TestLoadAndValidateConfig(t *testing.T) {
	srv := crawlFixtureServer(t)
	cfgPath, _, _ := writeCrawlConfig(t, srv.URL, "")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := loadAndValidateConfig(cfgPath, log)
	require.NotNil(t, cfg)
	assert.Equal(t, 2, cfg.NumWorkers)
	assert.NotEmpty(t, cfg.OutputBaseDir)

	validateSiteConfigs(cfg, []string{"fixture"}, log)
}
