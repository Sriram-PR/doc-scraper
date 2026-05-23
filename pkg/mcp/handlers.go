package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
)

// handleListSites handles the list_sites tool
func (s *Server) handleListSites(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sites := make([]map[string]interface{}, 0, len(s.cfg.AppConfig.Sites))

	// Get sorted keys for consistent output
	keys := make([]string, 0, len(s.cfg.AppConfig.Sites))
	for k := range s.cfg.AppConfig.Sites {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		siteCfg := s.cfg.AppConfig.Sites[key]
		siteInfo := map[string]interface{}{
			"key":              key,
			"domain":           siteCfg.AllowedDomain,
			"path_prefix":      siteCfg.AllowedPathPrefix,
			"start_urls_count": len(siteCfg.StartURLs),
			"max_depth":        siteCfg.MaxDepth,
		}

		// Check for last crawl info from metadata file
		lastCrawled := s.getLastCrawledTime(key, siteCfg)
		if !lastCrawled.IsZero() {
			siteInfo["last_crawled"] = lastCrawled.Format(time.RFC3339)
		}

		// Check if currently running
		if s.jobManager.IsRunning(key) {
			siteInfo["status"] = "running"
		}

		sites = append(sites, siteInfo)
	}

	result := map[string]interface{}{
		"sites":       sites,
		"config_path": s.cfg.ConfigPath,
		"total_sites": len(sites),
	}

	return mcp.NewToolResultText(formatJSON(result)), nil
}

// handleCancelCrawl handles the cancel_crawl tool. Wires the existing
// JobManager.CancelJob through MCP so agents can reclaim resources when a
// crawl was started by mistake or is no longer wanted. Returns cancelled=false
// for unknown jobs and for jobs already in a terminal state (completed,
// failed, already-cancelled), with a status field so the agent can tell which
// case occurred.
func (s *Server) handleCancelCrawl(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	jobID := request.GetString("job_id", "")
	if jobID == "" {
		return mcp.NewToolResultError("job_id parameter is required"), nil
	}

	job := s.jobManager.GetJob(jobID)
	if job == nil {
		result := map[string]interface{}{
			"job_id":    jobID,
			"cancelled": false,
			"message":   "job not found",
		}
		return mcp.NewToolResultText(formatJSON(result)), nil
	}

	cancelled := s.jobManager.CancelJob(jobID)
	// Re-read post-cancel to pick up the updated status/completed_at if any.
	job = s.jobManager.GetJob(jobID)

	result := map[string]interface{}{
		"job_id":    jobID,
		"cancelled": cancelled,
		"site_key":  job.SiteKey,
		"status":    job.Status,
	}
	if cancelled {
		result["message"] = "job cancelled"
	} else {
		result["message"] = fmt.Sprintf("job is in terminal state %q and cannot be cancelled", job.Status)
	}
	return mcp.NewToolResultText(formatJSON(result)), nil
}

// handleDescribeServer handles the describe_server tool. It returns a single
// orientation payload an agent can fetch on connection to discover what this
// server can do without having to chain list_sites + N x get_job_status. The
// MCP protocol already advertises tool schemas, so this response intentionally
// does NOT duplicate them; it provides the dynamic info (sites + jobs) plus a
// small server identity block.
func (s *Server) handleDescribeServer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	siteKeys := make([]string, 0, len(s.cfg.AppConfig.Sites))
	for k := range s.cfg.AppConfig.Sites {
		siteKeys = append(siteKeys, k)
	}
	sort.Strings(siteKeys)

	sites := make([]map[string]interface{}, 0, len(siteKeys))
	for _, key := range siteKeys {
		siteCfg := s.cfg.AppConfig.Sites[key]
		entry := map[string]interface{}{
			"key":    key,
			"domain": siteCfg.AllowedDomain,
		}
		if lastCrawled := s.getLastCrawledTime(key, siteCfg); !lastCrawled.IsZero() {
			entry["last_crawled"] = lastCrawled.Format(time.RFC3339)
		}
		if s.jobManager.IsRunning(key) {
			entry["status"] = "running"
		}
		sites = append(sites, entry)
	}

	// Recent jobs: sort newest first by StartedAt, cap at 20 so the orientation
	// payload stays small even after a long-running server has run many crawls.
	allJobs := s.jobManager.ListJobs()
	sort.Slice(allJobs, func(i, j int) bool {
		return allJobs[i].StartedAt.After(allJobs[j].StartedAt)
	})
	const maxJobs = 20
	if len(allJobs) > maxJobs {
		allJobs = allJobs[:maxJobs]
	}
	jobs := make([]map[string]interface{}, 0, len(allJobs))
	for _, j := range allJobs {
		entry := map[string]interface{}{
			"job_id":          j.ID,
			"site_key":        j.SiteKey,
			"status":          j.Status,
			"started_at":      j.StartedAt.Format(time.RFC3339),
			"pages_processed": j.PagesProcessed,
		}
		if !j.CompletedAt.IsZero() {
			entry["completed_at"] = j.CompletedAt.Format(time.RFC3339)
		}
		if j.ErrorMessage != "" {
			entry["error_message"] = j.ErrorMessage
		}
		jobs = append(jobs, entry)
	}

	result := map[string]interface{}{
		"server": map[string]interface{}{
			"name":        serverName,
			"version":     serverVersion,
			"config_path": s.cfg.ConfigPath,
		},
		"sites":         sites,
		"recent_jobs":   jobs,
		"total_sites":   len(sites),
		"total_jobs":    len(jobs),
		"jobs_capped":   len(s.jobManager.ListJobs()) > maxJobs,
		"next_actions":  "Use list_sites for full site config, list_pages to enumerate crawled pages, crawl_site to start a crawl, get_job_status to check a job, get_page to fetch a single URL.",
	}
	return mcp.NewToolResultText(formatJSON(result)), nil
}

