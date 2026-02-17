package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	"gopkg.in/yaml.v3"

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
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
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

// handleSearchCrawled handles the search_crawled tool
func (s *Server) handleSearchCrawled(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query := request.GetString("query", "")
	if query == "" {
		return mcp.NewToolResultError("query parameter is required"), nil
	}

	siteKey := request.GetString("site_key", "")
	maxResults := request.GetInt("max_results", 10)
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 100 {
		maxResults = 100
	}

	// Determine which sites to search
	sitesToSearch := make(map[string]*config.SiteConfig)
	if siteKey != "" {
		if siteCfg, exists := s.cfg.AppConfig.Sites[siteKey]; exists {
			sitesToSearch[siteKey] = siteCfg
		} else {
			return mcp.NewToolResultError(fmt.Sprintf("site '%s' not found", siteKey)), nil
		}
	} else {
		sitesToSearch = s.cfg.AppConfig.Sites
	}

	// Search JSONL files
	results := s.searchJSONL(query, sitesToSearch, maxResults)

	response := map[string]interface{}{
		"query":         query,
		"results":       results,
		"total_matches": len(results),
	}

	if siteKey != "" {
		response["site_key"] = siteKey
	}

	return mcp.NewToolResultText(formatJSON(response)), nil
}

// runCrawlJob runs a crawl job in the background
func (s *Server) runCrawlJob(job *Job, siteCfg *config.SiteConfig, siteKey string) {
	s.jobManager.UpdateStatus(job.ID, JobStatusRunning, "")

	jobCtx := s.jobManager.GetContext(job.ID)

	// Create crawler components
	httpClient := fetch.NewClient(s.cfg.AppConfig.HTTPClientSettings, s.log)
	fetcher := fetch.NewFetcher(httpClient, s.cfg.AppConfig, s.log)
	rateLimiter := fetch.NewRateLimiter(time.Second, s.log)

	// Open store (always fresh for MCP jobs, never resume)
	store, err := storage.NewBadgerStore(jobCtx, s.cfg.AppConfig.StateDir, siteCfg.AllowedDomain, false, s.log)
	if err != nil {
		s.jobManager.UpdateStatus(job.ID, JobStatusFailed, fmt.Sprintf("failed to open store: %v", err))
		return
	}
	defer store.Close()

	// Set incremental mode
	appCfgCopy := *s.cfg.AppConfig
	if job.Incremental {
		appCfgCopy.EnableIncremental = true
	}

	// Create crawler
	crawlerCtx, cancelCrawl := context.WithCancel(jobCtx)
	defer cancelCrawl()

	crawlerInstance, err := crawler.NewCrawler(
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
	)
	if err != nil {
		s.jobManager.UpdateStatus(job.ID, JobStatusFailed, fmt.Sprintf("failed to create crawler: %v", err))
		return
	}

	// Run crawler
	if err := crawlerInstance.Run(false); err != nil {
		if err == context.Canceled {
			s.jobManager.UpdateStatus(job.ID, JobStatusCancelled, "")
		} else {
			s.jobManager.UpdateStatus(job.ID, JobStatusFailed, err.Error())
		}
		return
	}

	s.jobManager.UpdateStatus(job.ID, JobStatusCompleted, "")
}

// searchJSONL searches JSONL files for matching content
func (s *Server) searchJSONL(query string, sites map[string]*config.SiteConfig, maxResults int) []map[string]interface{} {
	results := make([]map[string]interface{}, 0)
	queryLower := strings.ToLower(query)

	for siteKey, siteCfg := range sites {
		siteOutputDir := filepath.Join(s.cfg.AppConfig.OutputBaseDir, siteCfg.AllowedDomain)
		jsonlPath := filepath.Join(siteOutputDir, config.GetEffectiveJSONLOutputFilename(siteCfg, s.cfg.AppConfig))

		// Stream JSONL file line-by-line to avoid loading it all into memory
		file, err := os.Open(jsonlPath)
		if err != nil {
			continue // Skip if file doesn't exist
		}

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // up to 10MB per line

		for scanner.Scan() {
			if len(results) >= maxResults {
				break
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var page models.PageJSONL
			if err := parseJSONLine(line, &page); err != nil {
				continue
			}

			// Search in content, title, and headings
			contentLower := strings.ToLower(page.Content)
			titleLower := strings.ToLower(page.Title)

			matched := false
			matchLocation := ""

			if strings.Contains(titleLower, queryLower) {
				matched = true
				matchLocation = "title"
			} else if strings.Contains(contentLower, queryLower) {
				matched = true
				matchLocation = "content"
			} else {
				for _, heading := range page.Headings {
					if strings.Contains(strings.ToLower(heading), queryLower) {
						matched = true
						matchLocation = "headings"
						break
					}
				}
			}

			if matched {
				snippet := extractSnippet(page.Content, query, 150)
				results = append(results, map[string]interface{}{
					"url":            page.URL,
					"title":          page.Title,
					"snippet":        snippet,
					"site_key":       siteKey,
					"match_location": matchLocation,
				})
			}
		}
		file.Close()

		if len(results) >= maxResults {
			break
		}
	}

	return results
}

// getLastCrawledTime gets the last crawl time from metadata file
func (s *Server) getLastCrawledTime(siteKey string, siteCfg *config.SiteConfig) time.Time {
	siteOutputDir := filepath.Join(s.cfg.AppConfig.OutputBaseDir, siteCfg.AllowedDomain)
	metadataPath := filepath.Join(siteOutputDir, config.GetEffectiveMetadataYAMLFilename(siteCfg, s.cfg.AppConfig))

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return time.Time{}
	}

	var metadata models.CrawlMetadata
	if err := yaml.Unmarshal(data, &metadata); err != nil {
		return time.Time{}
	}

	return metadata.CrawlEndTime
}

// extractSnippet extracts a snippet around the query match, slicing on rune
// boundaries so multi-byte UTF-8 characters are never split.
func extractSnippet(content, query string, maxLen int) string {
	runes := []rune(content)
	queryRunes := []rune(strings.ToLower(query))
	contentLowerRunes := []rune(strings.ToLower(content))

	// Find match position in runes
	idx := -1
	for i := 0; i <= len(contentLowerRunes)-len(queryRunes); i++ {
		if string(contentLowerRunes[i:i+len(queryRunes)]) == string(queryRunes) {
			idx = i
			break
		}
	}

	if idx == -1 {
		if len(runes) > maxLen {
			return string(runes[:maxLen]) + "..."
		}
		return content
	}

	// Calculate start and end positions in rune space
	start := idx - maxLen/2
	if start < 0 {
		start = 0
	}

	end := idx + len(queryRunes) + maxLen/2
	if end > len(runes) {
		end = len(runes)
	}

	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(runes) {
		snippet = snippet + "..."
	}

	return snippet
}

// parseJSONLine parses a single JSON line into a PageJSONL struct
func parseJSONLine(line string, page *models.PageJSONL) error {
	return json.Unmarshal([]byte(line), page)
}

// formatJSON formats data as an indented JSON string
func formatJSON(data map[string]interface{}) string {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\": %q}", err.Error())
	}
	return string(b)
}
