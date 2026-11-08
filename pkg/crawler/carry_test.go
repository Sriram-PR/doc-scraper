package crawler

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/process"
)

type carryFixture struct {
	liveDir, stagedDir string
	siteCfg            *config.SiteConfig
	cp                 *process.ContentProcessor
	om                 *OutputManager
	outsideRef         string // image ref on page a that points outside the live dir
}

func newCarryFixture(t *testing.T) *carryFixture {
	t.Helper()
	appCfg := &config.AppConfig{JSONLOutputFilename: "pages.jsonl"}
	siteCfg := &config.SiteConfig{AllowedDomain: "h", AllowedPathPrefix: "/docs"}
	// The live dir sits one level down so a ref escaping it still lands in the test's temp dir.
	f := &carryFixture{liveDir: filepath.Join(t.TempDir(), "live"), siteCfg: siteCfg, cp: process.NewContentProcessor(nil, appCfg, silentLogger())}
	f.om, f.stagedDir = newOpenOutputManager(t, true)

	mdPath := func(rawURL string) string {
		u, err := url.Parse(rawURL)
		require.NoError(t, err)
		p, ok := f.cp.OutputPathForURL(u, siteCfg, f.liveDir)
		require.True(t, ok)
		return p
	}
	refFrom := func(mdFile, target string) string {
		rel, err := filepath.Rel(filepath.Dir(mdFile), target)
		require.NoError(t, err)
		return filepath.ToSlash(rel)
	}
	writeLive := func(rawURL, md string, images []string) {
		p := mdPath(rawURL)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(md), 0o644))
		rec, err := json.Marshal(models.PageJSONL{RecordType: models.RecordTypePage, URL: rawURL, Title: "T", Content: md, Images: images, ContentHash: "old-hash"})
		require.NoError(t, err)
		jf, err := os.OpenFile(filepath.Join(f.liveDir, "pages.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		_, err = jf.Write(append(rec, '\n'))
		require.NoError(t, err)
		require.NoError(t, jf.Close())
	}

	require.NoError(t, os.MkdirAll(filepath.Join(f.liveDir, "images"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.liveDir, "images", "a.png"), []byte("png"), 0o644))
	outside := filepath.Join(filepath.Dir(f.liveDir), "outside.png")
	require.NoError(t, os.WriteFile(outside, []byte("png"), 0o644))

	aMD := mdPath("http://h/docs/a.html")
	inRef := refFrom(aMD, filepath.Join(f.liveDir, "images", "a.png"))
	f.outsideRef = refFrom(aMD, outside)
	writeLive("http://h/docs/a.html", "# A\n![x]("+inRef+")\n", []string{inRef, "https://cdn.example/x.png", f.outsideRef})
	writeLive("http://h/docs/nofile.html", "# N\n", nil)
	return f
}

func TestCarryForward_CopiesRecordMarkdownAndImages(t *testing.T) {
	f := newCarryFixture(t)
	cf, err := newCarryForward(f.liveDir, "pages.jsonl", silentLogger())
	require.NoError(t, err)
	require.NotNil(t, cf)
	u, _ := url.Parse("http://h/docs/a.html")

	require.True(t, cf.carry(u, f.om, f.cp, f.siteCfg, f.stagedDir))

	staged, ok := f.cp.OutputPathForURL(u, f.siteCfg, f.stagedDir)
	require.True(t, ok)
	assert.FileExists(t, staged)
	assert.FileExists(t, filepath.Join(f.stagedDir, "images", "a.png"))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(staged), filepath.FromSlash(f.outsideRef)), "images outside the live dir are not copied")
	assert.Equal(t, 1, f.om.UniquePages(nil))
	assert.EqualValues(t, 1, cf.count())
}

func TestCarryForward_SkipsUnknownURLAndMissingFile(t *testing.T) {
	f := newCarryFixture(t)
	cf, err := newCarryForward(f.liveDir, "pages.jsonl", silentLogger())
	require.NoError(t, err)
	unknown, _ := url.Parse("http://h/docs/never.html")
	nofile, _ := url.Parse("http://h/docs/nofile.html")
	p, _ := f.cp.OutputPathForURL(nofile, f.siteCfg, f.liveDir)
	require.NoError(t, os.Remove(p))

	assert.False(t, cf.carry(unknown, f.om, f.cp, f.siteCfg, f.stagedDir))
	assert.False(t, cf.carry(nofile, f.om, f.cp, f.siteCfg, f.stagedDir))
	assert.Equal(t, 0, f.om.UniquePages(nil))
}

func TestCarryForward_NoLiveCorpus(t *testing.T) {
	cf, err := newCarryForward(t.TempDir(), "pages.jsonl", silentLogger())
	require.NoError(t, err)
	assert.Nil(t, cf)
	assert.EqualValues(t, 0, cf.count(), "count is nil-safe")
}

func TestCarryForward_PendingSkipsPagesWithAFreshRecord(t *testing.T) {
	f := newCarryFixture(t)
	cf, err := newCarryForward(f.liveDir, "pages.jsonl", silentLogger())
	require.NoError(t, err)
	a, _ := url.Parse("http://h/docs/a.html")
	nofile, _ := url.Parse("http://h/docs/nofile.html")
	unknown, _ := url.Parse("http://h/docs/never.html")
	cf.note(a)
	cf.note(nofile)
	cf.note(unknown)

	assert.Equal(t, 2, cf.carryable(f.om), "only pages the live corpus has")
	f.om.RecordPageOutput("http://h/docs/nofile.html", []byte("# N2"), "N", 0, silentLogger())
	assert.Equal(t, 1, cf.carryable(f.om), "a page fetched later in the run is not carried")

	cf.carryAll(f.om, f.cp, f.siteCfg, f.stagedDir)

	assert.EqualValues(t, 1, cf.count())
	assert.Equal(t, 2, f.om.UniquePages(nil))
}
