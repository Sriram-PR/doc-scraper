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

func TestDeleteSiteChunksRemovesOnlyThatSite(t *testing.T) {
	idx := newTestIndex(t, 5)
	ctx := context.Background()

	seedPage(t, idx, "off", "https://off/a", "A", "h1", "# A\n\nwalrus text. "+strings.Repeat("filler ", 40))
	seedPage(t, idx, "on", "https://on/a", "A", "h2", "# A\n\nwalrus text. "+strings.Repeat("filler ", 40))

	require.NoError(t, idx.DeleteSiteChunks(ctx, "off"))

	has, err := idx.SiteHasChunks(ctx, "off")
	require.NoError(t, err)
	assert.False(t, has)
	results, err := idx.SearchChunks(ctx, "walrus", "", 10)
	require.NoError(t, err)
	require.Len(t, results, 1, "the full-text index drops the deleted site's rows")
	assert.Equal(t, "on", results[0].SiteKey)

	require.NoError(t, idx.DeleteSiteChunks(ctx, "off"), "deleting an empty site is a no-op")
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
	assert.Equal(t, MatchAll, results[0].Match, "every content word matched")

	results, err = idx.SearchChunks(context.Background(), `"group commands nowhere literal"`, "", 5)
	require.NoError(t, err)
	assert.Empty(t, results, "explicit phrase syntax must NOT be relaxed")
}

func TestSearchChunksBreaksTiesByURL(t *testing.T) {
	idx := newTestIndex(t, 5)
	md := "# Tie\n\nIdentical wording scores identically. " + strings.Repeat("filler ", 40)
	for _, url := range []string{"https://e/c", "https://e/a", "https://e/b"} {
		seedPage(t, idx, "s", url, "Tie", "h", md)
	}
	results, err := idx.SearchChunks(context.Background(), "identical wording", "", 10)
	require.NoError(t, err)
	urls := make([]string, 0, len(results))
	for _, r := range results {
		urls = append(urls, r.URL)
	}
	assert.Equal(t, []string{"https://e/a", "https://e/b", "https://e/c"}, urls)
}

func TestSearchChunksBreaksURLTiesByAnchor(t *testing.T) {
	idx := newTestIndex(t, 5)
	section := "Identical wording scores identically. " + strings.Repeat("filler ", 40)
	seedPage(t, idx, "s", "https://e/a", "Tie", "h", "# Tie\n\nAn unrelated introduction. "+strings.Repeat("filler ", 40)+"\n\n## Zeta\n\n"+section+"\n\n## Beta\n\n"+section)
	results, err := idx.SearchChunks(context.Background(), "identical wording", "", 10)
	require.NoError(t, err)
	anchors := make([]string, 0, len(results))
	for _, r := range results {
		anchors = append(anchors, r.Anchor)
	}
	assert.Equal(t, []string{"beta", "zeta"}, anchors)
}

func seedFillCorpus(t *testing.T, idx *Index) {
	t.Helper()
	filler := strings.Repeat("filler ", 40)
	seedPage(t, idx, "s", "https://e/filters", "Filters", "h1", "# Filters\n\nRegister a custom jinja filter on the app. "+filler)
	seedPage(t, idx, "s", "https://e/jinja", "Templates", "h2", "# Templates\n\nJinja renders every template. "+filler)
	seedPage(t, idx, "s", "https://e/requests", "Requests", "h3", "# Requests\n\nA request filter drops bad input. "+filler)
	seedPage(t, idx, "s", "https://e/other", "Other", "h4", "# Other\n\nNothing relevant here. "+filler)
}

func resultURLs(results []SearchResult) []string {
	urls := make([]string, 0, len(results))
	for _, r := range results {
		urls = append(urls, r.URL)
	}
	return urls
}

