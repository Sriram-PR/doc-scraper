package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopkg.in/yaml.v3"
)

func testSite() *SiteConfig {
	return &SiteConfig{
		StartURLs:         []string{"https://docs.example.com/guide/"},
		AllowedDomain:     "docs.example.com",
		AllowedPathPrefix: "/guide/",
		ContentSelector:   "article.main",
		MaxDepth:          4,
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func readBack(t *testing.T, path string) (string, *AppConfig) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var cfg AppConfig
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	return string(data), &cfg
}

func TestInsertSite_PreservesFileBytes(t *testing.T) {
	original := `# my crawler config, do not lose this comment
num_workers: 8 # inline comment survives too

sites:
  existing:
    start_urls: ["https://old.example.com/docs/"]
    allowed_domain: "old.example.com"
    allowed_path_prefix: /docs/
    content_selector: "main"
    max_depth: 3

# trailing comment about future plans
enable_incremental: true
`
	path := writeTempConfig(t, original)
	require.NoError(t, InsertSite(path, "example_docs", testSite()))

	content, cfg := readBack(t, path)
	assert.Contains(t, content, "# my crawler config, do not lose this comment")
	assert.Contains(t, content, "# inline comment survives too")
	assert.Contains(t, content, "# trailing comment about future plans")
	assert.Contains(t, content, `start_urls: ["https://old.example.com/docs/"]`, "existing entry untouched byte-for-byte")

	require.Len(t, cfg.Sites, 2)
	added := cfg.Sites["example_docs"]
	require.NotNil(t, added)
	assert.Equal(t, "docs.example.com", added.AllowedDomain)
	assert.Equal(t, "article.main", added.ContentSelector)
	assert.Equal(t, 4, added.MaxDepth)
	assert.True(t, cfg.EnableIncremental, "keys after the sites block still parse")
	assert.Equal(t, 8, cfg.NumWorkers)
}

func TestInsertSite_CreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, InsertSite(path, "example_docs", testSite()))
	_, cfg := readBack(t, path)
	require.Len(t, cfg.Sites, 1)
	assert.Equal(t, "/guide/", cfg.Sites["example_docs"].AllowedPathPrefix)
}

func TestInsertSite_NoSitesKey(t *testing.T) {
	path := writeTempConfig(t, "num_workers: 2\n")
	require.NoError(t, InsertSite(path, "example_docs", testSite()))
	_, cfg := readBack(t, path)
	assert.Equal(t, 2, cfg.NumWorkers)
	require.Len(t, cfg.Sites, 1)
}

func TestInsertSite_EmptySitesMapping(t *testing.T) {
	path := writeTempConfig(t, "sites:\n")
	require.NoError(t, InsertSite(path, "example_docs", testSite()))
	_, cfg := readBack(t, path)
	require.Len(t, cfg.Sites, 1)
}

func TestInsertSite_DuplicateKey(t *testing.T) {
	path := writeTempConfig(t, `sites:
  example_docs:
    start_urls: ["https://docs.example.com/"]
    allowed_domain: "docs.example.com"
    content_selector: "main"
`)
	err := InsertSite(path, "example_docs", testSite())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestInsertSite_FlowStyleRefused(t *testing.T) {
	path := writeTempConfig(t, "sites: {}\n")
	err := InsertSite(path, "example_docs", testSite())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "flow style")
}

func TestInsertSite_DisallowedPatternsRendered(t *testing.T) {
	site := testSite()
	site.DisallowedPathPatterns = []string{`^/en/1\.0/`, `^/ja/`}
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, InsertSite(path, "example_docs", site))
	content, cfg := readBack(t, path)
	assert.Contains(t, content, "disallowed_path_patterns:")
	assert.Equal(t, []string{`^/en/1\.0/`, `^/ja/`}, cfg.Sites["example_docs"].DisallowedPathPatterns)
}

func TestRenderSiteEntry(t *testing.T) {
	out, err := RenderSiteEntry("example_docs", testSite())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "example_docs:\n"))
	assert.Contains(t, out, "  start_urls:\n")
	assert.NotContains(t, out, "skip_images", "zero-valued optional fields stay out of the draft")
	assert.NotContains(t, out, "user_agent")
}

func TestInsertSite_CreatesMissingParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "deeper", "config.yaml")
	require.NoError(t, InsertSite(path, "example_docs", testSite()))
	_, cfg := readBack(t, path)
	require.Len(t, cfg.Sites, 1)
}

func TestInsertSite_CRLFConfigStaysCRLF(t *testing.T) {
	const existing = "sites:\r\n  existing:\r\n    start_urls: [\"https://old.example.com/docs/\"]\r\n    allowed_domain: \"old.example.com\"\r\n    allowed_path_prefix: /docs/\r\n    max_depth: 3\r\n"
	cases := map[string]string{
		"sites in middle": "# header\r\n" + existing + "\r\nenable_incremental: true\r\n",
		"sites at end":    "# header\r\n" + existing,
		"no trailing eol": "# header\r\n" + strings.TrimSuffix(existing, "\r\n"),
		"no sites key":    "num_workers: 8\r\n",
	}
	for name, original := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeTempConfig(t, original)
			require.NoError(t, InsertSite(path, "example_docs", testSite()))

			data, err := os.ReadFile(path)
			require.NoError(t, err)
			got := string(data)
			assert.Equal(t, strings.Count(got, "\n"), strings.Count(got, "\r\n"), "every line ending must be CRLF:\n%q", got)
			assert.True(t, strings.HasPrefix(got, strings.TrimRight(strings.SplitN(original, "sites:", 2)[0], "\r\n")), "content before the insertion is untouched")
			_, cfg := readBack(t, path)
			assert.NotNil(t, cfg.Sites["example_docs"])
		})
	}
}

