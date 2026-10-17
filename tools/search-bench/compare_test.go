package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deltaByName(t *testing.T, c comparison, name string) metricDelta {
	t.Helper()
	for _, m := range c.metrics {
		if m.name == name {
			return m
		}
	}
	require.FailNow(t, "unknown metric "+name)
	return metricDelta{}
}

func runOf(ranks ...int) runFile {
	r := runFile{Version: runFileVersion, Corpora: []corpusInfo{{Site: "s", Fingerprint: fingerprint{Pages: 1}}}}
	for i, rank := range ranks {
		r.Queries = append(r.Queries, queryResult{Site: "s", ID: fmt.Sprint(i), PageRank: rank, ChunkRank: rank})
	}
	return r
}

func TestCompare_IdenticalRuns(t *testing.T) {
	r := runOf(1, 2, 0, 5)
	c := compare(r, r, 1)
	assert.Equal(t, 4, c.paired)
	assert.Empty(t, c.fingerprintChanged)
	for _, m := range c.metrics {
		if m.n == 0 {
			continue
		}
		assert.Zero(t, m.delta, m.name)
		assert.Zero(t, m.wins+m.losses, m.name)
		assert.InDelta(t, 1.0, m.p, 1e-12, m.name)
	}
}

func TestCompare_ConsistentImprovement(t *testing.T) {
	base := make([]int, 20)
	cur := make([]int, 20)
	for i := range cur {
		cur[i] = 1
	}
	c := compare(runOf(base...), runOf(cur...), 1)
	s1 := deltaByName(t, c, "s@1")
	assert.Equal(t, 20, s1.n)
	assert.Equal(t, 20, s1.wins)
	assert.InDelta(t, 1.0, s1.delta, 1e-12)
	assert.Less(t, s1.p, 0.01)
}

func TestCompare_MixedDeltas(t *testing.T) {
	c := compare(runOf(1, 0, 2, 3), runOf(2, 1, 2, 0), 1)
	mrr := deltaByName(t, c, "mrr@10")
	assert.Equal(t, 1, mrr.wins)
	assert.Equal(t, 2, mrr.losses)
	assert.Equal(t, 1, mrr.ties)
	assert.InDelta(t, (1+0+0.5+1.0/3)/4, mrr.base, 1e-12)
	assert.InDelta(t, (0.5+1+0.5+0)/4, mrr.cur, 1e-12)
	assert.Greater(t, mrr.p, 0.05)
}

func TestCompare_SeedIsReproducible(t *testing.T) {
	base, cur := runOf(1, 0, 2, 3, 0, 1), runOf(2, 1, 2, 0, 1, 1)
	p1 := deltaByName(t, compare(base, cur, 7), "mrr@10").p
	p2 := deltaByName(t, compare(base, cur, 7), "mrr@10").p
	assert.InDelta(t, p1, p2, 0)
}

func TestCompare_UnmatchedAndFingerprint(t *testing.T) {
	base := runOf(1, 2)
	cur := runOf(1, 2)
	cur.Queries[1].ID = "new"
	cur.Corpora[0].Fingerprint.Pages = 9
	c := compare(base, cur, 1)
	assert.Equal(t, 1, c.paired)
	assert.Equal(t, []string{"s/1"}, c.onlyBase)
	assert.Equal(t, []string{"s/new"}, c.onlyCur)
	assert.Equal(t, []string{"s"}, c.fingerprintChanged)
	assert.Equal(t, 1, deltaByName(t, c, "s@1").n)
}

func TestCompare_SectionOnlyWhereBothHaveAnchors(t *testing.T) {
	base, cur := runOf(1, 1), runOf(1, 1)
	base.Queries[0].HasSection, base.Queries[0].SectionRank = true, 3
	cur.Queries[0].HasSection, cur.Queries[0].SectionRank = true, 1
	// Only the baseline names an anchor for query 1: not comparable.
	base.Queries[1].HasSection, base.Queries[1].SectionRank = true, 1
	sec := deltaByName(t, compare(base, cur, 1), "sec@10")
	assert.Equal(t, 1, sec.n)
	assert.Zero(t, sec.delta)
}

func TestCompare_ZeroCounts(t *testing.T) {
	base, cur := runOf(0, 1), runOf(1, 1)
	base.Queries[0].Zero = true
	c := compare(base, cur, 1)
	assert.Equal(t, 1, c.zeroBase)
	assert.Equal(t, 0, c.zeroCur)
}

func TestRandomizationP(t *testing.T) {
	assert.InDelta(t, 1.0, randomizationP(nil, 1), 1e-12)
	assert.InDelta(t, 1.0, randomizationP([]float64{0, 0, 0}, 1), 1e-12)
	// One delta: a sign flip always ties the observed magnitude.
	assert.InDelta(t, 1.0, randomizationP([]float64{1}, 1), 1e-12)
	// 30 equal deltas: no flip pattern but all-same-sign reaches the observed
	// mean (chance 2/2^30 per draw), so p is the (0+1)/(iters+1) floor.
	same := make([]float64, 30)
	for i := range same {
		same[i] = 1
	}
	assert.InDelta(t, 1.0/(randomizationIters+1), randomizationP(same, 1), 1e-12)
}
