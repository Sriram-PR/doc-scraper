package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
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

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func TestDoValidate_NoSitesIsInvalid(t *testing.T) {
	for name, content := range map[string]string{
		"empty file":    "",
		"no sites key":  "num_workers: 2\n",
		"empty mapping": "sites: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfgPath := writeTempConfig(t, content)

			var stdout, stderr bytes.Buffer
			assert.Equal(t, 1, doValidate(cfgPath, "", false, &stdout, &stderr))
			assert.Contains(t, stderr.String(), "no sites configured")
			assert.NotContains(t, stdout.String(), "Configuration valid")

			stdout.Reset()
			assert.Equal(t, 1, doValidate(cfgPath, "", true, &stdout, new(bytes.Buffer)))
			var payload map[string]any
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
			assert.Equal(t, false, payload["valid"])
			assert.Contains(t, payload["errors"], "no sites configured")
		})
	}
}

func TestDoListSites_NoSites(t *testing.T) {
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, doListSites(writeTempConfig(t, "sites: {}\n"), false, &stdout, &stderr))
	assert.Contains(t, stdout.String(), "no sites configured")
}

func TestDoValidate_UnknownKeysWarn(t *testing.T) {
	cfgPath := writeTempConfig(t, `
num_workerz: 9
sites:
  a:
    start_urls: ["http://a.com"]
    allowed_domain: "a.com"
    content_selector: "main"
    max_dept: 2
`)
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, doValidate(cfgPath, "", false, &stdout, &stderr))
	assert.Contains(t, stdout.String(), `WARN: line 2: unknown key "num_workerz"`)
	assert.Contains(t, stdout.String(), `unknown key "sites.a.max_dept"`)

	stdout.Reset()
	assert.Equal(t, 0, doValidate(cfgPath, "", true, &stdout, &stderr))
	var payload struct {
		Valid    bool     `json:"valid"`
		Warnings []string `json:"global_warnings"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
	assert.True(t, payload.Valid)
	assert.Contains(t, strings.Join(payload.Warnings, "\n"), `unknown key "num_workerz"`)
}

func TestLoadConfigForAdd_NoSitesStillWorks(t *testing.T) {
	for _, content := range []string{"", "sites: {}\n", "num_workers: 2\n"} {
		cfg, err := loadConfigForAdd(writeTempConfig(t, content))
		require.NoError(t, err)
		assert.Empty(t, cfg.Sites)
	}
}

func TestDoMcpServer_NoSitesRefused(t *testing.T) {
	var stderr bytes.Buffer
	assert.Equal(t, 1, doMcpServer(writeTempConfig(t, "sites: {}\n"), "info", nil, &stderr))
	assert.Contains(t, stderr.String(), "no sites configured")
}

func TestExtraArgsError(t *testing.T) {
	fs := flag.NewFlagSet("crawl", flag.ContinueOnError)
	fs.String("site", "", "")
	require.NoError(t, fs.Parse([]string{"-site", "x"}))
	assert.NoError(t, extraArgsError(fs))

	fs = flag.NewFlagSet("crawl", flag.ContinueOnError)
	fs.String("site", "", "")
	require.NoError(t, fs.Parse([]string{"-site", "x", "extra", "more"}))
	assert.ErrorContains(t, extraArgsError(fs), "extra more")
}

func TestDoValidate_DisallowedPathPatterns(t *testing.T) {
	content := `
sites:
  good:
    start_urls: ["https://example.com"]
    allowed_domain: "example.com"
    content_selector: "main"
    disallowed_path_patterns: ["^/private/"]
  bad:
    start_urls: ["https://example.com"]
    allowed_domain: "example.com"
    content_selector: "main"
    disallowed_path_patterns: ["([unclosed"]
`
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0600))

	tests := []struct {
		name     string
		site     string
		jsonOut  bool
		wantCode int
	}{
		{"all text", "", false, 1},
		{"all json", "", true, 1},
		{"bad text", "bad", false, 1},
		{"bad json", "bad", true, 1},
		{"good text", "good", false, 0},
		{"good json", "good", true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := doValidate(cfgPath, tt.site, tt.jsonOut, &stdout, &stderr)
			assert.Equal(t, tt.wantCode, code)

			if tt.jsonOut {
				var result struct {
					Valid bool `json:"valid"`
					Sites []struct {
						Key   string `json:"key"`
						Valid bool   `json:"valid"`
						Error string `json:"error"`
					} `json:"sites"`
				}
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
				assert.Empty(t, stderr.String())
				assert.Equal(t, tt.wantCode == 0, result.Valid)

				wantKeys := []string{tt.site}
				if tt.site == "" {
					wantKeys = []string{"bad", "good"}
				}
				gotKeys := make([]string, 0, len(result.Sites))
				for _, site := range result.Sites {
					gotKeys = append(gotKeys, site.Key)
					assert.Equal(t, site.Key == "good", site.Valid)
					if site.Key == "bad" {
						assert.Contains(t, site.Error, "invalid regex pattern #1")
						assert.Contains(t, site.Error, "([unclosed")
					} else {
						assert.Empty(t, site.Error)
					}
				}
				assert.ElementsMatch(t, wantKeys, gotKeys)
				return
			}

			if tt.wantCode == 1 {
				assert.Contains(t, stderr.String(), "ERROR: [bad]")
				assert.Contains(t, stderr.String(), "invalid regex pattern #1")
				assert.Contains(t, stderr.String(), "([unclosed")
				assert.NotContains(t, stdout.String(), "Configuration valid.")
				if tt.site == "" {
					assert.Contains(t, stdout.String(), "OK: [good]")
				}
			} else {
				assert.Empty(t, stderr.String())
				assert.Contains(t, stdout.String(), "OK: Site 'good'")
				assert.Contains(t, stdout.String(), "Configuration valid.")
			}
		})
	}
}
