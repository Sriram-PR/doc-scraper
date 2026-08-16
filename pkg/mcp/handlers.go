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
	"github.com/JohannesKaufmann/html-to-markdown/plugin"
	"github.com/PuerkitoBio/goquery"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func (s *Server) handleListSites(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sites := make([]map[string]interface{}, 0, len(s.cfg.AppConfig.Sites))

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

		lastCrawled := s.getLastCrawledTime(key, siteCfg)
		if !lastCrawled.IsZero() {
			siteInfo["last_crawled"] = lastCrawled.Format(time.RFC3339)
		}

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

// handleCancelCrawl returns an error for an unknown job_id (matching
// get_job_status) and cancelled=false with a status field for jobs already
// in a terminal state.
func (s *Server) handleCancelCrawl(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	jobID := request.GetString("job_id", "")
	if jobID == "" {
		return mcp.NewToolResultError("job_id parameter is required"), nil
	}

	job := s.jobManager.GetJob(jobID)
	if job == nil {
		return mcp.NewToolResultError(fmt.Sprintf("job '%s' not found", jobID)), nil
	}

	cancelled := s.jobManager.CancelJob(jobID)
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

// handleDescribeServer returns server identity, sites, and recent jobs in one
// payload. Tool schemas are advertised by the MCP protocol and are not duplicated.
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

	// Newest first, capped so the payload stays small for long-running servers.
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
		"sites":       sites,
		"recent_jobs": jobs,
		"total_sites": len(sites),
		"total_jobs":  len(jobs),
		"jobs_capped": len(s.jobManager.ListJobs()) > maxJobs,
		"next_actions": "Use list_sites for full site config, list_pages to enumerate crawled " +
			"pages, crawl_site to start a crawl, get_job_status to check a job, get_page to " +
			"fetch a single URL, get_freshness to check how stale a site's crawl is, diff_crawl " +
			"to see what changed since a given timestamp.",
	}
	return mcp.NewToolResultText(formatJSON(result)), nil
}

// pageListEntry is metadata only; full content goes through get_page so
// list_pages stays cheap on sites with thousands of pages.
type pageListEntry struct {
	URL           string `json:"url"`
	Title         string `json:"title"`
	Depth         int    `json:"depth"`
	CrawledAt     string `json:"crawled_at"`
	ContentLength int    `json:"content_length"`
}

// handleListPages returns paginated page metadata, URL-sorted, from the site's
// JSONL output. Returns an empty result with a hint when no crawl exists yet.
func (s *Server) handleListPages(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	siteKey := request.GetString("site_key", "")
	if siteKey == "" {
		return mcp.NewToolResultError("site_key parameter is required"), nil
	}
	siteCfg, errResult := s.resolveSiteOrError(siteKey)
	if errResult != nil {
		return errResult, nil
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
		s.cfg.AppConfig.SiteOutputDir(siteKey),
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

func (s *Server) handleGetPage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	urlStr := request.GetString("url", "")
	if urlStr == "" {
		return mcp.NewToolResultError("url parameter is required"), nil
	}

	contentSelector := request.GetString("content_selector", "body")

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid URL: %v", err)), nil
	}

	startTime := time.Now()

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

	const maxPageSize = 50 * 1024 * 1024
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxPageSize))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read response: %v", err)), nil
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(bodyBytes))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse HTML: %v", err)), nil
	}

	title := strings.TrimSpace(doc.Find("title").First().Text())
	if title == "" {
		title = "Untitled"
	}

	contentSelection := doc.Find(contentSelector)
	if contentSelection.Length() == 0 {
		return mcp.NewToolResultError(fmt.Sprintf("content selector '%s' not found on page", contentSelector)), nil
	}

	converter := md.NewConverter("", true, nil)
	converter.Use(plugin.GitHubFlavored())
	content := strings.TrimSpace(converter.Convert(contentSelection.First()))

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

func (s *Server) handleCrawlSite(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	siteKey := request.GetString("site_key", "")
	if siteKey == "" {
		return mcp.NewToolResultError("site_key parameter is required"), nil
	}

	incremental := request.GetBool("incremental", false)

	siteCfg, errResult := s.resolveSiteOrError(siteKey)
	if errResult != nil {
		return errResult, nil
	}

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

	job, err := s.jobManager.CreateJob(siteKey, incremental)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create job: %v", err)), nil
	}

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