func TestSearchChunksFillsWithPartialMatches(t *testing.T) {
	idx := newTestIndex(t, 5)
	seedFillCorpus(t, idx)

	results, err := idx.SearchChunks(context.Background(), "register custom jinja filter", "", 5)
	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.Equal(t, "https://e/filters", results[0].URL, "the strict match keeps first place")
	assert.Equal(t, MatchAll, results[0].Match)
	assert.ElementsMatch(t, []string{"https://e/jinja", "https://e/requests"}, resultURLs(results[1:]))
	for _, r := range results[1:] {
		assert.Equal(t, MatchPartial, r.Match, r.URL)
	}
}

func TestSearchChunksPrefixIsStableAcrossLimits(t *testing.T) {
	idx := newTestIndex(t, 5)
	seedFillCorpus(t, idx)
	ctx := context.Background()

	full, err := idx.SearchChunks(ctx, "register custom jinja filter", "", 10)
	require.NoError(t, err)
	for _, limit := range []int{1, 2} {
		got, err := idx.SearchChunks(ctx, "register custom jinja filter", "", limit)
		require.NoError(t, err)
		assert.Equal(t, full[:limit], got, "limit %d", limit)
	}
}

func TestSearchChunksFillKeepsEveryChunkOfASplitSection(t *testing.T) {
	idx := newTestIndex(t, 5)
	filler := strings.Repeat("filler ", 190)
	seedPage(t, idx, "s", "https://e/long", "Long", "h", "# Long\n\nalpha beta "+filler+"\n\nalpha gamma "+filler)

	results, err := idx.SearchChunks(context.Background(), "alpha beta", "", 10)
	require.NoError(t, err)
	require.Len(t, results, 2, "both chunks of the section, once each")
	assert.Equal(t, results[0].Anchor, results[1].Anchor, "fixture: one section split in two")
	assert.Equal(t, MatchAll, results[0].Match)
	assert.Equal(t, MatchPartial, results[1].Match)
}

func TestSearchChunksNoFillForExplicitSyntaxOrOneWord(t *testing.T) {
	idx := newTestIndex(t, 5)
	seedFillCorpus(t, idx)
	ctx := context.Background()

	results, err := idx.SearchChunks(ctx, `"custom jinja"`, "", 5)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://e/filters"}, resultURLs(results))

	results, err = idx.SearchChunks(ctx, "title:filters jinja", "", 5)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://e/filters"}, resultURLs(results), "a column filter is never relaxed")

	results, err = idx.SearchChunks(ctx, "jinja", "", 5)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"https://e/filters", "https://e/jinja"}, resultURLs(results))
	for _, r := range results {
		assert.Equal(t, MatchAll, r.Match)
	}
}

func TestSearchStagedReturnsFillStageError(t *testing.T) {
	calls := 0
	raw := func(_ context.Context, _, _ string, _ int, _ []int64) ([]SearchResult, []int64, error) {
		calls++
		if calls == 1 {
			return []SearchResult{{URL: "https://e/a"}}, []int64{1}, nil
		}
		return nil, nil, context.Canceled
	}
	results, err := searchStaged(context.Background(), "two words", "", 5, raw)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, results)
	assert.Equal(t, 2, calls)
}

func TestRelaxedFTSQueriesFlagsOnlyORPartial(t *testing.T) {
	cases := map[string][]ftsRewrite{
		"how do I install":        {{match: `"install"`}},
		"how do I group commands": {{match: `"group" "commands"`}, {match: `"group" OR "commands"`, partial: true}},
		"retry limits":            {{match: `"retry" OR "limits"`, partial: true}},
		"how do I":                {{match: `"how" OR "do" OR "I"`, partial: true}},
		"jinja":                   nil,
		`"custom jinja"`:          nil,
		"title:retry backoff":     nil,
		"Title : retry backoff":   nil,
		"{title text}: retry now": nil,
		"{title}: retry now":      nil,
		"{text } : retry now":     nil,
		"-identifiers:foo bar":    nil,
		"retry: backoff limits":   {{match: `"retry:" OR "backoff" OR "limits"`, partial: true}},
	}
	for q, want := range cases {
		assert.Equal(t, want, relaxedFTSQueries(q), q)
	}
}