// pageListEntry is the per-page metadata shape returned by handleListPages.
// Kept narrow on purpose: full page content is intentionally omitted (use
// get_page for that), so the response stays small even for sites with
// thousands of pages.
type pageListEntry struct {
	URL           string `json:"url"`
	Title         string `json:"title"`
	Depth         int    `json:"depth"`
	CrawledAt     string `json:"crawled_at"`
	ContentLength int    `json:"content_length"`
}

// handleListPages handles the list_pages tool. Returns a paginated list of
// crawled pages for a site, sorted by URL for deterministic output. Reads
// from the site's JSONL output; returns an empty result with a hint when the
// site has never been crawled.
func (s *Server) handleListPages(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	siteKey := request.GetString("site_key", "")
	if siteKey == "" {
		return mcp.NewToolResultError("site_key parameter is required"), nil
	}
	siteCfg, exists := s.cfg.AppConfig.Sites[siteKey]
	if !exists {
		availableKeys := make([]string, 0, len(s.cfg.AppConfig.Sites))
		for k := range s.cfg.AppConfig.Sites {
			availableKeys = append(availableKeys, k)
		}
		sort.Strings(availableKeys)
		return mcp.NewToolResultError(fmt.Sprintf("site '%s' not found. Available sites: %v", siteKey, availableKeys)), nil
	}

	maxResults := request.GetInt("max_results", 100)
	if maxResults <= 0 {
		maxResults = 100
	}
	if maxResults > 1000 {
		maxResults = 1000
	}
	offset := request.GetInt("offset", 0)
	if offset < 0 {
		offset = 0
	}

	jsonlPath := filepath.Join(
		s.cfg.AppConfig.OutputBaseDir,
		siteCfg.AllowedDomain,
		config.GetEffectiveJSONLOutputFilename(siteCfg, s.cfg.AppConfig),
	)

	file, err := os.Open(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			response := map[string]interface{}{
				"site_key":    siteKey,
				"pages":       []pageListEntry{},
				"total":       0,
				"offset":      offset,
				"max_results": maxResults,
				"returned":    0,
				"message":     "No crawl output found for this site. Run crawl_site first.",
			}
			return mcp.NewToolResultText(formatJSON(response)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("failed to open JSONL for site '%s': %v", siteKey, err)), nil
	}
	defer file.Close()

	pages := make([]pageListEntry, 0, 256)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Cheap discriminator before Unmarshal; skips the crawl_meta footer.
		if !strings.Contains(line, `"record_type":"page"`) {
			continue
		}
		var p models.PageJSONL
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			continue
		}
		if p.RecordType != models.RecordTypePage {
			continue
		}
		pages = append(pages, pageListEntry{
			URL:           p.URL,
			Title:         p.Title,
			Depth:         p.Depth,
			CrawledAt:     p.CrawledAt,
			ContentLength: len(p.Content),
		})
	}
	if err := scanner.Err(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to scan JSONL for site '%s': %v", siteKey, err)), nil
	}

	sort.Slice(pages, func(i, j int) bool { return pages[i].URL < pages[j].URL })

	total := len(pages)
	var pageSlice []pageListEntry
	if offset >= total {
		pageSlice = []pageListEntry{}
	} else {
		end := offset + maxResults
		if end > total {
			end = total
		}
		pageSlice = pages[offset:end]
	}

	response := map[string]interface{}{
		"site_key":    siteKey,
		"pages":       pageSlice,
		"total":       total,
		"offset":      offset,
		"max_results": maxResults,
		"returned":    len(pageSlice),
	}
	return mcp.NewToolResultText(formatJSON(response)), nil
}

