package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/discover"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
)

const (
	addExitWritten = 0
	addExitError   = 1
	addExitDrafted = 2
)

type addOptions struct {
	configPath string
	rawURL     string
	siteKey    string
	selector   string
	depth      int
	yes        bool
	dryRun     bool
	jsonOut    bool
	isTTY      bool
}

func runAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "Path to YAML config file")
	siteKey := fs.String("site", "", "Site key to use instead of the derived one")
	selector := fs.String("selector", "", "Content CSS selector, skipping auto-detection")
	depth := fs.Int("depth", 0, "Override the proposed max_depth")
	yes := fs.Bool("yes", false, "Write the drafted entry without prompting")
	dryRun := fs.Bool("dry-run", false, "Show the draft and exit without writing (exit code 2)")
	jsonOut := fs.Bool("json", false, "Print the draft as JSON on stdout")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: doc-scraper add [options] <url>

Probes a documentation site, drafts a site entry for the config file, shows
what one extracted page looks like, and writes the entry after confirmation.

Options:
`)
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), `
Examples:
  doc-scraper add https://vitepress.dev/guide/what-is-vitepress
  doc-scraper add -config config.yaml -site vue_docs https://vuejs.org/guide/
  doc-scraper add -dry-run -json https://docs.example.com   # agent mode, no write
