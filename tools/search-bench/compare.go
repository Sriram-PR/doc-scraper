package main

import (
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
)

const randomizationIters = 10000

type metricDelta struct {
	name                string
	n                   int
	wins, losses, ties  int
	base, cur, delta, p float64
}

type comparison struct {
	paired             int
	onlyBase, onlyCur  []string
	fingerprintChanged []string
	zeroBase, zeroCur  int
	metrics            []metricDelta
}

func resultKey(r queryResult) string { return r.Site + "/" + r.ID }

// compare pairs queries by site and id; queries in only one run are listed and
// left out of every metric.
func compare(base, cur runFile, seed uint64) comparison {
	var c comparison

	baseFP := make(map[string]fingerprint, len(base.Corpora))
	for _, ci := range base.Corpora {
		baseFP[ci.Site] = ci.Fingerprint
	}
	for _, ci := range cur.Corpora {
		if fp, ok := baseFP[ci.Site]; ok && fp != ci.Fingerprint {
			c.fingerprintChanged = append(c.fingerprintChanged, ci.Site)
		}
	}

	baseByKey := make(map[string]queryResult, len(base.Queries))
	for _, r := range base.Queries {
		baseByKey[resultKey(r)] = r
	}
	var pairs [][2]queryResult
	inCur := make(map[string]bool, len(cur.Queries))
	for _, r := range cur.Queries {
		inCur[resultKey(r)] = true
		b, ok := baseByKey[resultKey(r)]
		if !ok {
			c.onlyCur = append(c.onlyCur, resultKey(r))
			continue
		}
		pairs = append(pairs, [2]queryResult{b, r})
		if b.Zero {
			c.zeroBase++
		}
		if r.Zero {
			c.zeroCur++
		}
	}
	for _, r := range base.Queries {
		if !inCur[resultKey(r)] {
			c.onlyBase = append(c.onlyBase, resultKey(r))
		}
	}
	slices.Sort(c.onlyBase)
	slices.Sort(c.onlyCur)
	c.paired = len(pairs)

	for _, def := range metricDefs {
		md := metricDelta{name: def.name}
		var deltas []float64
		for _, p := range pairs {
			if !def.counts(p[0]) || !def.counts(p[1]) {
				continue
			}
			bv, cv := def.value(p[0]), def.value(p[1])
			md.base += bv
			md.cur += cv
			d := cv - bv
			deltas = append(deltas, d)
			switch {
			case d > 0:
				md.wins++
			case d < 0:
				md.losses++
			default:
				md.ties++
			}
		}
		md.n = len(deltas)
		if md.n > 0 {
			md.base /= float64(md.n)
			md.cur /= float64(md.n)
			md.delta = md.cur - md.base
		}
		md.p = randomizationP(deltas, seed)
		c.metrics = append(c.metrics, md)
	}
	return c
}

// randomizationP is a two-sided paired randomization (sign-flip) test on the
// mean of deltas (Smucker, Allan & Carterette 2007). All-zero deltas give 1.
func randomizationP(deltas []float64, seed uint64) float64 {
	if len(deltas) == 0 {
		return 1
	}
	var observed float64
	for _, d := range deltas {
		observed += d
	}
	observed = math.Abs(observed)
	rng := rand.New(rand.NewPCG(seed, 0))
	extreme := 0
	for range randomizationIters {
		var s float64
		for _, d := range deltas {
			if rng.IntN(2) == 0 {
				s += d
			} else {
				s -= d
			}
		}
		// Sums of the same values in a different order can differ in the last bits.
		if math.Abs(s) >= observed-1e-9 {
			extreme++
		}
	}
	return float64(extreme+1) / float64(randomizationIters+1)
}

func printComparison(w io.Writer, c comparison) {
	if len(c.fingerprintChanged) > 0 {
		fmt.Fprintf(w, "WARNING: corpus changed since the baseline for %s; deltas mix data and search changes\n",
			strings.Join(c.fingerprintChanged, ", "))
	}
	fmt.Fprintf(w, "compared %d queries\n", c.paired)
	if len(c.onlyBase) > 0 {
		fmt.Fprintf(w, "only in baseline (excluded): %s\n", strings.Join(c.onlyBase, ", "))
	}
	if len(c.onlyCur) > 0 {
		fmt.Fprintf(w, "only in this run (excluded): %s\n", strings.Join(c.onlyCur, ", "))
	}
	fmt.Fprintf(w, "%-8s %4s %7s %7s %8s %4s %4s %4s %7s\n", "metric", "n", "base", "cur", "delta", "win", "loss", "tie", "p")
	for _, m := range c.metrics {
		if m.n == 0 {
			fmt.Fprintf(w, "%-8s %4d %7s %7s %8s %4s %4s %4s %7s\n", m.name, 0, "-", "-", "-", "-", "-", "-", "-")
			continue
		}
		fmt.Fprintf(w, "%-8s %4d %7.3f %7.3f %+8.3f %4d %4d %4d %7.4f\n", m.name, m.n, m.base, m.cur, m.delta, m.wins, m.losses, m.ties, m.p)
	}
	fmt.Fprintf(w, "zero-result queries: baseline %d, this run %d\n", c.zeroBase, c.zeroCur)
}
