package mcp

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sirupsen/logrus"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
)

const (
	serverName    = "doc-scraper"
	serverVersion = "2.2.0"
)

// ServerConfig holds configuration for the MCP server. Only the stdio
// transport is supported (Claude Desktop, Claude Code, Cursor). The older
// unauthenticated SSE transport was removed in v2.x because it exposed an
// unauthenticated crawl endpoint on a TCP port.
type ServerConfig struct {
	AppConfig  *config.AppConfig
	ConfigPath string
	Logger     *logrus.Logger
}

// Server wraps the MCP server with doc-scraper specific functionality
type Server struct {
	mcpServer  *server.MCPServer
	cfg        *ServerConfig
	log        *logrus.Entry
	jobManager *JobManager
}

// NewServer creates a new MCP server instance
func NewServer(cfg *ServerConfig) (*Server, error) {
	if cfg.AppConfig == nil {
		return nil, fmt.Errorf("AppConfig is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = logrus.New()
	}

	// Create the MCP server
	mcpServer := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithLogging(),
	)

	logEntry := cfg.Logger.WithField("component", "mcp")
	var jobsPath string
	if cfg.AppConfig.StateDir != "" {
		jobsPath = filepath.Join(cfg.AppConfig.StateDir, jobsFilename)
	} else {
		logEntry.Warn("state_dir is empty; MCP jobs will not persist across restarts")
	}
	s := &Server{
		mcpServer:  mcpServer,
		cfg:        cfg,
		log:        logEntry,
		jobManager: NewJobManager(jobsPath, logEntry),
	}

	// Register all tools
	s.registerTools()

	return s, nil
}

// registerTools registers all available MCP tools
func (s *Server) registerTools() {
	// list_sites - List all configured sites
	listSitesTool := mcp.NewTool("list_sites",
		mcp.WithDescription("List all configured sites available for crawling"),
	)
	s.mcpServer.AddTool(listSitesTool, s.handleListSites)

	// get_page - Fetch a single URL as markdown
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
	s.mcpServer.AddTool(getPageTool, s.handleGetPage)

	// crawl_site - Start a background crawl
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
	s.mcpServer.AddTool(crawlSiteTool, s.handleCrawlSite)

	// get_job_status - Check status of a crawl job
	getJobStatusTool := mcp.NewTool("get_job_status",
		mcp.WithDescription("Get the status of a crawl job"),
		mcp.WithString("job_id",
			mcp.Required(),
			mcp.Description("The job ID returned by crawl_site"),
		),
	)
	s.mcpServer.AddTool(getJobStatusTool, s.handleGetJobStatus)

	// cancel_crawl - Cancel a running crawl job
	cancelCrawlTool := mcp.NewTool("cancel_crawl",
		mcp.WithDescription("Cancel a running or pending crawl job by job ID. Has no effect on jobs already in a terminal state."),
		mcp.WithString("job_id",
			mcp.Required(),
			mcp.Description("The job ID returned by crawl_site"),
		),
	)
	s.mcpServer.AddTool(cancelCrawlTool, s.handleCancelCrawl)

	// describe_server - Orientation manifest; intended to be called first
	describeServerTool := mcp.NewTool("describe_server",
		mcp.WithDescription("Returns server identity, configured sites, and recent crawl jobs in one call. Call this first to orient yourself: it consolidates what would otherwise require list_sites plus several get_job_status calls. The MCP tool list is already advertised by the protocol so it is not duplicated here."),
	)
	s.mcpServer.AddTool(describeServerTool, s.handleDescribeServer)

	// list_pages - Enumerate crawled pages for a site
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
	s.mcpServer.AddTool(listPagesTool, s.handleListPages)

	s.log.Infof("Registered %d MCP tools", 7)
}

// Run starts the MCP server over the stdio transport.
func (s *Server) Run() error {
	s.log.Info("Starting MCP server with stdio transport")
	return server.ServeStdio(s.mcpServer)
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("Shutting down MCP server...")
	// Cancel any running jobs
	s.jobManager.CancelAll()
	// Flush and stop the persistence flusher
	s.jobManager.Stop()
	return nil
}
