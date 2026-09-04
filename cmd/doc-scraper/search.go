package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func runSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	configFile := fs.String("config", "config.yaml", "Path to config file")
	siteKey := fs.String("site", "", "Limit results to one site key (optional)")
	limit := fs.Int("limit", 10, "Maximum results to return")
	jsonOut := fs.Bool("json", false, "Emit results as a JSON array instead of human-readable text")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: doc-scraper search [options] <query>\n\n"+
			"Ranked full-text search over the crawled corpus (BM25, stemming, FTS5 syntax).\n\nOptions:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		fs.Usage()
		os.Exit(1)
	}
	os.Exit(doSearch(*configFile, query, *siteKey, *limit, *jsonOut, os.Stdout, os.Stderr))
}

func doSearch(configPath, query, siteKey string, limit int, jsonOut bool, stdout, stderr io.Writer) int {
	appCfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	if _, err := appCfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "Error: invalid config: %v\n", err)
		return 1
	}
	if siteKey != "" {
		if _, ok := appCfg.Sites[siteKey]; !ok {
			fmt.Fprintf(stderr, "Error: site '%s' not found in config\n", siteKey)
			return 1
		}
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	idx, err := index.OpenAt(appCfg.StateDir, appCfg.CrawlHistoryRetention, log)
	if err != nil {
		fmt.Fprintf(stderr, "Error: open index: %v\n", err)
		return 1
	}
	if idx == nil {
		fmt.Fprintln(stderr, "Error: state_dir is unset, so there is no search index")
		return 1
	}
	defer func() { _ = idx.Close() }()

	results, err := idx.SearchChunks(context.Background(), query, siteKey, limit)
	if err != nil {
		fmt.Fprintf(stderr, "Error: search: %v\n", err)
		return 1
	}

	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if results == nil {
			results = []index.SearchResult{}
		}
		if err := enc.Encode(results); err != nil {
			fmt.Fprintf(stderr, "Error: encode results: %v\n", err)
			return 1
		}
		return 0
	}

	if len(results) == 0 {
		fmt.Fprintf(stdout, "No matches for %q. Crawl a site first, or broaden the query.\n", query)
		return 0
	}
	for i, r := range results {
		title := r.Title
		if r.HeadingPath != "" && r.HeadingPath != r.Title {
			title += "  >  " + r.HeadingPath
		}
		link := r.URL
		if r.Anchor != "" {
			link += "#" + r.Anchor
		}
		fmt.Fprintf(stdout, "%2d. %s  (%s)\n    %s\n    %s\n", i+1, title, r.SiteKey, link, r.Snippet)
	}
	return 0
}
