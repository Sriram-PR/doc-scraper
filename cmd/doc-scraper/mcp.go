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

	pkglog "github.com/Sriram-PR/doc-scraper/v2/pkg/log"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/mcp"
)

func runMcpServer(args []string) {
	fs := flag.NewFlagSet("mcp-server", flag.ExitOnError)
	configFile := fs.String("config", "config.yaml", "Path to config file")
	logLevel := fs.String("loglevel", "info", "Log level (debug, info, warn, error)")

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
  describe_server Orientation manifest: sites + recent jobs (call this first)
  list_sites      List all configured sites
  get_page        Fetch a single URL as markdown
  crawl_site      Start a background crawl for a site
  get_job_status  Check the status of a crawl job
  cancel_crawl    Cancel a running or pending crawl job
  list_pages      Enumerate crawled pages for a site (paginated, metadata only)
  get_freshness   Report how stale a site's latest crawl is
  diff_crawl      Report pages added, removed, or changed since a timestamp
`)
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	exitCode := doMcpServer(*configFile, *logLevel, os.Stdout, os.Stderr)
	os.Exit(exitCode)
}

// doMcpServer is the testable implementation of the MCP server. The MCP
// protocol uses stdout, so logs always go to the supplied stderr writer.
func doMcpServer(configPath, logLevel string, _, stderr io.Writer) int {
	level, parseErr := pkglog.ParseLevel(logLevel)
	log := pkglog.New(level, pkglog.FormatText, stderr)
	if parseErr != nil {
		log.Warn(fmt.Sprintf("Invalid log level '%s', using default 'info'. Error: %v", logLevel, parseErr))
	}

	appCfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading config: %v\n", err)
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
	for _, w := range appWarnings {
		log.Warn(w)
	}

	serverCfg := &mcp.ServerConfig{
		AppConfig:  appCfg,
		ConfigPath: configPath,
		Logger:     log,
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(stderr, "Error during MCP server shutdown: %v\n", err)
	}

	// A cancelled context or closed stdin is a normal exit, not a failure.
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, io.EOF) {
		fmt.Fprintf(stderr, "MCP server error: %v\n", runErr)
		return 1
	}
	return 0
}
