package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	pkglog "github.com/Sriram-PR/doc-scraper/v2/pkg/log"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/mcp"
)

func runMcpServer(args []string) {
	fs := flag.NewFlagSet("mcp-server", flag.ExitOnError)
	configFile := fs.String("config", "config.yaml", "Path to config file")
	logLevel := fs.String("loglevel", "info", "Log level (debug, info, warn, error)")
	allowWrites := fs.Bool("allow-config-writes", false, "Let MCP clients save site entries to the config file (save_site)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: doc-scraper mcp-server [options]

Start an MCP (Model Context Protocol) server for AI tool integration.
Uses the stdio transport (compatible with Claude Desktop, Claude Code, Cursor).

Options:
`)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Example:
  doc-scraper mcp-server -config config.yaml

Available MCP Tools:
  describe_server Orientation manifest: server identity, sites, and recent jobs (call this first)
  list_sites      List all configured sites
  get_page        Fetch a single URL live over the network as markdown
  crawl_site      Start a background crawl for a site
  get_job_status  Check the status of a crawl job, optionally waiting for it to finish
  cancel_crawl    Cancel a running or pending crawl job
  draft_site      Draft a site entry for a docs URL or a configured site and preview one page, no config write
  save_site       Save a site entry to the config file and load it (needs -allow-config-writes)
  list_pages      Enumerate crawled pages for a site (paginated, metadata only)
  read_page       Return a crawled page's markdown from stored output, no network access
  search_docs     Ranked full-text search across crawled docs with snippets and section anchors, no network access
  get_freshness   Report how stale a site's latest crawl is
  diff_crawl      Report pages added, removed, or changed since the previous crawl or a timestamp
`)
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	rejectExtraArgs(fs)

	exitCode := doMcpServer(*configFile, *logLevel, *allowWrites, os.Stdout, os.Stderr)
	os.Exit(exitCode)
}

// doMcpServer is the testable implementation of the MCP server. The MCP
// protocol uses stdout, so logs always go to the supplied stderr writer.
func doMcpServer(configPath, logLevel string, allowWrites bool, _, stderr io.Writer) int {
	level, parseErr := pkglog.ParseLevel(logLevel)
	log := pkglog.New(level, pkglog.FormatText, stderr)
	if parseErr != nil {
		log.Warn(fmt.Sprintf("Invalid log level '%s', using default 'info'. Error: %v", logLevel, parseErr))
	}

	appCfg, unknownKeys, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading config: %v\n", err)
		return 1
	}
	if len(appCfg.Sites) == 0 && !allowWrites {
		fmt.Fprintf(stderr, "Error: %v in %s; run 'doc-scraper add <url>' to add one, or start with -allow-config-writes and add one with draft_site and save_site\n", errNoSites, configPath)
		return 1
	}
	// Validate applies the same defaults (e.g. max_requests) the crawl/watch
	// commands rely on. Skipping it leaves the global request semaphore at
	// capacity 0, which hangs every crawl_site fetch.
	appWarnings, err := appCfg.Validate()
	if err != nil {
		fmt.Fprintf(stderr, "Error validating config: %v\n", err)
		return 1
	}
	for _, w := range append(appWarnings, unknownKeys...) {
		log.Warn(w)
	}

	serverCfg := &mcp.ServerConfig{
		AppConfig:         appCfg,
		ConfigPath:        configPath,
		Logger:            log,
		AllowConfigWrites: allowWrites,
	}

	server, err := mcp.NewServer(serverCfg)
	if err != nil {
		fmt.Fprintf(stderr, "Error creating MCP server: %v\n", err)
		return 1
	}

	log.Info("Starting MCP server (stdio transport)")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runErr := server.Run(ctx)

	// Bound cleanup so a stuck Shutdown can't hang the process. The context
	// only bounds the wait for cancelled crawls, so also race it against a timer.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelShutdown()
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Shutdown(shutdownCtx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		fmt.Fprintln(stderr, "MCP server shutdown timed out; exiting")
	}

	// A cancelled context or closed stdin is a normal exit, not a failure.
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, io.EOF) {
		fmt.Fprintf(stderr, "MCP server error: %v\n", runErr)
		return 1
	}
	return 0
}
