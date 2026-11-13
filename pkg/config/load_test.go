package config

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
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

func TestLoad_RejectsSiteWithNoSettings(t *testing.T) {
	for _, entry := range []string{"b:", "b: null", "b: ~"} {
		t.Run(entry, func(t *testing.T) {
			_, _, err := Load(writeTempConfig(t, "sites:\n  a:\n    content_selector: main\n  "+entry+"\n"))
			require.ErrorIs(t, err, utils.ErrConfigValidation)
			assert.Equal(t, utils.ErrConfigValidation.Error()+": site 'b' has no settings", err.Error())
		})
	}
}

func TestLoad_ListsEverySiteWithNoSettings(t *testing.T) {
	_, _, err := Load(writeTempConfig(t, "sites:\n  z:\n  a: null\n  ok:\n    content_selector: main\n"))
	require.Error(t, err)
	lines := strings.Split(err.Error(), "\n")
	require.Len(t, lines, 2, err.Error())
	assert.Contains(t, lines[0], "site 'a' has no settings")
	assert.Contains(t, lines[1], "site 'z' has no settings")
}