func TestInsertSite_LFConfigUnchangedStyle(t *testing.T) {
	path := writeTempConfig(t, "sites:\n  a:\n    start_urls: [\"https://a.example.com/\"]\n    allowed_domain: a.example.com\n    allowed_path_prefix: /\n")
	require.NoError(t, InsertSite(path, "example_docs", testSite()))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "\r")
}

const upsertFixture = `# my config
output_base_dir: ./out   # keep me
sites:
  alpha:
    start_urls: ["https://alpha.example.com/"]
    allowed_domain: alpha.example.com
    content_selector: main   # alpha selector

  # beta is the second site
  beta:
    start_urls: ["https://beta.example.com/docs/"]
    allowed_domain: beta.example.com
    content_selector: article
# trailing comment
num_workers: 2
`

func TestUpsertSite_ReplaceMiddleEntry(t *testing.T) {
	path := writeTempConfig(t, upsertFixture)
	require.NoError(t, UpsertSite(path, "alpha", testSite(), true))

	content, cfg := readBack(t, path)
	before, _, _ := strings.Cut(upsertFixture, "  alpha:\n")
	_, after, _ := strings.Cut(upsertFixture, "    content_selector: main   # alpha selector\n")
	assert.True(t, strings.HasPrefix(content, before), "text before the entry is untouched:\n%s", content)
	assert.True(t, strings.HasSuffix(content, after), "text after the entry is untouched:\n%s", content)
	assert.NotContains(t, content, "alpha.example.com")
	assert.Equal(t, "article.main", cfg.Sites["alpha"].ContentSelector)
	assert.Equal(t, "docs.example.com", cfg.Sites["alpha"].AllowedDomain)
	assert.Equal(t, "article", cfg.Sites["beta"].ContentSelector)
	assert.Equal(t, 2, cfg.NumWorkers)
}

func TestUpsertSite_ReplaceLastEntry(t *testing.T) {
	path := writeTempConfig(t, upsertFixture)
	require.NoError(t, UpsertSite(path, "beta", testSite(), true))

	content, cfg := readBack(t, path)
	before, _, _ := strings.Cut(upsertFixture, "  beta:\n")
	assert.True(t, strings.HasPrefix(content, before), "text before the entry, including beta's head comment, is untouched:\n%s", content)
	assert.True(t, strings.HasSuffix(content, "# trailing comment\nnum_workers: 2\n"), "keys after sites are untouched:\n%s", content)
	assert.Equal(t, "article.main", cfg.Sites["beta"].ContentSelector)
	assert.Equal(t, "main", cfg.Sites["alpha"].ContentSelector)
}

func TestUpsertSite_ReplaceRemovesMultiLineValues(t *testing.T) {
	original := "sites:\n  alpha:\n    start_urls: [\"https://alpha.example.com/\"]\n    allowed_domain: alpha.example.com\n    content_selector: >-\n      main\n      article\n  beta:\n    start_urls: [\"https://beta.example.com/\"]\n    allowed_domain: beta.example.com\n    content_selector: article\n"
	path := writeTempConfig(t, original)
	require.NoError(t, UpsertSite(path, "alpha", testSite(), true))

	content, cfg := readBack(t, path)
	assert.NotContains(t, content, "      main\n", "continuation lines of the old value are gone")
	assert.Equal(t, "article.main", cfg.Sites["alpha"].ContentSelector)
	assert.Equal(t, 4, cfg.Sites["alpha"].MaxDepth)
	assert.Equal(t, "article", cfg.Sites["beta"].ContentSelector)
}

func TestUpsertSite_KeyMismatchLeavesFileUntouched(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		replace bool
		want    string
	}{
		{"replace a missing key", "gamma", true, `site "gamma" doesn't exist`},
		{"insert an existing key", "alpha", false, `site key "alpha" already exists`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, upsertFixture)
			err := UpsertSite(path, tc.key, testSite(), tc.replace)
			require.ErrorContains(t, err, tc.want)
			content, _ := readBack(t, path)
			assert.Equal(t, upsertFixture, content)
		})
	}
}

func TestUpsertSite_ReplaceMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.ErrorContains(t, UpsertSite(path, "alpha", testSite(), true), `site "alpha" doesn't exist`)
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "no file is created")
}

func TestUpsertSite_ReplaceKeepsCRLF(t *testing.T) {
	path := writeTempConfig(t, strings.ReplaceAll(upsertFixture, "\n", "\r\n"))
	require.NoError(t, UpsertSite(path, "alpha", testSite(), true))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(data)
	assert.Equal(t, strings.Count(got, "\n"), strings.Count(got, "\r\n"), "every line ending must be CRLF:\n%q", got)
	_, cfg := readBack(t, path)
	assert.Equal(t, "article.main", cfg.Sites["alpha"].ContentSelector)
	assert.Equal(t, "article", cfg.Sites["beta"].ContentSelector)
}
