package crawler

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

func TestContentRegionHash_DiffersFromUnversionedHash(t *testing.T) {
	region := `<main id="c"><p>A1</p></main>`
	assert.NotEqual(t, utils.CalculateStringSHA256(region), contentRegionHash(region))
	assert.NotEqual(t, contentRegionHash(region), contentRegionHash(`<main id="c"><p>A2</p></main>`))
}

// A visited DB written before the format version existed holds plain SHA-256
// hashes of the content region; the next incremental crawl must reconvert
// those pages instead of skipping them as unchanged.
func TestCrawlerRun_IncrementalReconvertsPagesHashedBeforeFormatVersion(t *testing.T) {
	server, ms := newMutableDocServer(t)
	pageHTML := `<html><head><title>A</title></head><body><main id="c"><p>A1</p></main></body></html>`
	ms.set("/docs/index.html", pageHTML)

	appCfg := newTestAppConfig(t)
	appCfg.EnableIncremental = true
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.ContentSelector = "#c"

	p := runResumableCrawl(t, appCfg, siteCfg, false)
	require.EqualValues(t, 1, p.PagesProcessed)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(pageHTML))
	require.NoError(t, err)
	region, err := goquery.OuterHtml(doc.Find("#c"))
	require.NoError(t, err)
	key, _, err := parse.ParseAndNormalize(server.URL + "/docs/index.html")
	require.NoError(t, err)

	store, err := storage.NewBadgerStore(context.Background(), appCfg.StateDir, testSiteKey, true, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	_, entry, err := store.CheckPageStatus(key)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.Equal(t, contentRegionHash(region), entry.ContentHash, "a crawl stores the versioned hash")
	entry.ContentHash = utils.CalculateStringSHA256(region)
	require.NoError(t, store.UpdatePageStatus(key, entry))
	require.NoError(t, store.Close())

	p = runResumableCrawl(t, appCfg, siteCfg, true)
	assert.EqualValues(t, 1, p.PagesProcessed, "an unversioned hash must not match")
	assert.EqualValues(t, 0, p.PagesUnchanged)

	p = runResumableCrawl(t, appCfg, siteCfg, true)
	assert.EqualValues(t, 0, p.PagesProcessed, "the rewritten versioned hash matches again")
	assert.EqualValues(t, 1, p.PagesUnchanged)
}