func (s *Server) runCrawlJob(job *Job, siteCfg *config.SiteConfig, siteKey string) {
	s.jobManager.UpdateStatus(job.ID, JobStatusRunning, "")

	jobCtx := s.jobManager.GetContext(job.ID)

	httpClient := fetch.NewClient(s.cfg.AppConfig.HTTPClientSettings, s.log)
	fetcher := fetch.NewFetcher(httpClient, s.cfg.AppConfig, s.log)
	rateLimiter := fetch.NewRateLimiter(s.cfg.AppConfig.DefaultDelayPerHost, s.log)

	// MCP jobs always start fresh, never resume.
	store, err := storage.NewBadgerStore(jobCtx, s.cfg.AppConfig.StateDir, siteKey, false, s.log)
	if err != nil {
		s.jobManager.UpdateStatus(job.ID, JobStatusFailed, fmt.Sprintf("failed to open store: %v", err))
		return
	}
	defer func() { _ = store.Close() }()

	appCfgCopy := *s.cfg.AppConfig
	if job.Incremental {
		appCfgCopy.EnableIncremental = true
	}

	crawlerCtx, cancelCrawl := context.WithCancel(jobCtx)
	defer cancelCrawl()

	// Run GC under crawlerCtx, not jobCtx: jobCtx is only cancelled on
	// explicit CancelJob, so a normally-completed job would leak this
	// goroutine. crawlerCtx is always cancelled by the deferred cancelCrawl.
	go store.RunGC(crawlerCtx, 0)

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
		false,
		&crawler.CrawlerOptions{
			ProgressCallback: func(processed, queued int64) {
				s.jobManager.UpdateProgress(jobID, processed, queued)
			},
			Index: s.idx,
		},
	)
	if err != nil {
		s.jobManager.UpdateStatus(job.ID, JobStatusFailed, fmt.Sprintf("failed to create crawler: %v", err))
		return
	}

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

// getLastCrawledTime returns the end time from the last crawl_meta record in
// the site's JSONL, or the zero time if none exists.
func (s *Server) getLastCrawledTime(siteKey string, siteCfg *config.SiteConfig) time.Time {
	siteOutputDir := s.cfg.AppConfig.SiteOutputDir(siteKey)
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

// resolveSiteOrError validates site_key against the configured sites and
// returns either the site config or a sorted-available-sites error result.
func (s *Server) resolveSiteOrError(siteKey string) (*config.SiteConfig, *mcp.CallToolResult) {
	siteCfg, exists := s.cfg.AppConfig.Sites[siteKey]
	if exists {
		return siteCfg, nil
	}
	availableKeys := make([]string, 0, len(s.cfg.AppConfig.Sites))
	for k := range s.cfg.AppConfig.Sites {
		availableKeys = append(availableKeys, k)
	}
	sort.Strings(availableKeys)
	return nil, mcp.NewToolResultError(fmt.Sprintf("site '%s' not found. Available sites: %v", siteKey, availableKeys))
}

// handleGetFreshness answers "is the local crawl recent enough to query, or
// should I run crawl_site first?" Pulls the latest crawl from the history
// index, derives age, reports running-job presence.
func (s *Server) handleGetFreshness(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	siteKey := request.GetString("site_key", "")
	if siteKey == "" {
		return mcp.NewToolResultError("site_key parameter is required"), nil
	}
	if _, errResult := s.resolveSiteOrError(siteKey); errResult != nil {
		return errResult, nil
	}

	siteOutputDir := s.cfg.AppConfig.SiteOutputDir(siteKey)
	stateDBPath := storage.VisitedDBPath(s.cfg.AppConfig.StateDir, siteKey)
	outputExists := dirExists(siteOutputDir)
	stateExists := dirExists(stateDBPath)

	result := map[string]interface{}{
		"site_key":         siteKey,
		"output_dir":       siteOutputDir,
		"output_exists":    outputExists,
		"state_dir_exists": stateExists,
	}

	if s.idx != nil {
		latest, err := s.idx.GetLatestCrawl(ctx, siteKey)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to query crawl history: %v", err)), nil
		}
		if latest != nil {
			now := time.Now()
			result["last_crawl_id"] = latest.ID
			result["last_crawl_started_at"] = latest.CrawlStartedAt.Format(time.RFC3339)
			result["last_crawl_ended_at"] = latest.CrawlEndedAt.Format(time.RFC3339)
			result["last_crawl_total_pages"] = latest.TotalPages
			result["last_crawl_mode"] = string(latest.Mode)
			result["age_seconds"] = int64(now.Sub(latest.CrawlEndedAt).Seconds())
		}
	}

	if s.jobManager.IsRunning(siteKey) {
		if job := s.jobManager.GetJobBySite(siteKey); job != nil {
			result["running_job"] = map[string]interface{}{
				"job_id":          job.ID,
				"started_at":      job.StartedAt.Format(time.RFC3339),
				"pages_processed": job.PagesProcessed,
				"pages_queued":    job.PagesQueued,
				"incremental":     job.Incremental,
			}
		}
	}

	if _, ok := result["last_crawl_ended_at"]; !ok {
		result["next_actions"] = "No prior crawl recorded. Run crawl_site to populate the history index."
	} else {
		result["next_actions"] = "list_pages or get_page to read; crawl_site to refresh; diff_crawl with since=last_crawl_ended_at to compute what changed."
	}

	return mcp.NewToolResultText(formatJSON(result)), nil
}

