// Command detect-bench measures pkg/detect against a battery of live
// documentation sites with labeled ground truth. Run it before a release to
// catch signature drift (frameworks redesigning their themes, sites migrating
// platforms):
//
//	go run ./tools/detect-bench
//
// It fetches each site once (roughly 230 requests) and prints a per-family
// scorecard plus every disagreement. Scoring is family-aware: any sphinx-*
// theme counts as a sphinx hit, and a js-shell verdict on an unknown-labeled
// site counts as correct. Showcase-derived labels rot as sites migrate; when
// a "miss" shows a generator meta contradicting the label, fix sites.json,
// not the detector.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/detect"
)

type benchSite struct {
	URL       string `json:"url"`
	Framework string `json:"framework"`
}

type benchOutcome struct {
	site       benchSite
	got        string
	confidence string
	generator  string
	fetchErr   string
	class      string
}

func main() {
	sitesPath := flag.String("sites", "tools/detect-bench/sites.json", "Path to the labeled site battery")
	concurrency := flag.Int("concurrency", 8, "Concurrent fetches")
	verbose := flag.Bool("v", false, "Print every site, not just disagreements")
	flag.Parse()

	data, err := os.ReadFile(*sitesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	var sites []benchSite
	if err := json.Unmarshal(data, &sites); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	results := make([]benchOutcome, len(sites))
	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup
	for i, s := range sites {
		wg.Add(1)
		go func(i int, s benchSite) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = evalSite(s)
		}(i, s)
	}
	wg.Wait()
	report(results, *verbose)
}

func evalSite(s benchSite) benchOutcome {
	o := benchOutcome{site: s}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		o.fetchErr = err.Error()
		return o
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) doc-scraper-detect-bench/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		o.fetchErr = err.Error()
		return o
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		o.fetchErr = fmt.Sprintf("status %d", resp.StatusCode)
		return o
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		o.fetchErr = err.Error()
		return o
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		o.fetchErr = "parse: " + err.Error()
		return o
	}
	o.generator, _ = doc.Find(`meta[name="generator"]`).First().Attr("content")
	r := detect.DetectPage(doc)
	o.got = string(r.Framework)
	o.confidence = string(r.Confidence)
	o.class = classify(s.Framework, o.got)
	return o
}

func family(f string) string {
	switch {
	case strings.HasPrefix(f, "sphinx"):
		return "sphinx"
	case strings.HasPrefix(f, "mkdocs"):
		return "mkdocs"
	case f == "docsify" || f == "swagger-ui" || f == "redoc" || f == "scalar" || f == "document360" || f == "js-shell":
		return "shell:" + f
	}
	return f
}

func classify(truth, got string) string {
	tf, gf := family(truth), family(got)
	switch {
	case tf == gf,
		strings.HasPrefix(tf, "shell:") && strings.HasPrefix(gf, "shell:"),
		truth == "unknown" && got == "js-shell":
		return "HIT"
	case truth == "unknown":
		return "OVER"
	case got == "unknown":
		return "UNDER"
	case got == "js-shell":
		return "SHELL"
	default:
		return "CONFUSE"
	}
}

func report(results []benchOutcome, verbose bool) {
	counts := map[string]int{}
	perTruth := map[string][2]int{}
	var fetched int
	for _, r := range results {
		if r.fetchErr != "" {
			counts["FETCH-FAIL"]++
			if verbose {
				fmt.Printf("FAIL     %-60s %s\n", r.site.URL, r.fetchErr)
			}
			continue
		}
		fetched++
		counts[r.class]++
		p := perTruth[family(r.site.Framework)]
		p[1]++
		if r.class == "HIT" {
			p[0]++
		}
		perTruth[family(r.site.Framework)] = p
		if r.class != "HIT" || verbose {
			fmt.Printf("%-8s truth=%-15s got=%-15s conf=%-11s gen=%-30q %s\n",
				r.class, r.site.Framework, r.got, r.confidence, truncate(r.generator, 30), r.site.URL)
		}
	}
	hitRate := 0.0
	if fetched > 0 {
		hitRate = 100 * float64(counts["HIT"]) / float64(fetched)
	}
	fmt.Printf("\nfetched %d/%d | HIT %d (%.1f%%) | UNDER %d | OVER %d | CONFUSE %d | SHELL %d | fetch-fail %d\n",
		fetched, len(results), counts["HIT"], hitRate,
		counts["UNDER"], counts["OVER"], counts["CONFUSE"], counts["SHELL"], counts["FETCH-FAIL"])

	keys := make([]string, 0, len(perTruth))
	for k := range perTruth {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("\nper-family:")
	for _, k := range keys {
		p := perTruth[k]
		fmt.Printf("  %-16s %d/%d\n", k, p[0], p[1])
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
