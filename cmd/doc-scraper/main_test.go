package main

import (
	"bytes"
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
	exitCode := doValidate(cfgPath, "", &stdout, &stderr)

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
	exitCode := doValidate(cfgPath, "my_site", &stdout, &stderr)

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
	exitCode := doValidate(cfgPath, "nonexistent", &stdout, &stderr)

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
	exitCode := doValidate(cfgPath, "bad_site", &stdout, &stderr)

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr.String(), "ERROR")
}

func TestDoValidate_ConfigNotFound(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := doValidate("/nonexistent.yaml", "", &stdout, &stderr)

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
	exitCode := doListSites(cfgPath, &stdout, &stderr)

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
	exitCode := doListSites("/nonexistent.yaml", &stdout, &stderr)

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr.String(), "Error")
}

func TestPrintUsageTo(t *testing.T) {
	var buf bytes.Buffer
	printUsageTo(&buf)

	out := buf.String()
	assert.Contains(t, out, "crawl")
	assert.Contains(t, out, "resume")
	assert.Contains(t, out, "validate")
	assert.Contains(t, out, "list-sites")
	assert.Contains(t, out, "version")
}
