package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_ValidFile(t *testing.T) {
	content := `
num_workers: 4
output_base_dir: "./out"
state_dir: "./state"
sites:
  test_site:
    start_urls: ["http://example.com"]
    allowed_domain: "example.com"
    content_selector: "main"
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0644))

	cfg, err := loadConfig(cfgPath)

	require.NoError(t, err)
	assert.Equal(t, 4, cfg.NumWorkers)
	assert.Contains(t, cfg.Sites, "test_site")
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := loadConfig("/nonexistent/path/config.yaml")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "read config")
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bad.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("{{invalid yaml"), 0644))

	_, err := loadConfig(cfgPath)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse config")
}

func TestDoValidate_AllSites(t *testing.T) {
	content := `
sites:
  site_a:
    start_urls: ["http://a.com"]
    allowed_domain: "a.com"
    content_selector: "main"
  site_b:
    start_urls: ["http://b.com"]
    allowed_domain: "b.com"
    content_selector: "article"
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0644))

	var stdout, stderr bytes.Buffer
	exitCode := doValidate(cfgPath, "", false, &stdout, &stderr)

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout.String(), "OK: [site_a]")
	assert.Contains(t, stdout.String(), "OK: [site_b]")
	assert.Contains(t, stdout.String(), "Configuration valid")
}

func TestDoValidate_SpecificSite(t *testing.T) {
	content := `
sites:
  my_site:
    start_urls: ["http://example.com"]
    allowed_domain: "example.com"
    content_selector: "div.content"
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0644))

	var stdout, stderr bytes.Buffer
	exitCode := doValidate(cfgPath, "my_site", false, &stdout, &stderr)

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout.String(), "OK: Site 'my_site'")
}

func TestDoValidate_SiteNotFound(t *testing.T) {
	content := `
sites:
  existing:
    start_urls: ["http://example.com"]
    allowed_domain: "example.com"
    content_selector: "main"
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0644))

	var stdout, stderr bytes.Buffer
	exitCode := doValidate(cfgPath, "nonexistent", false, &stdout, &stderr)

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr.String(), "not found")
}

func TestDoValidate_InvalidSite(t *testing.T) {
	content := `
sites:
  bad_site:
    start_urls: []
    allowed_domain: ""
    content_selector: ""
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0644))

	var stdout, stderr bytes.Buffer
	exitCode := doValidate(cfgPath, "bad_site", false, &stdout, &stderr)

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr.String(), "ERROR")
}

func TestDoValidate_ConfigNotFound(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := doValidate("/nonexistent.yaml", "", false, &stdout, &stderr)

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr.String(), "Error")
}

func TestDoListSites(t *testing.T) {
	content := `
sites:
  alpha:
    start_urls: ["http://alpha.com", "http://alpha.com/docs"]
    allowed_domain: "alpha.com"
    allowed_path_prefix: "/docs"
    content_selector: "main"
  beta:
    start_urls: ["http://beta.com"]
    allowed_domain: "beta.com"
    content_selector: "article"
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0644))

	var stdout, stderr bytes.Buffer
	exitCode := doListSites(cfgPath, false, &stdout, &stderr)

	assert.Equal(t, 0, exitCode)
	out := stdout.String()
	assert.Contains(t, out, "alpha")
	assert.Contains(t, out, "beta")
	assert.Contains(t, out, "Domain: alpha.com")
	assert.Contains(t, out, "Start URLs: 2")
	assert.Contains(t, out, "Path Prefix: /docs")
}

func TestDoListSites_ConfigNotFound(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := doListSites("/nonexistent.yaml", false, &stdout, &stderr)

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr.String(), "Error")
}

func TestDoValidate_JSONHappyPath(t *testing.T) {
	content := `
default_delay_per_host: 500ms
num_workers: 4
max_requests: 16
max_requests_per_host: 4
output_base_dir: "./out"
state_dir: "./state"
sites:
  alpha:
    start_urls: ["https://alpha.com/"]
    allowed_domain: "alpha.com"
    allowed_path_prefix: "/"
    content_selector: "body"
    max_depth: 1
  beta:
    start_urls: ["https://beta.com/"]
    allowed_domain: "beta.com"
    allowed_path_prefix: "/"
    content_selector: "body"
    max_depth: 1
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0644))

	var stdout, stderr bytes.Buffer
	exitCode := doValidate(cfgPath, "", true, &stdout, &stderr)
	assert.Equal(t, 0, exitCode)
	assert.Empty(t, stderr.String(), "JSON mode must not write to stderr on success")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
	assert.Equal(t, true, payload["valid"])
	assert.EqualValues(t, 2, payload["site_count"])
	sites, ok := payload["sites"].([]any)
	require.True(t, ok)
	require.Len(t, sites, 2)
	// Sites must be sorted alphabetically.
	assert.Equal(t, "alpha", sites[0].(map[string]any)["key"])
	assert.Equal(t, "beta", sites[1].(map[string]any)["key"])
}

func TestDoValidate_JSONConfigLoadFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := doValidate("/nonexistent.yaml", "", true, &stdout, &stderr)
	assert.Equal(t, 1, exitCode)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
	assert.Equal(t, false, payload["valid"])
	errors, ok := payload["errors"].([]any)
	require.True(t, ok)
	assert.NotEmpty(t, errors)
}

func TestDoListSites_JSONHappyPath(t *testing.T) {
	content := `
default_delay_per_host: 500ms
num_workers: 4
max_requests: 16
max_requests_per_host: 4
output_base_dir: "./out"
state_dir: "./state"
sites:
  alpha:
    start_urls: ["https://alpha.com/"]
    allowed_domain: "alpha.com"
    allowed_path_prefix: "/docs"
    content_selector: "body"
    max_depth: 1
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0644))

	var stdout, stderr bytes.Buffer
	exitCode := doListSites(cfgPath, true, &stdout, &stderr)
	assert.Equal(t, 0, exitCode)
	assert.Empty(t, stderr.String(), "JSON mode must not write to stderr on success")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
	assert.EqualValues(t, 1, payload["count"])
	sites := payload["sites"].([]any)
	require.Len(t, sites, 1)
	site := sites[0].(map[string]any)
	assert.Equal(t, "alpha", site["key"])
	assert.Equal(t, "alpha.com", site["domain"])
	assert.Equal(t, "/docs", site["path_prefix"])
	assert.EqualValues(t, 1, site["start_urls_count"])
}

func TestPrintUsageTo(t *testing.T) {
	var buf bytes.Buffer
	printUsageTo(&buf)

	out := buf.String()
	assert.Contains(t, out, "crawl")
	assert.Contains(t, out, "--resume")
	assert.Contains(t, out, "config")
	assert.Contains(t, out, "mcp-server")
	assert.Contains(t, out, "version")
	assert.Contains(t, out, "run")
}

func TestPrintRunUsage(t *testing.T) {
	var buf bytes.Buffer
	printRunUsage(&buf)

	out := buf.String()
	assert.Contains(t, out, "stdin")
	assert.Contains(t, out, "\"command\":")
	assert.Contains(t, out, "site")
	assert.Contains(t, out, "all_sites")
	assert.Contains(t, out, "interval")
}
