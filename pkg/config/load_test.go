package config

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	path := writeTempConfig(t, "num_workers: 3\nbogus_key: 1\nsites:\n  demo:\n    start_urls: [\"https://example.com/\"]\n    allowed_domain: example.com\n    content_selector: main\n")
	cfg, unknown, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.NumWorkers)
	require.Contains(t, cfg.Sites, "demo")
	assert.Equal(t, "main", cfg.Sites["demo"].ContentSelector)
	require.Len(t, unknown, 1)
	assert.Contains(t, unknown[0], `"bogus_key"`)
}

func TestLoad_MissingFileIsNotExist(t *testing.T) {
	_, _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestLoad_ParseError(t *testing.T) {
	_, _, err := Load(writeTempConfig(t, "sites: [\n"))
	require.ErrorContains(t, err, "parse config")
}
