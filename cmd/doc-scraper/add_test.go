package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func addFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	page := `<!DOCTYPE html><html><head>
<title>Guide - VitePress Demo</title>
<meta name="generator" content="VitePress v1.6.4">
</head><body>
<div id="VPContent"><main class="main"><div class="vp-doc">
<h1>Guide</h1><h2>Install</h2>
<p>` + strings.Repeat("Real documentation body text with plenty of length for validation. ", 6) + `</p>
<pre><code>npm i vitepress</code></pre>
</div></main></div></body></html>`
	mux.HandleFunc("/guide/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, page)
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><urlset>`)
		for i := range 12 {
			fmt.Fprintf(w, "<url><loc>%s/guide/p%d/</loc></url>", srv.URL, i)
		}
		fmt.Fprint(w, `</urlset>`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func addFixtureConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `# test config
http_client_settings:
  allow_private_networks: true
sites:
  existing:
    start_urls: ["https://old.example.com/docs/"]
    allowed_domain: "old.example.com"
    content_selector: "main"
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestDoAdd_DryRun(t *testing.T) {
	srv := addFixtureServer(t)
	cfgPath := addFixtureConfig(t)
	var stdout, stderr bytes.Buffer

	code := doAdd(addOptions{
		configPath: cfgPath, rawURL: srv.URL + "/guide/", dryRun: true,
	}, strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, addExitDrafted, code, stderr.String())
	out := stdout.String()
	assert.Contains(t, out, "Detected: vitepress")
	assert.Contains(t, out, "confidence high")
	assert.Contains(t, out, ".vp-doc")
	assert.Contains(t, out, "npm i vitepress", "preview shows the extracted code block")
	assert.Contains(t, out, "code blocks 1/1")
	assert.Contains(t, out, "nothing written")

	data, _ := os.ReadFile(cfgPath)
	assert.NotContains(t, string(data), "vp-doc", "dry run must not touch the config")
}

func TestDoAdd_YesWrites(t *testing.T) {
	srv := addFixtureServer(t)
	cfgPath := addFixtureConfig(t)
	var stdout, stderr bytes.Buffer

	code := doAdd(addOptions{
		configPath: cfgPath, rawURL: srv.URL + "/guide/", yes: true, siteKey: "vp_demo", depth: 2,
	}, strings.NewReader(""), &stdout, &stderr)

	require.Equal(t, addExitWritten, code, stderr.String())
	assert.Contains(t, stdout.String(), "doc-scraper crawl -config")

	cfg, err := loadConfig(cfgPath)
	require.NoError(t, err)
	require.Len(t, cfg.Sites, 2)
	added := cfg.Sites["vp_demo"]
	require.NotNil(t, added)
	assert.Equal(t, "/guide/", added.AllowedPathPrefix)
	assert.Equal(t, 2, added.MaxDepth, "-depth overrides the proposal")
	assert.Contains(t, added.ContentSelector, ".vp-doc", "resolved selector written, not \"auto\"")
	assert.NotNil(t, cfg.Sites["existing"], "existing site untouched")
}

func TestDoAdd_InteractiveDecline(t *testing.T) {
	srv := addFixtureServer(t)
	cfgPath := addFixtureConfig(t)
	var stdout, stderr bytes.Buffer

	code := doAdd(addOptions{
		configPath: cfgPath, rawURL: srv.URL + "/guide/", isTTY: true,
	}, strings.NewReader("n\n"), &stdout, &stderr)

	assert.Equal(t, addExitWritten, code)
	assert.Contains(t, stdout.String(), "Not written.")
	cfg, err := loadConfig(cfgPath)
	require.NoError(t, err)
	assert.Len(t, cfg.Sites, 1)
}

func TestDoAdd_InteractiveAccept(t *testing.T) {
	srv := addFixtureServer(t)
	cfgPath := addFixtureConfig(t)
	var stdout, stderr bytes.Buffer

	code := doAdd(addOptions{
		configPath: cfgPath, rawURL: srv.URL + "/guide/", isTTY: true,
	}, strings.NewReader("y\n"), &stdout, &stderr)

	require.Equal(t, addExitWritten, code, stderr.String())
	cfg, err := loadConfig(cfgPath)
	require.NoError(t, err)
	assert.Len(t, cfg.Sites, 2)
}

func TestDoAdd_NoTTYFailsFast(t *testing.T) {
	srv := addFixtureServer(t)
	cfgPath := addFixtureConfig(t)
	var stdout, stderr bytes.Buffer

	code := doAdd(addOptions{
		configPath: cfgPath, rawURL: srv.URL + "/guide/", isTTY: false,
	}, strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, addExitError, code)
	assert.Contains(t, stderr.String(), "-yes")
	assert.Contains(t, stderr.String(), "-dry-run")
}

func TestDoAdd_JSONDryRun(t *testing.T) {
	srv := addFixtureServer(t)
	cfgPath := addFixtureConfig(t)
	var stdout, stderr bytes.Buffer

	code := doAdd(addOptions{
		configPath: cfgPath, rawURL: srv.URL + "/guide/", dryRun: true, jsonOut: true,
	}, strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, addExitDrafted, code, stderr.String())
	var result addJSONResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result), "stdout must be pure JSON")
	assert.Equal(t, "vitepress", result.Framework)
	assert.Equal(t, "high", result.Confidence)
	assert.False(t, result.Written)
	assert.Equal(t, 12, result.PageCount)
	assert.Contains(t, result.Config.ContentSelector, ".vp-doc")
	assert.Equal(t, 1, result.Preview.CodeBlocksKept)
}

func TestDoAdd_DuplicateKeyRejected(t *testing.T) {
	srv := addFixtureServer(t)
	cfgPath := addFixtureConfig(t)
	var stdout, stderr bytes.Buffer

	code := doAdd(addOptions{
		configPath: cfgPath, rawURL: srv.URL + "/guide/", yes: true, siteKey: "existing",
	}, strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, addExitError, code)
	assert.Contains(t, stderr.String(), "already exists")
}

func TestDoAdd_MissingConfigCreated(t *testing.T) {
	srv := addFixtureServer(t)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	var stdout, stderr bytes.Buffer

	// A fresh config has no allow_private_networks, so the SSRF guard would
	// block the loopback test server; write a minimal one to keep that on.
	require.NoError(t, os.WriteFile(cfgPath, []byte("http_client_settings:\n  allow_private_networks: true\n"), 0o644))
	code := doAdd(addOptions{
		configPath: cfgPath, rawURL: srv.URL + "/guide/", yes: true,
	}, strings.NewReader(""), &stdout, &stderr)

	require.Equal(t, addExitWritten, code, stderr.String())
	cfg, err := loadConfig(cfgPath)
	require.NoError(t, err)
	assert.Len(t, cfg.Sites, 1)
}

func TestDoAdd_SiteConfigValidates(t *testing.T) {
	srv := addFixtureServer(t)
	cfgPath := addFixtureConfig(t)
	var stdout, stderr bytes.Buffer

	code := doAdd(addOptions{
		configPath: cfgPath, rawURL: srv.URL + "/guide/", yes: true,
	}, strings.NewReader(""), &stdout, &stderr)
	require.Equal(t, addExitWritten, code, stderr.String())

	cfg, err := loadConfig(cfgPath)
	require.NoError(t, err)
	for key, site := range cfg.Sites {
		if key == "existing" {
			continue
		}
		_, verr := site.Validate()
		assert.NoError(t, verr, "written entry passes SiteConfig.Validate")
	}
}
