package index

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
)

func seedPage(t *testing.T, idx *Index, siteKey, url, title, hash, markdown string) {
	t.Helper()
	require.NoError(t, idx.ReplaceChunks(context.Background(), siteKey, url, title, hash, chunk.Split(markdown)))
}

func TestReplaceAndSearchChunks(t *testing.T) {
	idx := newTestIndex(t, 5)
	ctx := context.Background()

	seedPage(t, idx, "siteA", "https://a.example/config", "Configuration", "h1",
		"# Configuration\n\nRetry behavior is controlled by max_retries and initial_retry_delay. "+strings.Repeat("filler ", 40))
	seedPage(t, idx, "siteA", "https://a.example/install", "Install", "h2",
		"# Install\n\nDownload the binary or build from source with go install. "+strings.Repeat("filler ", 40))
	seedPage(t, idx, "siteB", "https://b.example/retry", "Retries elsewhere", "h3",
		"# Other site\n\nretry retry retry configuration. "+strings.Repeat("filler ", 40))

	results, err := idx.SearchChunks(ctx, "retry", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	bySite := map[string]bool{}
	for _, r := range results {
		bySite[r.SiteKey] = true
	}
	assert.True(t, bySite["siteA"] && bySite["siteB"], "unfiltered search spans sites")

	results, err = idx.SearchChunks(ctx, "retry", "siteA", 10)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	for _, r := range results {
		assert.Equal(t, "siteA", r.SiteKey)
	}
	assert.Contains(t, results[0].Snippet, "[", "snippet marks match terms")
}

func TestSearchChunksPorterStemming(t *testing.T) {
	idx := newTestIndex(t, 5)
	seedPage(t, idx, "s", "https://e/x", "Semaphores", "h",
		"# Semaphores\n\nAcquiring the host slot before the global slot prevents inversion. "+strings.Repeat("filler ", 40))

	results, err := idx.SearchChunks(context.Background(), "acquire", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results, "porter stemmer matches acquire against acquiring")
}

func TestSearchChunksMalformedQueryFallsBack(t *testing.T) {
	idx := newTestIndex(t, 5)
	seedPage(t, idx, "s", "https://e/x", "Page", "h",
		"# Page\n\nsome AND NOT \"quoted content here. "+strings.Repeat("filler ", 40))

	for _, q := range []string{`"unbalanced`, `AND`, `(dangling`, `col:val`} {
		_, err := idx.SearchChunks(context.Background(), q, "", 5)
		assert.NoError(t, err, "query %q must not surface a syntax error", q)
	}
}

func TestSearchChunksEmptyQueryRejected(t *testing.T) {
	idx := newTestIndex(t, 5)
	_, err := idx.SearchChunks(context.Background(), "   ", "", 5)
	assert.Error(t, err)
}

func TestReplaceChunksSwapsContent(t *testing.T) {
	idx := newTestIndex(t, 5)
	ctx := context.Background()

	seedPage(t, idx, "s", "https://e/p", "Page", "h1",
		"# Page\n\nzebra content only here. "+strings.Repeat("filler ", 40))
	seedPage(t, idx, "s", "https://e/p", "Page", "h2",
		"# Page\n\nquokka content only here. "+strings.Repeat("filler ", 40))

	results, err := idx.SearchChunks(ctx, "zebra", "", 5)
	require.NoError(t, err)
	assert.Empty(t, results, "replaced content must leave the FTS index")

	results, err = idx.SearchChunks(ctx, "quokka", "", 5)
	require.NoError(t, err)
	assert.NotEmpty(t, results)

	hashes, err := idx.ChunkedHashes(ctx, "s")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"https://e/p": "h2"}, hashes)
}

func TestPruneChunksRemovesStalePages(t *testing.T) {
	idx := newTestIndex(t, 5)
	ctx := context.Background()

	seedPage(t, idx, "s", "https://e/keep", "Keep", "h1",
		"# Keep\n\nalpaca text. "+strings.Repeat("filler ", 40))
	seedPage(t, idx, "s", "https://e/gone", "Gone", "h2",
		"# Gone\n\nwalrus text. "+strings.Repeat("filler ", 40))

	require.NoError(t, idx.PruneChunks(ctx, "s", map[string]struct{}{"https://e/keep": {}}))

	results, err := idx.SearchChunks(ctx, "walrus", "", 5)
	require.NoError(t, err)
	assert.Empty(t, results)

	has, err := idx.SiteHasChunks(ctx, "s")
	require.NoError(t, err)
	assert.True(t, has)

	hashes, err := idx.ChunkedHashes(ctx, "s")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"https://e/keep": "h1"}, hashes)
}

func TestSiteHasChunksEmptySite(t *testing.T) {
	idx := newTestIndex(t, 5)
	has, err := idx.SiteHasChunks(context.Background(), "nope")
	require.NoError(t, err)
	assert.False(t, has)
}

func TestSearchChunksReturnsAnchor(t *testing.T) {
	idx := newTestIndex(t, 5)
	seedPage(t, idx, "s", "https://e/guide", "Guide", "h",
		"# Getting Started "+strings.Repeat("g", 300)+"\n\n## From Source\n\nBuild the ocelot binary yourself. "+strings.Repeat("filler ", 40))

	results, err := idx.SearchChunks(context.Background(), "ocelot", "", 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, strings.HasPrefix(results[0].Anchor, "from-source"), "anchor %q", results[0].Anchor)
}

func TestSearchChunksMatchesCamelCaseViaIdentifiers(t *testing.T) {
	idx := newTestIndex(t, 5)
	seedPage(t, idx, "s", "https://e/api", "API", "h",
		"# API\n\nUse setMaxRetries to bound the wombat attempts. "+strings.Repeat("filler ", 40))

	results, err := idx.SearchChunks(context.Background(), "max retries", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results, "split query terms must match camelCase identifier")

	results, err = idx.SearchChunks(context.Background(), "setMaxRetries", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results, "fused query must still match")
}

func TestSearchChunksNaturalLanguageRelaxesToOR(t *testing.T) {
	idx := newTestIndex(t, 5)
	seedPage(t, idx, "s", "https://e/groups", "Groups", "h",
		"# Groups\n\nCommands nest under a group for subcommand dispatch. "+strings.Repeat("filler ", 40))

	results, err := idx.SearchChunks(context.Background(), "how do I group commands", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results, "OR relaxation must rescue natural-language queries")
	assert.Equal(t, "https://e/groups", results[0].URL)

	results, err = idx.SearchChunks(context.Background(), `"group commands nowhere literal"`, "", 5)
	require.NoError(t, err)
	assert.Empty(t, results, "explicit phrase syntax must NOT be relaxed")
}
