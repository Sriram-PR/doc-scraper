package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

const (
	serverName    = "doc-scraper"
	serverVersion = "2.7.0"
)

// ServerConfig holds configuration for the MCP server. Only the stdio
// transport is supported (Claude Desktop, Claude Code, Cursor). The older
// unauthenticated SSE transport was removed in v2.x because it exposed an
// unauthenticated crawl endpoint on a TCP port.
type ServerConfig struct {
	AppConfig  *config.AppConfig
	ConfigPath string
	Logger     *slog.Logger
}

// Server wraps the MCP server with doc-scraper specific functionality
type Server struct {
	mcpServer  *server.MCPServer
	cfg        *ServerConfig
	log        *slog.Logger
	jobManager *JobManager
	idx        *index.Index
}

func NewServer(cfg *ServerConfig) (*Server, error) {
	if cfg.AppConfig == nil {
		return nil, fmt.Errorf("AppConfig is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	mcpServer := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithLogging(),
	)

	logEntry := cfg.Logger.With("component", "mcp")
	var jobsPath string
	if cfg.AppConfig.StateDir != "" {
		jobsPath = filepath.Join(cfg.AppConfig.StateDir, jobsFilename)
	} else {
		logEntry.Warn("state_dir is empty; MCP jobs will not persist across restarts")
	}
	idx, err := index.OpenAt(cfg.AppConfig.StateDir, cfg.AppConfig.CrawlHistoryRetention, logEntry)
	if err != nil {
		return nil, fmt.Errorf("open crawl-history index: %w", err)
	}
	s := &Server{
		mcpServer:  mcpServer,
		cfg:        cfg,
		log:        logEntry,
		jobManager: NewJobManager(jobsPath, logEntry),
		idx:        idx,
	}

	s.registerTools()

	return s, nil
}

func (s *Server) registerTools() {
	toolCount := 0
	addTool := func(tool mcp.Tool, handler server.ToolHandlerFunc) {
		s.mcpServer.AddTool(tool, handler)
		toolCount++
	}

	listSitesTool := mcp.NewTool("list_sites",
		mcp.WithDescription("List all configured sites available for crawling"),
	)
	addTool(listSitesTool, s.handleListSites)

	getPageTool := mcp.NewTool("get_page",
		mcp.WithDescription("Fetch a single URL and return its content as markdown"),
		mcp.WithString("url",
			mcp.Required(),
			mcp.Description("The URL to fetch"),
		),
		mcp.WithString("content_selector",
			mcp.Description("CSS selector for main content (defaults to 'body')"),
		),
	)
	addTool(getPageTool, s.handleGetPage)

	crawlSiteTool := mcp.NewTool("crawl_site",
		mcp.WithDescription("Start a background crawl for a configured site. Returns immediately with a job ID."),
		mcp.WithString("site_key",
			mcp.Required(),
			mcp.Description("Site key from config file (e.g., 'langchain_py', 'rust_docs')"),
		),
		mcp.WithBoolean("incremental",
			mcp.Description("Enable incremental mode (skip unchanged pages)"),
		),
	)
	addTool(crawlSiteTool, s.handleCrawlSite)

	getJobStatusTool := mcp.NewTool("get_job_status",
		mcp.WithDescription("Get the status of a crawl job"),
		mcp.WithString("job_id",
			mcp.Required(),
			mcp.Description("The job ID returned by crawl_site"),
		),
	)
	addTool(getJobStatusTool, s.handleGetJobStatus)

	cancelCrawlTool := mcp.NewTool("cancel_crawl",
		mcp.WithDescription("Cancel a running or pending crawl job by job ID. Has no effect on jobs already in a terminal state."),
		mcp.WithString("job_id",
			mcp.Required(),
			mcp.Description("The job ID returned by crawl_site"),
		),
	)
	addTool(cancelCrawlTool, s.handleCancelCrawl)

	describeServerTool := mcp.NewTool("describe_server",
		mcp.WithDescription(
			"Returns server identity, configured sites, and recent crawl jobs in one call. "+
				"Call this first to orient yourself: it consolidates what would otherwise "+
				"require list_sites plus several get_job_status calls. The MCP tool list is "+
				"already advertised by the protocol so it is not duplicated here.",
		),
	)
	addTool(describeServerTool, s.handleDescribeServer)

	listPagesTool := mcp.NewTool("list_pages",
		mcp.WithDescription("List crawled pages for a site, paginated and sorted by URL. Returns metadata only (URL, title, depth, crawled_at, content_length); use get_page to fetch a specific page's full content."),
		mcp.WithString("site_key",
			mcp.Required(),
			mcp.Description("Site key from config (use list_sites to discover available keys)"),
		),
		mcp.WithNumber("max_results",
			mcp.Description("Maximum pages to return (default: 100, max: 1000)"),
		),
		mcp.WithNumber("offset",
			mcp.Description("Pagination offset, 0-based (default: 0)"),
		),
	)
	addTool(listPagesTool, s.handleListPages)

	getFreshnessTool := mcp.NewTool("get_freshness",
		mcp.WithDescription("Return the most recent crawl summary for a site "+
			"(last_crawl_started_at/ended_at, total_pages, mode, age_seconds) plus output/state "+
			"dir presence and any running job. Use this to decide whether to query the existing "+
			"crawl or run crawl_site first."),
		mcp.WithString("site_key",
			mcp.Required(),
			mcp.Description("Site key from config (use list_sites to discover available keys)"),
		),
	)
	addTool(getFreshnessTool, s.handleGetFreshness)

	diffCrawlTool := mcp.NewTool("diff_crawl",
		mcp.WithDescription("Return added/removed/changed pages between the latest crawl and "+
			"the most recent crawl whose crawl_ended_at <= since. Hash-based verdicts from the "+
			"SQLite history index. Pair with get_freshness: pass the last_crawl_ended_at value "+
			"as since after running crawl_site --incremental to see exactly what changed."),
		mcp.WithString("site_key",
			mcp.Required(),
			mcp.Description("Site key from config"),
		),
		mcp.WithString("since",
			mcp.Required(),
			mcp.Description("RFC3339 timestamp; the most recent crawl whose crawl_ended_at <= since is the baseline (e.g. 2026-05-23T22:00:00Z)"),
		),
		mcp.WithNumber("max_results",
			mcp.Description("Maximum diff entries to return (default 100, max 1000)"),
		),
		mcp.WithNumber("offset",
			mcp.Description("Pagination offset, 0-based"),
		),
	)
	addTool(diffCrawlTool, s.handleDiffCrawl)

	s.log.Info(fmt.Sprintf("Registered %d MCP tools", toolCount))
}

// Run serves the MCP stdio transport until ctx is cancelled or stdin closes.
// ServeStdio installs its own signal handler and swallows Shutdown, so the
// caller drives cancellation and cleanup explicitly via Listen instead.
func (s *Server) Run(ctx context.Context) error {
	s.log.Info("Starting MCP server with stdio transport")
	return server.NewStdioServer(s.mcpServer).Listen(ctx, os.Stdin, os.Stdout)
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("Shutting down MCP server...")
	s.jobManager.CancelAll()
	s.jobManager.Stop()
	if s.idx != nil {
		if err := s.idx.Close(); err != nil {
			s.log.Warn("error closing crawl-history index", "err", err)
		}
	}
	return nil
}
