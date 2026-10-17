// Command search-bench scores the full-text search ranking (index.SearchChunks,
// behind both the search CLI and the search_docs MCP tool) against labeled
// queries over frozen crawls of real documentation sites:
//
//	go run ./tools/search-bench
//	go run ./tools/search-bench -out .audit/search-bench/runs/<date>-<label>.json
//	go run ./tools/search-bench -baseline .audit/search-bench/runs/<date>-baseline.json
//
// Corpora are not committed. Get them once, from the repo root, with
//
//	doc-scraper crawl -config tools/search-bench/crawl.yaml -all-sites
//
// which leaves .audit/search-bench/corpora/<site_key>/pages.jsonl. Each run
// indexes every corpus into its own temp index, so bm25 statistics come from
// that corpus alone. A recrawl changes the corpus: the run then fails if an
// expected URL is gone, and expected pages need re-checking either way.
//
// Queries live in tools/search-bench/queries/<site_key>.json. Relevance is
// judged per page: chunks collapse to their page's best rank, ties break by
// url then anchor. Scores never change the exit code.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "search-bench:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("search-bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	queriesDir := fs.String("queries", "tools/search-bench/queries", "directory of query files")
	corporaDir := fs.String("corpora", ".audit/search-bench/corpora", "directory of frozen corpora")
	site := fs.String("site", "", "run one site_key only")
	out := fs.String("out", "", "write the run to this JSON file")
	baseline := fs.String("baseline", "", "compare with this run file")
	seed := fs.Uint64("seed", 1, "randomization-test seed")
	verbose := fs.Bool("v", false, "print one line per query")
	if err := fs.Parse(args); err != nil {
		return err
	}

	files, err := filepath.Glob(filepath.Join(*queriesDir, "*.json"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no query files in %s", *queriesDir)
	}

	cur := runFile{Version: runFileVersion, Created: time.Now().UTC().Format(time.RFC3339), GitCommit: gitCommit(ctx)}
	fileBySite := make(map[string]string, len(files))
	for _, f := range files {
		qs, err := loadQueries(f)
		if err != nil {
			return err
		}
		if prev, ok := fileBySite[qs.SiteKey]; ok {
			return fmt.Errorf("%s: site_key %q is also used by %s", f, qs.SiteKey, prev)
		}
		fileBySite[qs.SiteKey] = f
		if *site != "" && qs.SiteKey != *site {
			continue
		}
		results, info, err := runSite(ctx, *corporaDir, qs)
		if err != nil {
			return err
		}
		cur.Queries = append(cur.Queries, results...)
		cur.Corpora = append(cur.Corpora, info)
	}
	if len(cur.Corpora) == 0 {
		return fmt.Errorf("no query file for site %q in %s", *site, *queriesDir)
	}

	printScorecard(stdout, cur.Queries, *verbose)
	if *out != "" {
		if err := writeRun(*out, cur); err != nil {
			return err
		}
	}
	if *baseline != "" {
		base, err := readRun(*baseline)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout)
		printComparison(stdout, compare(base, cur, *seed))
	}
	return nil
}

func runSite(ctx context.Context, corporaDir string, qs querySet) (results []queryResult, info corpusInfo, err error) {
	c, err := loadCorpus(ctx, corporaDir, qs.SiteKey)
	if err != nil {
		return nil, corpusInfo{}, err
	}
	defer func() { err = errors.Join(err, c.close()) }()

	if err := qs.validate(c.pages); err != nil {
		return nil, corpusInfo{}, err
	}
	for _, q := range qs.Queries {
		hits, err := c.search(ctx, q.Query)
		if errors.Is(err, index.ErrNoSearchableTerms) {
			return nil, corpusInfo{}, fmt.Errorf("%s %s: query %q has no searchable terms", qs.SiteKey, q.ID, q.Query)
		}
		if err != nil {
			return nil, corpusInfo{}, fmt.Errorf("%s %s: %w", qs.SiteKey, q.ID, err)
		}
		r := scoreQuery(hits, q.Expected)
		r.Site, r.ID, r.Query, r.Tags = qs.SiteKey, q.ID, q.Query, q.Tags
		results = append(results, r)
	}
	return results, corpusInfo{Site: qs.SiteKey, Fingerprint: c.fp}, nil
}
