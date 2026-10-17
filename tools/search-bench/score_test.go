package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pageA = "https://x.example/a"
	pageB = "https://x.example/b"
	pageC = "https://x.example/c"
)

func TestScoreQuery_CollapsesChunksToPages(t *testing.T) {
	hits := []hit{
		{url: pageA, anchor: "one", score: -3},
		{url: pageA, anchor: "two", score: -2.5},
		{url: pageA, anchor: "three", score: -2},
		{url: pageB, anchor: "intro", score: -1},
	}
	r := scoreQuery(hits, []target{{URL: pageB}})
	assert.Equal(t, 2, r.PageRank)
	assert.Equal(t, 4, r.ChunkRank)
	assert.Equal(t, []string{pageA, pageB}, r.TopURLs)
	assert.False(t, r.Zero)
	assert.False(t, r.HasSection)
}

func TestScoreQuery_TieBreakIgnoresInputOrder(t *testing.T) {
	forward := []hit{{url: pageA, score: -1}, {url: pageB, score: -1}}
	backward := []hit{{url: pageB, score: -1}, {url: pageA, score: -1}}
	for _, hits := range [][]hit{forward, backward} {
		r := scoreQuery(hits, []target{{URL: pageB}})
		assert.Equal(t, 2, r.PageRank)
		assert.Equal(t, []string{pageA, pageB}, r.TopURLs)
	}
}

func TestScoreQuery_TieBreakOnAnchorWithinPage(t *testing.T) {
	hits := []hit{{url: pageA, anchor: "b", score: -1}, {url: pageA, anchor: "a", score: -1}}
	r := scoreQuery(hits, []target{{URL: pageA, Anchor: "b"}})
	assert.Equal(t, 2, r.SectionRank)
}

func TestScoreQuery_AbsentAndZero(t *testing.T) {
	r := scoreQuery([]hit{{url: pageA, score: -1}}, []target{{URL: pageC}})
	assert.Zero(t, r.PageRank)
	assert.Zero(t, r.ChunkRank)
	assert.False(t, r.Zero)

	r = scoreQuery(nil, []target{{URL: pageC}})
	assert.True(t, r.Zero)
	assert.Zero(t, r.PageRank)
	assert.Equal(t, []string{}, r.TopURLs)
}

func TestScoreQuery_SectionMatchesOnlyURLAndAnchor(t *testing.T) {
	hits := []hit{
		{url: pageA, anchor: "setup", score: -3},
		{url: pageB, anchor: "other", score: -2},
		{url: pageB, anchor: "setup", score: -1},
	}
	r := scoreQuery(hits, []target{{URL: pageB, Anchor: "setup"}})
	assert.True(t, r.HasSection)
	assert.Equal(t, 3, r.SectionRank)
	assert.Equal(t, 2, r.PageRank)
	assert.Equal(t, 2, r.ChunkRank)
}

func TestScoreQuery_AnyExpectedPageCounts(t *testing.T) {
	hits := []hit{{url: pageA, score: -3}, {url: pageB, score: -2}, {url: pageC, score: -1}}
	r := scoreQuery(hits, []target{{URL: pageC}, {URL: pageB}})
	assert.Equal(t, 2, r.PageRank)
}

func TestScoreQuery_ChunkRankOnlyWithinWindow(t *testing.T) {
	hits := make([]hit, 0, chunkWindow+1)
	for i := range chunkWindow {
		hits = append(hits, hit{url: pageA, anchor: string(rune('a' + i)), score: float64(-100 + i)})
	}
	hits = append(hits, hit{url: pageB, score: 0})
	r := scoreQuery(hits, []target{{URL: pageB}})
	assert.Equal(t, 2, r.PageRank)
	assert.Zero(t, r.ChunkRank)
}

func TestScoreQuery_TopURLsCapped(t *testing.T) {
	hits := make([]hit, 0, topURLCount+2)
	for i := range topURLCount + 2 {
		hits = append(hits, hit{url: "https://x.example/" + string(rune('a'+i)), score: float64(i)})
	}
	assert.Len(t, scoreQuery(hits, nil).TopURLs, topURLCount)
}

func metricByName(t *testing.T, s summary, name string) metricMean {
	t.Helper()
	for i, def := range metricDefs {
		if def.name == name {
			return s.metrics[i]
		}
	}
	require.FailNow(t, "unknown metric "+name)
	return metricMean{}
}

func TestSummarize(t *testing.T) {
	rs := []queryResult{
		{PageRank: 1, ChunkRank: 1},
		{PageRank: 2, ChunkRank: 5},
		{PageRank: 4, ChunkRank: 0, HasSection: true, SectionRank: 7},
		{PageRank: 11, HasSection: true, SectionRank: 12},
		{Zero: true},
	}
	s := summarize(rs)
	assert.Equal(t, 5, s.n)
	assert.Equal(t, 1, s.zero)
	assert.InDelta(t, 0.2, metricByName(t, s, "s@1").mean, 1e-9)
	assert.InDelta(t, 0.4, metricByName(t, s, "s@3").mean, 1e-9)
	assert.InDelta(t, 0.6, metricByName(t, s, "s@10").mean, 1e-9)
	assert.InDelta(t, (1+0.5+0.25)/5, metricByName(t, s, "mrr@10").mean, 1e-9)
	assert.InDelta(t, 0.4, metricByName(t, s, "seen@10").mean, 1e-9)
	sec := metricByName(t, s, "sec@10")
	assert.Equal(t, 2, sec.n)
	assert.InDelta(t, 0.5, sec.mean, 1e-9)
}

func TestSummarize_NoApplicableQueries(t *testing.T) {
	sec := metricByName(t, summarize([]queryResult{{PageRank: 1}}), "sec@10")
	assert.Zero(t, sec.n)
}

func TestGroupResults(t *testing.T) {
	rs := []queryResult{
		{Site: "b", ID: "1", Tags: []string{"keyword", "mismatch"}},
		{Site: "a", ID: "2", Tags: []string{"question"}},
		{Site: "b", ID: "3", Tags: []string{"keyword"}},
	}
	groups := groupResults(rs)
	names := make([]string, 0, len(groups))
	sizes := map[string]int{}
	for _, g := range groups {
		names = append(names, g.name)
		sizes[g.name] = len(g.results)
	}
	assert.Equal(t, []string{"all", "site:a", "site:b", "tag:keyword", "tag:mismatch", "tag:question"}, names)
	assert.Equal(t, map[string]int{"all": 3, "site:a": 1, "site:b": 2, "tag:keyword": 2, "tag:mismatch": 1, "tag:question": 1}, sizes)
}