func TestSearchChunksFillExcludesEveryEarlierStage(t *testing.T) {
	idx := newTestIndex(t, 5)
	filler := strings.Repeat("filler ", 40)
	seedPage(t, idx, "s", "https://e/groups", "Groups", "h1", "# Groups\n\nCommands nest under a group. "+filler)
	seedPage(t, idx, "s", "https://e/commands", "Commands", "h2", "# Commands\n\nEach command has options. "+filler)

	results, err := idx.SearchChunks(context.Background(), "how do I group commands", "", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://e/groups", "https://e/commands"}, resultURLs(results))
	assert.Equal(t, []string{MatchAll, MatchPartial}, []string{results[0].Match, results[1].Match})
}

func TestSearchStagedSkipsFillWhenFull(t *testing.T) {
	calls := 0
	raw := func(_ context.Context, _, _ string, limit int, _ []int64) ([]SearchResult, []int64, error) {
		calls++
		return make([]SearchResult, limit), make([]int64, limit), nil
	}
	results, err := searchStaged(context.Background(), "two words", "", 3, raw)
	require.NoError(t, err)
	assert.Len(t, results, 3)
	assert.Equal(t, 1, calls)
}

func TestRelaxedFTSQueriesExpandsSynonyms(t *testing.T) {
	withSynonyms(t, [][]string{{"autocomplete", "completion"}, {"query string", "url parameters"}})
	cases := map[string][]ftsRewrite{
		"how do I read query string parameters": {
			{match: `"read" "query" "string" "parameters"`},
			{match: `"read" AND ("query string" OR "url parameters") AND "parameters"`, partial: true},
			{match: `"read" OR "query" OR "string" OR "url parameters" OR "parameters"`, partial: true},
		},
		"Autocomplete setup": {
			{match: `("Autocomplete" OR "completion") AND "setup"`, partial: true},
			{match: `"Autocomplete" OR "completion" OR "setup"`, partial: true},
		},
		"autocomplete":            {{match: `("autocomplete" OR "completion")`, partial: true}},
		"install":                 nil,
		`"autocomplete"`:          nil,
		"autocomplete*":           nil,
		"title:autocomplete zsh":  nil,
		"autocomplete OR install": nil,
		"how do I":                {{match: `"how" OR "do" OR "I"`, partial: true}},
	}
	for q, want := range cases {
		assert.Equal(t, want, relaxedFTSQueries(q), q)
	}
}

func TestSearchChunksFillsFromSynonyms(t *testing.T) {
	withSynonyms(t, [][]string{{"autocomplete", "completion"}})
	idx := newTestIndex(t, 5)
	filler := strings.Repeat("filler ", 40)
	seedPage(t, idx, "s", "https://e/api", "API", "h1", "# API\n\nThe autocomplete hook. "+filler)
	seedPage(t, idx, "s", "https://e/completion", "Shell completion", "h2", "# Shell completion\n\nEnable completion for zsh. "+filler)
	seedPage(t, idx, "s", "https://e/other", "Other", "h3", "# Other\n\nNothing relevant here. "+filler)
	ctx := context.Background()

	results, err := idx.SearchChunks(ctx, "autocomplete", "", 5)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://e/api", "https://e/completion"}, resultURLs(results))
	assert.Equal(t, []string{MatchAll, MatchPartial}, []string{results[0].Match, results[1].Match})

	results, err = idx.SearchChunks(ctx, "autocomplete", "", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://e/api"}, resultURLs(results), "a full strict result list gets no synonym fill")

	results, err = idx.SearchChunks(ctx, "how do I enable autocomplete for zsh", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, "https://e/completion", results[0].URL, "the synonym AND stage ranks the page with every concept first")
	assert.Equal(t, MatchPartial, results[0].Match)
}