// handleGetPage handles the get_page tool
func (s *Server) handleGetPage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	urlStr := request.GetString("url", "")
	if urlStr == "" {
		return mcp.NewToolResultError("url parameter is required"), nil
	}

	contentSelector := request.GetString("content_selector", "body")

	// Parse URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid URL: %v", err)), nil
	}

	startTime := time.Now()

	// Create HTTP client and fetch
	client := fetch.NewClient(s.cfg.AppConfig.HTTPClientSettings, s.log)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create request: %v", err)), nil
	}

	userAgent := s.cfg.AppConfig.DefaultUserAgent
	if userAgent == "" {
		userAgent = "github.com/Sriram-PR/doc-scraper/1.0"
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to fetch URL: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(fmt.Sprintf("HTTP error: %d %s", resp.StatusCode, resp.Status)), nil
	}

	// Read body
	const maxPageSize = 50 * 1024 * 1024 // 50 MB
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxPageSize))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read response: %v", err)), nil
	}

	// Parse HTML
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(bodyBytes))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse HTML: %v", err)), nil
	}

	// Extract title
	title := strings.TrimSpace(doc.Find("title").First().Text())
	if title == "" {
		title = "Untitled"
	}

	// Extract content using selector
	contentSelection := doc.Find(contentSelector)
	if contentSelection.Length() == 0 {
		return mcp.NewToolResultError(fmt.Sprintf("content selector '%s' not found on page", contentSelector)), nil
	}

	// Convert HTML content to markdown
	contentHTML, err := contentSelection.First().Html()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to extract HTML content: %v", err)), nil
	}
	converter := md.NewConverter("", true, nil)
	content, err := converter.ConvertString(contentHTML)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to convert to markdown: %v", err)), nil
	}
	content = strings.TrimSpace(content)

	// Calculate metrics
	fetchTimeMs := time.Since(startTime).Milliseconds()

	result := map[string]interface{}{
		"url":            parsedURL.String(),
		"title":          title,
		"content":        content,
		"content_length": len(content),
		"fetch_time_ms":  fetchTimeMs,
	}

	return mcp.NewToolResultText(formatJSON(result)), nil
}

// handleCrawlSite handles the crawl_site tool
func (s *Server) handleCrawlSite(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	siteKey := request.GetString("site_key", "")
	if siteKey == "" {
		return mcp.NewToolResultError("site_key parameter is required"), nil
	}

	incremental := request.GetBool("incremental", false)

	// Check if site exists
	siteCfg, exists := s.cfg.AppConfig.Sites[siteKey]
	if !exists {
		availableKeys := make([]string, 0, len(s.cfg.AppConfig.Sites))
		for k := range s.cfg.AppConfig.Sites {
			availableKeys = append(availableKeys, k)
		}
		return mcp.NewToolResultError(fmt.Sprintf("site '%s' not found. Available sites: %v", siteKey, availableKeys)), nil
	}

	// Check if already running
	if s.jobManager.IsRunning(siteKey) {
		existingJob := s.jobManager.GetJobBySite(siteKey)
		result := map[string]interface{}{
			"status":   "already_running",
			"message":  "A crawl is already in progress for this site",
			"job_id":   existingJob.ID,
			"site_key": siteKey,
		}
		return mcp.NewToolResultText(formatJSON(result)), nil
	}

	// Create job
	job, err := s.jobManager.CreateJob(siteKey, incremental)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create job: %v", err)), nil
	}

	// Start crawl in background
	go s.runCrawlJob(job, siteCfg, siteKey)

	result := map[string]interface{}{
		"status":      "started",
		"message":     "Crawl started successfully",
		"job_id":      job.ID,
		"site_key":    siteKey,
		"incremental": incremental,
	}

	return mcp.NewToolResultText(formatJSON(result)), nil
}

