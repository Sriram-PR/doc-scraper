package main

import (
	"cmp"
	"maps"
	"slices"
)

// chunkWindow is the default search_docs and search limit: what a caller sees
// without asking for more.
const chunkWindow = 10

const topURLCount = 5

type hit struct {
	url    string
	anchor string
	score  float64
}

type target struct {
	URL    string `json:"url"`
	Anchor string `json:"anchor,omitempty"`
}

type queryResult struct {
	Site        string   `json:"site"`
	ID          string   `json:"id"`
	Query       string   `json:"query"`
	Tags        []string `json:"tags"`
	PageRank    int      `json:"page_rank"`
	SectionRank int      `json:"section_rank"`
	HasSection  bool     `json:"has_section"`
	ChunkRank   int      `json:"chunk_rank"`
	Zero        bool     `json:"zero"`
	TopURLs     []string `json:"top_urls"`
}

// scoreQuery ranks hits by bm25 score (lower is better), breaking ties by url
// then anchor so equal scores order the same way on every run.
func scoreQuery(hits []hit, expected []target) queryResult {
	sorted := slices.Clone(hits)
	slices.SortFunc(sorted, func(a, b hit) int {
		return cmp.Or(cmp.Compare(a.score, b.score), cmp.Compare(a.url, b.url), cmp.Compare(a.anchor, b.anchor))
	})

	wantPage := make(map[string]bool, len(expected))
	wantSection := make(map[target]bool)
	for _, t := range expected {
		wantPage[t.URL] = true
		if t.Anchor != "" {
			wantSection[t] = true
		}
	}

	r := queryResult{Zero: len(sorted) == 0, HasSection: len(wantSection) > 0, TopURLs: []string{}}
	seen := make(map[string]bool)
	for i, h := range sorted {
		if !seen[h.url] {
			seen[h.url] = true
			if len(r.TopURLs) < topURLCount {
				r.TopURLs = append(r.TopURLs, h.url)
			}
			if r.PageRank == 0 && wantPage[h.url] {
				r.PageRank = len(seen)
			}
		}
		if r.SectionRank == 0 && wantSection[target{URL: h.url, Anchor: h.anchor}] {
			r.SectionRank = i + 1
		}
		if r.ChunkRank == 0 && i < chunkWindow && wantPage[h.url] {
			r.ChunkRank = i + 1
		}
	}
	return r
}

type metricDef struct {
	name string
	// applies reports whether a query counts toward this metric; nil means all.
	applies func(queryResult) bool
	value   func(queryResult) float64
}

func within(rank, k int) float64 {
	if rank > 0 && rank <= k {
		return 1
	}
	return 0
}

var metricDefs = []metricDef{
	{name: "s@1", value: func(r queryResult) float64 { return within(r.PageRank, 1) }},
	{name: "s@3", value: func(r queryResult) float64 { return within(r.PageRank, 3) }},
	{name: "s@10", value: func(r queryResult) float64 { return within(r.PageRank, 10) }},
	{name: "mrr@10", value: func(r queryResult) float64 {
		if r.PageRank > 0 && r.PageRank <= 10 {
			return 1 / float64(r.PageRank)
		}
		return 0
	}},
	{name: "seen@10", value: func(r queryResult) float64 { return within(r.ChunkRank, chunkWindow) }},
	{
		name:    "sec@10",
		applies: func(r queryResult) bool { return r.HasSection },
		value:   func(r queryResult) float64 { return within(r.SectionRank, 10) },
	},
}

func (m metricDef) counts(r queryResult) bool { return m.applies == nil || m.applies(r) }

type metricMean struct {
	n    int
	mean float64
}

type summary struct {
	n       int
	zero    int
	metrics []metricMean
}

func summarize(rs []queryResult) summary {
	s := summary{n: len(rs), metrics: make([]metricMean, len(metricDefs))}
	for _, r := range rs {
		if r.Zero {
			s.zero++
		}
	}
	for i, def := range metricDefs {
		var sum float64
		var n int
		for _, r := range rs {
			if def.counts(r) {
				sum += def.value(r)
				n++
			}
		}
		if n > 0 {
			s.metrics[i] = metricMean{n: n, mean: sum / float64(n)}
		}
	}
	return s
}

type group struct {
	name    string
	results []queryResult
}

// groupResults returns overall, per-site and per-tag groups in that order,
// sites and tags sorted by name. A query counts in every tag it carries.
func groupResults(rs []queryResult) []group {
	bySite := make(map[string][]queryResult)
	byTag := make(map[string][]queryResult)
	for _, r := range rs {
		bySite[r.Site] = append(bySite[r.Site], r)
		for _, t := range r.Tags {
			byTag[t] = append(byTag[t], r)
		}
	}
	groups := make([]group, 0, 1+len(bySite)+len(byTag))
	groups = append(groups, group{name: "all", results: rs})
	for _, site := range slices.Sorted(maps.Keys(bySite)) {
		groups = append(groups, group{name: "site:" + site, results: bySite[site]})
	}
	for _, tag := range slices.Sorted(maps.Keys(byTag)) {
		groups = append(groups, group{name: "tag:" + tag, results: byTag[tag]})
	}
	return groups
}
