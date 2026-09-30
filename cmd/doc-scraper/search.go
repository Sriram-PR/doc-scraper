package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

type searchArgs struct {
	configFile string
	siteKey    string
	limit      int
	jsonOut    bool
	query      string
}

// parseSearchArgs accepts flags before, between, or after the query words; a
// literal "--" ends flag parsing so queries may start with a dash.
func parseSearchArgs(args []string, out io.Writer) (searchArgs, error) {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(out)
	var a searchArgs
	fs.StringVar(&a.configFile, "config", "config.yaml", "Path to config file")
	fs.StringVar(&a.siteKey, "site", "", "Limit results to one site key (optional)")
	fs.IntVar(&a.limit, "limit", 10, "Maximum results to return (must be positive)")
	fs.BoolVar(&a.jsonOut, "json", false, "Emit results as a JSON array instead of human-readable text")
	fs.Usage = func() {
		fmt.Fprintf(out, "Usage: doc-scraper search [options] <query>\n\n"+
			"Ranked full-text search over the crawled corpus (BM25, stemming, FTS5 syntax).\n"+
			"Options may appear before or after the query; use -- to end option parsing.\n\nOptions:\n")
		fs.PrintDefaults()
	}

	var words []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return a, err
		}
		consumed := len(rest) - fs.NArg()
		rest = fs.Args()
		if consumed > 0 && args[len(args)-len(rest)-1] == "--" {
			words = append(words, rest...)
			break
		}
		if len(rest) > 0 {
			words = append(words, rest[0])
			rest = rest[1:]
		}
	}
	a.query = strings.TrimSpace(strings.Join(words, " "))
	if a.query == "" {
		fs.Usage()
		return a, errors.New("a query is required")
	}
	return a, nil
}

func runSearch(args []string) {
	a, err := parseSearchArgs(args, os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(doSearch(a.configFile, a.query, a.siteKey, a.limit, a.jsonOut, os.Stdout, os.Stderr))
}

func doSearch(configPath, query, siteKey string, limit int, jsonOut bool, stdout, stderr io.Writer) int {
	if limit <= 0 {
		fmt.Fprintf(stderr, "Error: -limit must be a positive integer, got %d\n", limit)
		return 1
	}
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