// handleDiffCrawl returns added/removed/changed pages between the latest crawl
// and the most recent crawl whose crawl_ended_at <= since. Hash-based verdicts
// from the SQLite history index.
func (s *Server) handleDiffCrawl(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	siteKey := request.GetString("site_key", "")
	if siteKey == "" {
		return mcp.NewToolResultError("site_key parameter is required"), nil
	}
	if _, errResult := s.resolveSiteOrError(siteKey); errResult != nil {
		return errResult, nil
	}
	if s.idx == nil {
		return mcp.NewToolResultError("crawl-history index is disabled (state_dir is unset)"), nil
	}

	sinceStr := request.GetString("since", "")
	if sinceStr == "" {
		return mcp.NewToolResultError("since parameter is required (RFC3339 timestamp, e.g. 2026-05-23T22:00:00Z)"), nil
	}
	since, err := time.Parse(time.RFC3339, sinceStr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid since timestamp %q: expected RFC3339 (e.g. 2026-05-23T22:00:00Z): %v", sinceStr, err)), nil
	}

	maxResults := request.GetInt("max_results", 100)
	offset := request.GetInt("offset", 0)

	res, err := s.idx.DiffSince(ctx, siteKey, since, maxResults, offset)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("diff_crawl failed: %v", err)), nil
	}

	added := make([]map[string]interface{}, 0)
	removed := make([]map[string]interface{}, 0)
	changed := make([]map[string]interface{}, 0)
	for _, e := range res.Entries {
		entry := map[string]interface{}{
			"url":          e.URL,
			"title":        e.Title,
			"content_hash": e.ContentHash,
			"depth":        e.Depth,
		}
		switch e.Kind {
		case "added":
			added = append(added, entry)
		case "removed":
			removed = append(removed, entry)
		case "changed":
			entry["prior_hash"] = e.PriorHash
			changed = append(changed, entry)
		}
	}

	out := map[string]interface{}{
		"site_key":        siteKey,
		"since":           since.Format(time.RFC3339),
		"added":           added,
		"removed":         removed,
		"changed":         changed,
		"unchanged_count": res.UnchangedCount,
		"total":           res.Total,
		"returned":        len(res.Entries),
		"offset":          offset,
		"max_results":     maxResults,
	}
	if res.CurrentCrawl != nil {
		out["current_crawl"] = summarizeCrawl(res.CurrentCrawl)
	}
	if res.BaselineCrawl != nil {
		out["baseline_crawl"] = summarizeCrawl(res.BaselineCrawl)
	}
	switch {
	case res.CurrentCrawl == nil:
		out["note"] = "No crawl has been recorded for this site yet. Run crawl_site to seed the history."
	case res.BaselineCrawl == nil:
		out["note"] = "No baseline crawl ended at or before since; cannot diff. Re-issue with an earlier since, or treat the current crawl as the baseline going forward."
	case res.BaselineCrawl.ID == res.CurrentCrawl.ID:
		out["note"] = "Baseline and current crawl are the same (since is newer than the latest crawl_ended_at)."
	}
	return mcp.NewToolResultText(formatJSON(out)), nil
}

// dirExists returns true if path is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// summarizeCrawl renders a LatestCrawl for the diff_crawl response payload.
func summarizeCrawl(c *index.LatestCrawl) map[string]interface{} {
	return map[string]interface{}{
		"crawl_id":         c.ID,
		"crawl_started_at": c.CrawlStartedAt.Format(time.RFC3339),
		"crawl_ended_at":   c.CrawlEndedAt.Format(time.RFC3339),
		"total_pages":      c.TotalPages,
		"mode":             string(c.Mode),
	}
}

// formatJSON formats data as an indented JSON string
func formatJSON(data map[string]interface{}) string {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\": %q}", err.Error())
	}
	return string(b)
}