// handleGetJobStatus handles the get_job_status tool
func (s *Server) handleGetJobStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	jobID := request.GetString("job_id", "")
	if jobID == "" {
		return mcp.NewToolResultError("job_id parameter is required"), nil
	}

	job := s.jobManager.GetJob(jobID)
	if job == nil {
		return mcp.NewToolResultError(fmt.Sprintf("job '%s' not found", jobID)), nil
	}

	result := map[string]interface{}{
		"job_id":          job.ID,
		"site_key":        job.SiteKey,
		"status":          job.Status,
		"started_at":      job.StartedAt.Format(time.RFC3339),
		"pages_processed": job.PagesProcessed,
		"pages_queued":    job.PagesQueued,
		"incremental":     job.Incremental,
	}

	if !job.CompletedAt.IsZero() {
		result["completed_at"] = job.CompletedAt.Format(time.RFC3339)
		result["duration_seconds"] = job.CompletedAt.Sub(job.StartedAt).Seconds()
	}

	if job.ErrorMessage != "" {
		result["error_message"] = job.ErrorMessage
	}

	return mcp.NewToolResultText(formatJSON(result)), nil
}

// runCrawlJob runs a crawl job in the background
func (s *Server) runCrawlJob(job *Job, siteCfg *config.SiteConfig, siteKey string) {
	s.jobManager.UpdateStatus(job.ID, JobStatusRunning, "")

	jobCtx := s.jobManager.GetContext(job.ID)

	// Create crawler components
	httpClient := fetch.NewClient(s.cfg.AppConfig.HTTPClientSettings, s.log)
	fetcher := fetch.NewFetcher(httpClient, s.cfg.AppConfig, s.log)
	rateLimiter := fetch.NewRateLimiter(s.cfg.AppConfig.DefaultDelayPerHost, s.log)

	// Open store (always fresh for MCP jobs, never resume)
	store, err := storage.NewBadgerStore(jobCtx, s.cfg.AppConfig.StateDir, siteCfg.AllowedDomain, false, s.log)
	if err != nil {
		s.jobManager.UpdateStatus(job.ID, JobStatusFailed, fmt.Sprintf("failed to open store: %v", err))
		return
	}
	defer store.Close()
	go store.RunGC(jobCtx, 0) // 0 = use the store's built-in default (10m)

	// Set incremental mode
	appCfgCopy := *s.cfg.AppConfig
	if job.Incremental {
		appCfgCopy.EnableIncremental = true
	}

	// Create crawler
	crawlerCtx, cancelCrawl := context.WithCancel(jobCtx)
	defer cancelCrawl()

	jobID := job.ID
	crawlerInstance, err := crawler.NewCrawlerWithOptions(
		&appCfgCopy,
		siteCfg,
		siteKey,
		s.log,
		store,
		fetcher,
		rateLimiter,
		crawlerCtx,
		cancelCrawl,
		false, // not resume
		&crawler.CrawlerOptions{
			ProgressCallback: func(processed, queued int64) {
				s.jobManager.UpdateProgress(jobID, processed, queued)
			},
		},
	)
	if err != nil {
		s.jobManager.UpdateStatus(job.ID, JobStatusFailed, fmt.Sprintf("failed to create crawler: %v", err))
		return
	}

	// Run crawler
	if err := crawlerInstance.Run(false); err != nil {
		if errors.Is(err, context.Canceled) {
			s.jobManager.UpdateStatus(job.ID, JobStatusCancelled, "")
		} else {
			s.jobManager.UpdateStatus(job.ID, JobStatusFailed, err.Error())
		}
		return
	}

	s.jobManager.UpdateStatus(job.ID, JobStatusCompleted, "")
}

// getLastCrawledTime returns the end time of the most recent crawl by scanning
// the site's JSONL output for crawl_meta records. The last such record in the
// file is authoritative (a resumed crawl appends a fresh one). Returns the zero
// time if the file is absent or contains no crawl_meta record.
func (s *Server) getLastCrawledTime(_ string, siteCfg *config.SiteConfig) time.Time {
	siteOutputDir := filepath.Join(s.cfg.AppConfig.OutputBaseDir, siteCfg.AllowedDomain)
	jsonlPath := filepath.Join(siteOutputDir, config.GetEffectiveJSONLOutputFilename(siteCfg, s.cfg.AppConfig))

	file, err := os.Open(jsonlPath)
	if err != nil {
		return time.Time{}
	}
	defer file.Close()

	var lastEnded time.Time
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, models.RecordTypeCrawlMeta) {
			continue
		}
		var meta models.CrawlMetaJSONL
		if err := json.Unmarshal([]byte(line), &meta); err != nil {
			continue
		}
		if meta.RecordType != models.RecordTypeCrawlMeta {
			continue
		}
		if t, err := time.Parse(time.RFC3339, meta.CrawlEndedAt); err == nil {
			lastEnded = t
		}
	}
	return lastEnded
}

// formatJSON formats data as an indented JSON string
func formatJSON(data map[string]interface{}) string {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\": %q}", err.Error())
	}
	return string(b)
}
