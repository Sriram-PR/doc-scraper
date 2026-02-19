package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sirupsen/logrus"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
)

const (
	serverName    = "doc-scraper"
	serverVersion = "1.3.0"
)

// ServerConfig holds configuration for the MCP server
type ServerConfig struct {
	AppConfig  *config.AppConfig
	ConfigPath string
	Transport  string // "stdio" or "sse"
	Port       int
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

	s := &Server{
		mcpServer:  mcpServer,
		cfg:        cfg,
		log:        cfg.Logger.WithField("component", "mcp"),
		jobManager: NewJobManager(),
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

	// search_crawled - Search previously crawled content
	searchCrawledTool := mcp.NewTool("search_crawled",
		mcp.WithDescription("Search previously crawled content using text matching"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query (case-insensitive substring match)"),
		),
		mcp.WithString("site_key",
			mcp.Description("Limit search to specific site (optional)"),
		),
		mcp.WithNumber("max_results",
			mcp.Description("Maximum number of results to return (default: 10, max: 100)"),
		),
	)
	s.mcpServer.AddTool(searchCrawledTool, s.handleSearchCrawled)

	s.log.Infof("Registered %d MCP tools", 5)
}

// Run starts the MCP server with the configured transport
func (s *Server) Run() error {
	switch s.cfg.Transport {
	case "stdio":
		s.log.Info("Starting MCP server with stdio transport")
		return server.ServeStdio(s.mcpServer)
	case "sse":
		addr := fmt.Sprintf(":%d", s.cfg.Port)
		s.log.Infof("Starting MCP server with SSE transport on %s", addr)
		sseServer := server.NewSSEServer(s.mcpServer)
		return sseServer.Start(addr)
	default:
		return fmt.Errorf("unknown transport: %s (supported: stdio, sse)", s.cfg.Transport)
	}
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("Shutting down MCP server...")
	// Cancel any running jobs
	s.jobManager.CancelAll()
	return nil
}
