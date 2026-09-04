package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func writeSearchFixture(t *testing.T) (cfgPath string) {
	t.Helper()
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	cfgPath = filepath.Join(tmpDir, "config.yaml")
	// Single-quoted YAML plus forward slashes: a double-quoted Windows path
	// like D:\a\... is parsed as YAML escape sequences and breaks loading.
	content := `
state_dir: '` + filepath.ToSlash(stateDir) + `'
output_base_dir: '` + filepath.ToSlash(filepath.Join(tmpDir, "out")) + `'
sites:
  demo:
    start_urls: ["https://demo.example.com/docs/"]
    allowed_domain: "demo.example.com"
    content_selector: "main"
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o644))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	idx, err := index.OpenAt(stateDir, 5, log)
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()
	md := "# Retries\n\n## Backoff\n\nExponential backoff doubles the ocelot delay each attempt. " + strings.Repeat("filler ", 40)
	require.NoError(t, idx.ReplaceChunks(context.Background(), "demo", "https://demo.example.com/docs/retries", "Retries", "h1", chunk.Split(md)))
	return cfgPath
}

func TestDoSearch_HumanOutput(t *testing.T) {
	cfgPath := writeSearchFixture(t)
	var stdout, stderr bytes.Buffer
	code := doSearch(cfgPath, "ocelot backoff", "", 10, false, &stdout, &stderr)
	assert.Equal(t, 0, code, stderr.String())
	out := stdout.String()
	assert.Contains(t, out, "https://demo.example.com/docs/retries#backoff", "result links to the section anchor")
	assert.Contains(t, out, "(demo)")
	assert.Contains(t, out, "[ocelot]", "snippet marks match terms")
}

func TestDoSearch_JSONOutput(t *testing.T) {
	cfgPath := writeSearchFixture(t)
	var stdout, stderr bytes.Buffer
	code := doSearch(cfgPath, "ocelot", "demo", 5, true, &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	var results []index.SearchResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &results))
	require.Len(t, results, 1)
	assert.Equal(t, "backoff", results[0].Anchor)
}

func TestDoSearch_NoMatchesAndErrors(t *testing.T) {
	cfgPath := writeSearchFixture(t)
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, doSearch(cfgPath, "wombatless", "", 5, false, &stdout, &stderr))
	assert.Contains(t, stdout.String(), "No matches")

	stderr.Reset()
	assert.Equal(t, 1, doSearch(cfgPath, "x", "nope", 5, false, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "not found in config")

	stderr.Reset()
	assert.Equal(t, 1, doSearch(filepath.Join(t.TempDir(), "missing.yaml"), "x", "", 5, false, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "read config")
}
