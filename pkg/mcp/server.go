package mcp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/version"
)

const serverName = "doc-scraper"

// The tool set is fixed at startup.
const toolsListCacheTTL = time.Hour

var serverVersion = version.Version

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
		server.WithMethodCacheHints(mcp.MethodToolsList, toolsListCacheTTL.Milliseconds(), mcp.CacheScopePrivate),
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
		mcp.WithTitleAnnotation("List configured sites"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription("List all configured sites available for crawling"),
		outputSchemaFor[listSitesResult](),
	)
	addTool(listSitesTool, s.handleListSites)

	getPageTool := mcp.NewTool("get_page",
		mcp.WithTitleAnnotation("Fetch a page live"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithDescription("Fetch a URL live over the network and return its content as markdown. "+
			"This is an on-demand fetch independent of any crawl: it does not read the stored crawl "+
			"output, and it ignores the site's configured content_selector and scope."),
		mcp.WithString("url",
			mcp.Required(),
			mcp.Description("The URL to fetch"),
		),
		mcp.WithString("content_selector",
			mcp.Description("CSS selector for main content (defaults to 'body')"),
		),
		outputSchemaFor[getPageResult](),
	)
	addTool(getPageTool, s.handleGetPage)

	crawlSiteTool := mcp.NewTool("crawl_site",
		mcp.WithTitleAnnotation("Start a crawl"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithDescription("Start a background crawl for a configured site. Returns immediately with a job ID. "+
			"The crawl builds in a staging area and replaces the stored corpus only when it completes, "+
			"so a cancelled or failed crawl leaves the previous corpus intact."),
		mcp.WithString("site_key",
			mcp.Required(),
			mcp.Description("Site key from config file (e.g., 'langchain_py', 'rust_docs')"),
		),
		mcp.WithBoolean("incremental",
			mcp.Description("Enable incremental mode (skip unchanged pages)"),
		),
		outputSchemaFor[crawlSiteResult](),
	)
	addTool(crawlSiteTool, s.handleCrawlSite)

	getJobStatusTool := mcp.NewTool("get_job_status",
		mcp.WithTitleAnnotation("Check a crawl job"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription("Get the status of a crawl job"),
		mcp.WithString("job_id",
			mcp.Required(),
			mcp.Description("The job ID returned by crawl_site"),
		),
		outputSchemaFor[jobSummary](),
	)
	addTool(getJobStatusTool, s.handleGetJobStatus)

	cancelCrawlTool := mcp.NewTool("cancel_crawl",
		mcp.WithTitleAnnotation("Cancel a crawl job"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription("Cancel a running or pending crawl job by job ID. The previously stored corpus and crawl history are left untouched. Has no effect on jobs already in a terminal state."),
		mcp.WithString("job_id",
			mcp.Required(),
			mcp.Description("The job ID returned by crawl_site"),
		),
		outputSchemaFor[cancelCrawlResult](),
	)
	addTool(cancelCrawlTool, s.handleCancelCrawl)

	s.registerCorpusTools(addTool)
	s.log.Info(fmt.Sprintf("Registered %d MCP tools", toolCount))
}

// registerCorpusTools adds the read-side tools that serve the stored crawl
// output and history index.
func (s *Server) registerCorpusTools(addTool func(mcp.Tool, server.ToolHandlerFunc)) {
	describeServerTool := mcp.NewTool("describe_server",
		mcp.WithTitleAnnotation("Describe this server"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription(
			"Returns server identity, configured sites, and recent crawl jobs in one call. "+
				"Call this first to orient yourself: it consolidates what would otherwise "+
				"require list_sites plus several get_job_status calls. The MCP tool list is "+
				"already advertised by the protocol so it is not duplicated here.",
		),
		outputSchemaFor[describeServerResult](),
	)
	addTool(describeServerTool, s.handleDescribeServer)

	listPagesTool := mcp.NewTool("list_pages",
		mcp.WithTitleAnnotation("List crawled pages"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription("List crawled pages for a site, paginated and sorted by URL. Returns "+
			"metadata only (URL, title, depth, crawled_at, content_length). Pass any URL returned here "+
			"to read_page to get its stored markdown; get_page re-fetches a URL live rather than "+
			"returning the crawled copy."),
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
		outputSchemaFor[listPagesResult](),
	)
	addTool(listPagesTool, s.handleListPages)

	readPageTool := mcp.NewTool("read_page",
		mcp.WithTitleAnnotation("Read a crawled page"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription("Return a page's markdown from the stored crawl output, without any "+
			"network access. This is the counterpart to get_page: read_page serves the crawled copy "+
			"that already had the site's content_selector applied, while get_page re-fetches the URL "+
			"live. Use list_pages to discover URLs, then read_page to read them. Large pages are "+
			"truncated at max_bytes; follow next_offset to read the rest."),
		mcp.WithString("site_key",
			mcp.Required(),
			mcp.Description("Site key from config (use list_sites to discover available keys)"),
		),
		mcp.WithString("url",
			mcp.Required(),
			mcp.Description("URL of a crawled page, as reported by list_pages"),
		),
		mcp.WithNumber("max_bytes",
			mcp.Description("Maximum content bytes to return (default: 102400, max: 1048576)"),
		),
		mcp.WithNumber("offset",
			mcp.Description("Byte offset into the page content, for reading a truncated page in parts (default: 0)"),
		),
		outputSchemaFor[readPageResult](),
	)
	addTool(readPageTool, s.handleReadPage)

	searchDocsTool := mcp.NewTool("search_docs",
		mcp.WithTitleAnnotation("Search crawled docs"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription("Full-text search across all crawled documentation, ranked by relevance "+
			"(BM25 with stemming), with zero network access. Results carry the page URL, its section "+
			"heading path, and a snippet with match terms marked [like this]; follow up with read_page "+
			"for the full page. Supports FTS5 syntax: quoted phrases, OR, and trailing * for prefix "+
			"matching. Searches the stored index only; run crawl_site first for uncrawled sites."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search terms. Plain words are ANDed; when too few results contain every word, "+
				"the rest are filled with partial matches (match: partial). \"quoted phrases\", OR, and prefix* "+
				"work too and are never relaxed."),
		),
		mcp.WithString("site_key",
			mcp.Description("Limit results to one site (use list_sites to discover keys). Omit to search all sites."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum results to return (default: 10, max: 50)"),
		),
		outputSchemaFor[searchDocsResult](),
	)
	addTool(searchDocsTool, s.handleSearchDocs)

	getFreshnessTool := mcp.NewTool("get_freshness",
		mcp.WithTitleAnnotation("Check crawl freshness"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription("Return the most recent crawl summary for a site "+
			"(last_crawl with its start and end times, total_pages and mode, plus age_seconds), output/state "+
			"dir presence and any running job. Use this to decide whether to query the existing "+
			"crawl or run crawl_site first."),
		mcp.WithString("site_key",
			mcp.Required(),
			mcp.Description("Site key from config (use list_sites to discover available keys)"),
		),
		outputSchemaFor[freshnessResult](),
	)
	addTool(getFreshnessTool, s.handleGetFreshness)

	diffCrawlTool := mcp.NewTool("diff_crawl",
		mcp.WithTitleAnnotation("Diff crawls over time"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithDescription("Return added/removed/changed pages between the latest crawl and "+
			"the most recent crawl whose crawl_ended_at <= since. Hash-based verdicts from the "+
			"SQLite history index. Pair with get_freshness: pass its last_crawl.crawl_ended_at value "+
			"as since after running crawl_site with incremental set to true to see exactly what changed."),
		mcp.WithString("site_key",
			mcp.Required(),
			mcp.Description("Site key from config"),
		),
		mcp.WithString("since",
			mcp.Required(),
			mcp.Description("RFC3339 timestamp; the most recent crawl whose crawl_ended_at <= since, compared to the second, is the baseline (e.g. 2026-05-23T22:00:00Z)"),
		),
		mcp.WithNumber("max_results",
			mcp.Description("Maximum diff entries to return (default 100, max 1000)"),
		),
		mcp.WithNumber("offset",
			mcp.Description("Pagination offset, 0-based"),
		),
		outputSchemaFor[diffCrawlResult](),
	)
	addTool(diffCrawlTool, s.handleDiffCrawl)
}

// Run serves the MCP stdio transport until ctx is cancelled or stdin closes.
// ServeStdio installs its own signal handler and swallows Shutdown, so the
// caller drives cancellation and cleanup explicitly via Listen instead.
func (s *Server) Run(ctx context.Context) error {
	s.log.Info("Starting MCP server with stdio transport")
	go s.backfillChunks(ctx)
	return s.serve(ctx, os.Stdin, os.Stdout)
}

func (s *Server) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	d := newStdioDrain(ctx, stdinEOFDrainTimeout, s.log)
	return server.NewStdioServer(s.mcpServer).Listen(ctx, d.reader(in), d.writer(out))
}

// backfillChunks indexes existing crawl output for sites that predate the
// full-text index, so search_docs works after an upgrade without re-crawling.
// It runs in the background at startup; sites already chunked are skipped via
// content-hash comparison inside IndexChunksFromJSONL.
func (s *Server) backfillChunks(ctx context.Context) {
	if s.idx == nil {
		return
	}
	for key, siteCfg := range s.cfg.AppConfig.Sites {
		if ctx.Err() != nil {
			return
		}
		jsonlPath, enabled := s.siteJSONLPath(key, siteCfg)
		if !enabled {
			continue
		}
		has, err := s.idx.SiteHasChunks(ctx, key)
		if err != nil {
			s.log.Warn("chunk backfill: check failed", "site", key, "err", err)
			continue
		}
		if has {
			continue
		}
		if err := crawler.IndexChunksFromJSONL(ctx, s.idx, key, jsonlPath, s.log); err != nil {
			s.log.Warn("chunk backfill failed", "site", key, "err", err)
		}
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("Shutting down MCP server...")
	s.jobManager.CancelAll()
	// Cancelled crawls still close their stores and finish index writes.
	if err := s.jobManager.WaitIdle(ctx); err != nil {
		s.log.Warn("crawls still running at shutdown", "err", err)
	}
	s.jobManager.Stop()
	if s.idx != nil {
		if err := s.idx.Close(); err != nil {
			s.log.Warn("error closing crawl-history index", "err", err)
		}
	}
	return nil
}