`)
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(addExitError)
	}
	opts := addOptions{
		configPath: *configPath,
		rawURL:     fs.Arg(0),
		siteKey:    *siteKey,
		selector:   *selector,
		depth:      *depth,
		yes:        *yes,
		dryRun:     *dryRun,
		jsonOut:    *jsonOut,
		isTTY:      stdinIsTerminal(os.Stdin),
	}
	os.Exit(doAdd(opts, os.Stdin, os.Stdout, os.Stderr))
}

// stdinIsTerminal reports whether f is an interactive terminal.
// ModeCharDevice is not enough: /dev/null is a character device.
func stdinIsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func doAdd(opts addOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := loadConfigForAdd(opts.configPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return addExitError
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	ua := cfg.DefaultUserAgent
	if ua == "" {
		ua = fetch.DefaultUserAgent()
	}
	d := &discover.Discoverer{
		Client:    fetch.NewClient(cfg.HTTPClientSettings, log),
		UserAgent: ua,
		Log:       log,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fmt.Fprintf(stderr, "Probing %s ...\n", opts.rawURL)
	report, err := d.Run(ctx, opts.rawURL)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return addExitError
	}

	draft := discover.BuildDraft(report, opts.selector)
	if opts.siteKey != "" {
		draft.SiteKey = opts.siteKey
	}
	if opts.depth > 0 {
		draft.Site.MaxDepth = opts.depth
	}
	if _, exists := cfg.Sites[draft.SiteKey]; exists {
		fmt.Fprintf(stderr, "Error: site key %q already exists in %s; pick another with -site\n", draft.SiteKey, opts.configPath)
		return addExitError
	}

	doc, pageURL := report.Doc, report.FinalURL
	if report.Sample != nil {
		doc, pageURL = report.Sample.Doc, report.Sample.URL
	}
	preview := discover.ExtractPreview(doc, pageURL, &draft.Site, log)
	preview.Sampled = report.Sample != nil
	entryYAML, err := config.RenderSiteEntry(draft.SiteKey, &draft.Site)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return addExitError
	}

	if opts.jsonOut {
		return finishAddJSON(opts, draft, preview, stdin, stdout, stderr)
	}
	printAddDraft(stdout, draft, preview, entryYAML)
	return finishAdd(opts, draft, stdin, stdout, stderr)
}

// loadConfigForAdd tolerates a missing config file: add creates it on write,
// and until then defaults are enough to build an HTTP client.
func loadConfigForAdd(path string) (*config.AppConfig, error) {
	cfg, err := loadConfig(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg = &config.AppConfig{}
	} else if err != nil {
		return nil, err
	}
	if _, err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func printAddDraft(stdout io.Writer, draft *discover.Draft, preview discover.Preview, entryYAML string) {
	version := ""
	if draft.Version != "" {
		version = " (" + draft.Version + ")"
	}
	fmt.Fprintf(stdout, "Detected: %s%s via %s, confidence %s\n", draft.Framework, version, draft.Source, draft.Confidence)
	if draft.PageCount > 0 {
		fmt.Fprintf(stdout, "Corpus:   ~%d pages (sitemap)\n", draft.PageCount)
	}
	fmt.Fprintf(stdout, "\nDrafted entry:\n\n%s\n", indentLines(entryYAML, "  "))
	for _, e := range draft.Evidence {
		fmt.Fprintf(stdout, "  # %s\n", e)
	}

	header := "the fetched page"
	if preview.Sampled {
		header = preview.URL + " (the seed page is a landing layout)"
	}
	fmt.Fprintf(stdout, "\nPreview of %s:\n", header)
	if preview.Err != "" {
		fmt.Fprintf(stdout, "  extraction failed: %s\n", preview.Err)
	} else {
		fmt.Fprintf(stdout, "  %d chars of markdown, %.0f%% of page text, code blocks %d/%d, %d headings\n",
			preview.Chars, preview.PageTextRatio*100, preview.CodeBlocksKept, preview.CodeBlocksSeen, preview.Headings)
		if preview.NavLeak {
			fmt.Fprintln(stdout, "  note: extracted content still contains nav/sidebar elements")
		}
		fmt.Fprintln(stdout)
		fmt.Fprint(stdout, indentLines(discover.FirstLines(preview.Markdown, 25), "  | "))
	}
	if len(draft.Warnings) > 0 {
		fmt.Fprintln(stdout)
		for _, w := range draft.Warnings {
			fmt.Fprintf(stdout, "WARN: %s\n", w)
		}
	}
}

func finishAdd(opts addOptions, draft *discover.Draft, stdin io.Reader, stdout, stderr io.Writer) int {
	switch {
	case opts.dryRun:
		fmt.Fprintln(stdout, "\nDry run: nothing written.")
		return addExitDrafted
	case opts.yes:
	case !opts.isTTY:
		fmt.Fprintln(stderr, "Error: no terminal for confirmation; re-run with -yes to write or -dry-run to only draft")
		return addExitError
	default:
		fmt.Fprintf(stdout, "\nAdd site %q to %s? [y/N] ", draft.SiteKey, opts.configPath)
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "Not written.")
			return addExitDrafted
		}
	}
	if err := config.InsertSite(opts.configPath, draft.SiteKey, &draft.Site); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return addExitError
	}
	fmt.Fprintf(stdout, "Added %q to %s. Crawl it with:\n  doc-scraper crawl -config %s -site %s\n",
		draft.SiteKey, opts.configPath, opts.configPath, draft.SiteKey)
	return addExitWritten
}

type addJSONResult struct {
	SiteKey    string           `json:"site_key"`
	Framework  string           `json:"framework"`
	Confidence string           `json:"confidence"`
	Source     string           `json:"signal_source"`
	Generator  string           `json:"generator,omitempty"`
	PageCount  int              `json:"sitemap_page_count,omitempty"`
	Config     addJSONSite      `json:"config"`
	Preview    discover.Preview `json:"preview"`
	Evidence   []string         `json:"evidence"`
	Warnings   []string         `json:"warnings"`
	Written    bool             `json:"written"`
}

type addJSONSite struct {
	StartURLs              []string `json:"start_urls"`
	AllowedDomain          string   `json:"allowed_domain"`
	AllowedPathPrefix      string   `json:"allowed_path_prefix"`
	ContentSelector        string   `json:"content_selector"`
	MaxDepth               int      `json:"max_depth"`
	DisallowedPathPatterns []string `json:"disallowed_path_patterns,omitempty"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func finishAddJSON(opts addOptions, draft *discover.Draft, preview discover.Preview, stdin io.Reader, stdout, stderr io.Writer) int {
	result := addJSONResult{
		SiteKey:    draft.SiteKey,
		Framework:  string(draft.Framework),
		Confidence: string(draft.Confidence),
		Source:     string(draft.Source),
		Generator:  draft.Version,
		PageCount:  draft.PageCount,
		Config: addJSONSite{
			StartURLs:              draft.Site.StartURLs,
			AllowedDomain:          draft.Site.AllowedDomain,
			AllowedPathPrefix:      draft.Site.AllowedPathPrefix,
			ContentSelector:        draft.Site.ContentSelector,
			MaxDepth:               draft.Site.MaxDepth,
			DisallowedPathPatterns: draft.Site.DisallowedPathPatterns,
		},
		Preview:  preview,
		Evidence: nonNil(draft.Evidence),
		Warnings: nonNil(draft.Warnings),
	}
	result.Preview.Markdown = discover.FirstLines(result.Preview.Markdown, 40)

	exit := addExitDrafted
	switch {
	case opts.yes && !opts.dryRun:
		if err := config.InsertSite(opts.configPath, draft.SiteKey, &draft.Site); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return addExitError
		}
		result.Written = true
		exit = addExitWritten
	case !opts.dryRun && !opts.isTTY:
		fmt.Fprintln(stderr, "Note: no terminal for confirmation; draft emitted without writing (use -yes to write)")
	case !opts.dryRun && opts.isTTY:
		return finishAddJSONInteractive(opts, draft, result, stdin, stdout, stderr)
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintln(stdout, string(out))
	return exit
}

func finishAddJSONInteractive(opts addOptions, draft *discover.Draft, result addJSONResult, stdin io.Reader, stdout, stderr io.Writer) int {
	fmt.Fprintf(stderr, "Add site %q to %s? [y/N] ", draft.SiteKey, opts.configPath)
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "y" || answer == "yes" {
		if err := config.InsertSite(opts.configPath, draft.SiteKey, &draft.Site); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return addExitError
		}
		result.Written = true
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintln(stdout, string(out))
	if result.Written {
		return addExitWritten
	}
	return addExitDrafted
}

func indentLines(s, pad string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString(pad + line + "\n")
	}
	return b.String()
}
