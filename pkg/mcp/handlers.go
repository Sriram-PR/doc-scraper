package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/process"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func (s *Server) handleListSites(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	keys := config.GetAllSiteKeys(s.cfg.AppConfig)
	sites := make([]siteSummary, 0, len(keys))

	for _, key := range keys {
		siteCfg := s.cfg.AppConfig.Sites[key]
		lastCrawled, status := s.siteActivity(key, siteCfg)
		sites = append(sites, siteSummary{
			Key:            key,
			Domain:         siteCfg.AllowedDomain,
			PathPrefix:     siteCfg.AllowedPathPrefix,
			StartURLsCount: len(siteCfg.StartURLs),
			MaxDepth:       siteCfg.MaxDepth,
			LastCrawled:    lastCrawled,
			Status:         status,
		})
	}

	return structuredResult(listSitesResult{
		Sites:      sites,
		ConfigPath: s.cfg.ConfigPath,
		TotalSites: len(sites),
	}), nil
}

// handleCancelCrawl returns an error for an unknown job_id (matching
// get_job_status) and cancelled=false with a status field for jobs already
// in a terminal state.
func (s *Server) handleCancelCrawl(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{"job_id": argString}); errResult != nil {
		return errResult, nil
	}
	jobID := request.GetString("job_id", "")
	if jobID == "" {
		return mcp.NewToolResultError("job_id parameter is required"), nil
	}

	job := s.jobManager.GetJob(jobID)
	if job == nil {
		return mcp.NewToolResultError(fmt.Sprintf("job '%s' not found", clipInput(jobID))), nil
	}

	cancelled := s.jobManager.CancelJob(jobID)
	job = s.jobManager.GetJob(jobID)

	res := cancelCrawlResult{
		JobID:     jobID,
		Cancelled: cancelled,
		SiteKey:   job.SiteKey,
		Status:    job.Status,
		Message:   "job cancelled",
	}
	if !cancelled {
		res.Message = fmt.Sprintf("job is in terminal state %q and cannot be cancelled", job.Status)
	}
	return structuredResult(res), nil
}

// handleDescribeServer returns server identity, sites, and recent jobs in one
// payload. Tool schemas are advertised by the MCP protocol and are not duplicated.
func (s *Server) handleDescribeServer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	siteKeys := config.GetAllSiteKeys(s.cfg.AppConfig)

	sites := make([]serverSite, 0, len(siteKeys))
	for _, key := range siteKeys {
		siteCfg := s.cfg.AppConfig.Sites[key]
		lastCrawled, status := s.siteActivity(key, siteCfg)
		sites = append(sites, serverSite{Key: key, Domain: siteCfg.AllowedDomain, LastCrawled: lastCrawled, Status: status})
	}

	// Newest first, capped so the payload stays small for long-running servers.
	allJobs := s.jobManager.ListJobs()
	sortJobsNewestFirst(allJobs)
	const maxJobs = 20
	capped := len(allJobs) > maxJobs
	if capped {
		allJobs = allJobs[:maxJobs]
	}
	jobs := make([]jobSummary, 0, len(allJobs))
	for _, j := range allJobs {
		jobs = append(jobs, summarizeJob(j))
	}

	return structuredResult(describeServerResult{
		Server:     serverIdentity{Name: serverName, Version: serverVersion, ConfigPath: s.cfg.ConfigPath},
		Sites:      sites,
		RecentJobs: jobs,
		TotalSites: len(sites),
		TotalJobs:  len(jobs),
		JobsCapped: capped,
		NextActions: "Use search_docs to find where something is documented, list_sites for " +
			"full site config, list_pages to enumerate crawled pages, crawl_site to start a " +
			"crawl, get_job_status to check a job, get_page to fetch a single URL, " +
			"get_freshness to check how stale a site's crawl is, diff_crawl to see what " +
			"the latest crawl changed, read_page to read a crawled page's stored " +
			"markdown, cancel_crawl to stop a running job.",
	}), nil
}

// handleListPages returns paginated page metadata, URL-sorted, from the site's
// JSONL output. Returns an empty result with a hint when no crawl exists yet.
// siteJSONLPath locates the crawl JSONL that backs the stored corpus. The
// second return is false when JSONL output is disabled for the site, which is
// worth reporting separately from a missing file: the crawl may have run fine
// and simply written Markdown only.
func (s *Server) siteJSONLPath(siteKey string, siteCfg *config.SiteConfig) (string, bool) {
	if !config.GetEffectiveEnableJSONLOutput(siteCfg, s.cfg.AppConfig) {
		return "", false
	}
	return filepath.Join(
		s.cfg.AppConfig.SiteOutputDir(siteKey),
		config.GetEffectiveJSONLOutputFilename(siteCfg, s.cfg.AppConfig),
	), true
}

const jsonlDisabledHint = "JSONL output is disabled for site '%s', so no stored corpus is available to read. " +
	"Remove enable_jsonl_output: false (it defaults to true) and re-run crawl_site."

const jsonlNotIndexedHint = "enable_jsonl_output is false for site '%s', so its crawls are not indexed for " +
	"search or crawl history. Remove the setting (it defaults to true) and re-run crawl_site."

func (s *Server) jsonlDisabled(siteKey string) bool {
	siteCfg, ok := s.cfg.AppConfig.Sites[siteKey]
	return ok && !config.GetEffectiveEnableJSONLOutput(siteCfg, s.cfg.AppConfig)
}

// unindexedOptOuts lists opted-out sites with nothing in the search index;
// chunks indexed before a site opted out are searched until its next crawl.
func (s *Server) unindexedOptOuts(ctx context.Context) []string {
	var off []string
	for _, key := range config.GetAllSiteKeys(s.cfg.AppConfig) {
		if !s.jsonlDisabled(key) {
			continue
		}
		if has, err := s.idx.SiteHasChunks(ctx, key); err == nil && !has {
			off = append(off, key)
		}
	}
	return off
}

func (s *Server) handleListPages(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{"site_key": argString, "max_results": argInteger, "offset": argInteger}); errResult != nil {
		return errResult, nil
	}
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

	jsonlPath, jsonlEnabled := s.siteJSONLPath(siteKey, siteCfg)
	if !jsonlEnabled {
		return mcp.NewToolResultError(fmt.Sprintf(jsonlDisabledHint, siteKey)), nil
	}

	file, err := os.Open(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return structuredResult(listPagesResult{
				SiteKey:    siteKey,
				Pages:      []pageListEntry{},
				Offset:     offset,
				MaxResults: maxResults,
				Message:    "No crawl output found for this site. Run crawl_site first.",
			}), nil
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

	return structuredResult(listPagesResult{
		SiteKey:    siteKey,
		Pages:      pageSlice,
		Total:      total,
		Offset:     offset,
		MaxResults: maxResults,
		Returned:   len(pageSlice),
	}), nil
}

func (s *Server) handleGetPage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{"url": argString, "content_selector": argString, "max_bytes": argInteger, "offset": argInteger}); errResult != nil {
		return errResult, nil
	}
	maxBytes, offset := pageWindow(request)
	urlStr := request.GetString("url", "")
	if urlStr == "" {
		return mcp.NewToolResultError("url parameter is required"), nil
	}

	contentSelector := request.GetString("content_selector", "body")
	selector, err := cascadia.Compile(contentSelector)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid CSS selector %q: %s", clipInput(contentSelector), clipInput(err.Error()))), nil
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid URL: %s", clipInput(err.Error()))), nil
	}

	startTime := time.Now()

	client := fetch.NewClient(s.cfg.AppConfig.HTTPClientSettings, s.log)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create request: %s", clipInput(err.Error()))), nil
	}

	userAgent := s.cfg.AppConfig.DefaultUserAgent
	if userAgent == "" {
		userAgent = fetch.DefaultUserAgent()
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to fetch URL: %s", clipInput(err.Error()))), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(fmt.Sprintf("HTTP error: %d %s", resp.StatusCode, resp.Status)), nil
	}

	body := bufio.NewReader(resp.Body)
	head, _ := body.Peek(512)
	if ct := htmlContentType(resp.Header.Get("Content-Type"), head); ct != "" {
		return mcp.NewToolResultError(fmt.Sprintf("%s is not an HTML page (Content-Type %s); get_page only converts HTML", clipInput(urlStr), clipInput(ct))), nil
	}

	const maxPageSize = 50 * 1024 * 1024
	bodyBytes, err := io.ReadAll(io.LimitReader(body, maxPageSize))
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

	contentSelection := doc.FindMatcher(selector)
	if contentSelection.Length() == 0 {
		return mcp.NewToolResultError(fmt.Sprintf("content selector '%s' not found on page", clipInput(contentSelector))), nil
	}

	content, err := process.ToMarkdown(contentSelection.First())
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to convert page: %v", err)), nil
	}
	content = strings.TrimSpace(content)

	fetchTimeMs := time.Since(startTime).Milliseconds()

	part, start := sliceAtRuneBoundary(content, offset, maxBytes)
	end := start + len(part)
	res := getPageResult{
		URL:           parsedURL.String(),
		Title:         title,
		Content:       part,
		ContentLength: len(part),
		TotalLength:   len(content),
		Offset:        start,
		Truncated:     end < len(content),
		FetchTimeMs:   fetchTimeMs,
	}
	if res.Truncated {
		res.NextOffset = &end
	}
	return structuredResult(res), nil
}

// pageWindow reads the max_bytes and offset arguments shared by read_page and
// get_page.
func pageWindow(request mcp.CallToolRequest) (maxBytes, offset int) {
	maxBytes = request.GetInt("max_bytes", defaultReadPageBytes)
	if maxBytes <= 0 {
		maxBytes = defaultReadPageBytes
	}
	if maxBytes > maxReadPageBytes {
		maxBytes = maxReadPageBytes
	}
	return maxBytes, request.GetInt("offset", 0)
}

// htmlContentType returns "" for an HTML response, or else the content type
// to report, sniffing the body when the server sent none.
func htmlContentType(header string, body []byte) string {
	ct := header
	if ct == "" {
		ct = http.DetectContentType(body)
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err == nil && (mediaType == "text/html" || mediaType == "application/xhtml+xml") {
		return ""
	}
	return ct
}

func (s *Server) handleCrawlSite(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{"site_key": argString, "incremental": argBool}); errResult != nil {
		return errResult, nil
	}
	siteKey := request.GetString("site_key", "")
	if siteKey == "" {
		return mcp.NewToolResultError("site_key parameter is required"), nil
	}

	incremental := request.GetBool("incremental", false)

	siteCfg, errResult := s.resolveSiteOrError(siteKey)
	if errResult != nil {
		return errResult, nil
	}

	job, created, err := s.jobManager.StartJob(ctx, siteKey, incremental, crawlRestartWait)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create job: %v", err)), nil
	}
	if !created {
		message := "A crawl is already in progress for this site"
		if isTerminalStatus(job.Status) {
			message = "The previous crawl for this site is still shutting down; try again shortly"
		}
		return structuredResult(crawlSiteResult{
			Status:      "already_running",
			Message:     message,
			JobID:       job.ID,
			SiteKey:     siteKey,
			Incremental: job.Incremental,
		}), nil
	}

	go s.runCrawlJob(job, siteCfg, siteKey)

	return structuredResult(crawlSiteResult{
		Status:      "started",
		Message:     "Crawl started. Call get_job_status with wait_seconds to wait for it to finish.",
		JobID:       job.ID,
		SiteKey:     siteKey,
		Incremental: incremental,
	}), nil
}

func (s *Server) handleGetJobStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{"job_id": argString, "wait_seconds": argInteger}); errResult != nil {
		return errResult, nil
	}
	jobID := request.GetString("job_id", "")
	if jobID == "" {
		return mcp.NewToolResultError("job_id parameter is required"), nil
	}

	job := s.jobManager.GetJob(jobID)
	if job == nil {
		return mcp.NewToolResultError(fmt.Sprintf("job '%s' not found", clipInput(jobID))), nil
	}

	wait := time.Duration(min(max(request.GetInt("wait_seconds", 0), 0), maxJobWaitSeconds)) * time.Second
	if wait > 0 && !isTerminalStatus(job.Status) {
		deadline := time.NewTimer(wait)
		defer deadline.Stop()
		tick := time.NewTicker(jobWaitPoll)
		defer tick.Stop()
	wait:
		for !isTerminalStatus(job.Status) {
			select {
			case <-ctx.Done():
				break wait
			case <-deadline.C:
				break wait
			case <-tick.C:
				if j := s.jobManager.GetJob(jobID); j != nil {
					job = j
				}
			}
		}
	}

	return structuredResult(summarizeJob(job)), nil
}

const (
	// Short enough for MCP clients' tool-call timeouts.
	maxJobWaitSeconds = 60
	jobWaitPoll       = 250 * time.Millisecond
)

// crawlRestartWait bounds how long crawl_site waits for a cancelled crawl of
// the same site to close its store.
const crawlRestartWait = 30 * time.Second

func (s *Server) runCrawlJob(job *Job, siteCfg *config.SiteConfig, siteKey string) {
	// Deferred first so it runs last, after the store is closed.
	defer s.jobManager.FinishJob(job.ID)
	s.jobManager.UpdateStatus(job.ID, JobStatusRunning, "")

	jobCtx := s.jobManager.GetContext(job.ID)

	fetcher, rateLimiter := fetch.NewStack(s.cfg.AppConfig, s.log)

	// A full crawl starts fresh. An incremental crawl resumes against the
	// previous crawl's visited DB (like `crawl -incremental` on the CLI, where
	// incremental implies resume); change detection compares each page to the
	// hash stored there, so a fresh DB would reprocess every page.
	resume := job.Incremental
	stage, store, err := crawler.OpenStagedStore(jobCtx, s.cfg.AppConfig, siteKey, resume, s.log)
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
		resume,
		&crawler.CrawlerOptions{
			ProgressCallback: func(processed, queued int64) {
				s.jobManager.UpdateProgress(jobID, processed, queued)
			},
			Index: s.idx,
			Stage: stage,
		},
	)
	if err != nil {
		s.jobManager.UpdateStatus(job.ID, JobStatusFailed, fmt.Sprintf("failed to create crawler: %v", err))
		return
	}

	s.jobManager.AttachProgress(jobID, func() JobProgress {
		p := crawlerInstance.GetProgress()
		return JobProgress{Processed: p.PagesProcessed, Queued: int64(p.PagesQueued), Unchanged: p.PagesUnchanged}
	})
	err = crawlerInstance.Run(resume)
	s.jobManager.DetachProgress(jobID)
	if err != nil {
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
// siteActivity returns a site's last_crawled and status values, each empty
// when absent.
func (s *Server) siteActivity(key string, siteCfg *config.SiteConfig) (lastCrawled, status string) {
	if t := s.getLastCrawledTime(key, siteCfg); !t.IsZero() {
		lastCrawled = t.Format(time.RFC3339)
	}
	if s.jobManager.IsRunning(key) {
		status = "running"
	}
	return lastCrawled, status
}

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
	return nil, mcp.NewToolResultError(fmt.Sprintf("site '%s' not found. Available sites: %v", clipInput(siteKey), config.GetAllSiteKeys(s.cfg.AppConfig)))
}

// handleGetFreshness answers "is the local crawl recent enough to query, or
// should I run crawl_site first?" Pulls the latest crawl from the history
// index, derives age, reports running-job presence.
func (s *Server) handleGetFreshness(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{"site_key": argString}); errResult != nil {
		return errResult, nil
	}
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

	res := freshnessResult{
		SiteKey:        siteKey,
		OutputDir:      siteOutputDir,
		OutputExists:   outputExists,
		StateDirExists: stateExists,
	}

	if s.idx != nil {
		latest, err := s.idx.GetLatestCrawl(ctx, siteKey)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to query crawl history: %v", err)), nil
		}
		if latest != nil {
			res.LastCrawl = summarizeCrawl(latest)
			age := int64(time.Since(latest.CrawlEndedAt).Seconds())
			res.AgeSeconds = &age
		}
	}

	if s.jobManager.IsRunning(siteKey) {
		if job := s.jobManager.GetJobBySite(siteKey); job != nil {
			running := summarizeJob(job)
			res.RunningJob = &running
		}
	}

	switch {
	case s.jsonlDisabled(siteKey):
		res.NextActions = fmt.Sprintf(jsonlNotIndexedHint, siteKey)
	case res.LastCrawl == nil:
		res.NextActions = "No prior crawl recorded. Run crawl_site to populate the history index."
	default:
		res.NextActions = "list_pages to enumerate what was crawled, then read_page to read any " +
			"of those pages from the stored crawl; crawl_site to refresh, then diff_crawl " +
			"without since to see what that crawl changed. get_page re-fetches a URL live and " +
			"does not read this crawl."
	}

	return structuredResult(res), nil
}

// handleDiffCrawl returns added/removed/changed pages between the latest crawl
// and the crawl before it, or with since, the most recent crawl whose
// crawl_ended_at <= since. Hash-based verdicts
// from the SQLite history index.
func (s *Server) handleDiffCrawl(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{"site_key": argString, "since": argString, "max_results": argInteger, "offset": argInteger}); errResult != nil {
		return errResult, nil
	}
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
	var since time.Time
	if sinceStr != "" {
		var err error
		since, err = time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid since timestamp %q: expected RFC3339 (e.g. 2026-05-23T22:00:00Z): %s", clipInput(sinceStr), clipInput(err.Error()))), nil
		}
	}

	maxResults := request.GetInt("max_results", 100)
	if maxResults <= 0 {
		maxResults = 100
	}
	if maxResults > 1000 {
		maxResults = 1000
	}
	offset := max(request.GetInt("offset", 0), 0)

	var res *index.DiffResult
	var err error
	if sinceStr == "" {
		res, err = s.idx.DiffPrevious(ctx, siteKey, maxResults, offset)
	} else {
		res, err = s.idx.DiffSince(ctx, siteKey, since, maxResults, offset)
	}
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("diff_crawl failed: %v", err)), nil
	}

	out := diffCrawlResult{
		SiteKey:        siteKey,
		Added:          []diffEntry{},
		Removed:        []diffEntry{},
		Changed:        []diffEntry{},
		UnchangedCount: res.UnchangedCount,
		Total:          res.Total,
		Returned:       len(res.Entries),
		Offset:         offset,
		MaxResults:     maxResults,
		CurrentCrawl:   summarizeCrawl(res.CurrentCrawl),
		BaselineCrawl:  summarizeCrawl(res.BaselineCrawl),
	}
	if sinceStr != "" {
		out.Since = since.Format(time.RFC3339)
	}
	for _, e := range res.Entries {
		entry := diffEntry{URL: e.URL, Title: e.Title, ContentHash: e.ContentHash, Depth: e.Depth}
		switch e.Kind {
		case "added":
			out.Added = append(out.Added, entry)
		case "removed":
			out.Removed = append(out.Removed, entry)
		case "changed":
			entry.PriorHash = e.PriorHash
			out.Changed = append(out.Changed, entry)
		}
	}
	switch {
	case res.CurrentCrawl == nil && s.jsonlDisabled(siteKey):
		out.Note = fmt.Sprintf(jsonlNotIndexedHint, siteKey)
	case res.CurrentCrawl == nil:
		out.Note = "No crawl has been recorded for this site yet. Run crawl_site to seed the history."
	case res.BaselineCrawl == nil && sinceStr == "":
		out.Note = "Only one crawl has been recorded for this site, so there is nothing to compare it with yet. Run crawl_site again to see what changes."
	case res.BaselineCrawl == nil:
		out.Note = "No baseline crawl ended at or before since; cannot diff. Re-issue with an earlier since, or omit since to compare with the previous crawl."
	case res.BaselineCrawl.ID == res.CurrentCrawl.ID:
		out.Note = "since is at or after the latest crawl's crawl_ended_at, so the latest crawl was compared with itself. To see what the latest crawl changed, omit since."
	}
	return structuredResult(out), nil
}

// dirExists returns true if path is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

const (
	defaultReadPageBytes = 100 * 1024
	maxReadPageBytes     = 1024 * 1024
)

// findPageRecord streams the crawl JSONL and returns the first page record for
// wantURL, stopping at the match instead of reading the whole file. Normalized
// forms are compared as a fallback so a caller's trailing slash or fragment
// still resolves. A nil record with a nil error means the page is not in the
// corpus.
func findPageRecord(jsonlPath, wantURL string) (*models.PageJSONL, error) {
	file, err := os.Open(jsonlPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	wantNorm, _, normErr := parse.ParseAndNormalize(wantURL)

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, `"record_type":"page"`) {
			continue
		}
		var p models.PageJSONL
		if err := json.Unmarshal([]byte(line), &p); err != nil || p.RecordType != models.RecordTypePage {
			continue
		}
		if p.URL == wantURL {
			return &p, nil
		}
		if normErr == nil {
			if gotNorm, _, err := parse.ParseAndNormalize(p.URL); err == nil && gotNorm == wantNorm {
				return &p, nil
			}
		}
	}
	return nil, scanner.Err()
}

const maxEchoedInputBytes = 200

// clipInput shortens a caller-supplied value before it is echoed in an error,
// keeping both ends so the reason at the tail of a wrapped error survives.
func clipInput(v string) string {
	if len(v) <= maxEchoedInputBytes {
		return v
	}
	head, tail := maxEchoedInputBytes/2, len(v)-maxEchoedInputBytes/2
	for head > 0 && !utf8.RuneStart(v[head]) {
		head--
	}
	for tail < len(v) && !utf8.RuneStart(v[tail]) {
		tail++
	}
	return fmt.Sprintf("%s...[%d bytes]...%s", v[:head], len(v), v[tail:])
}

// sliceAtRuneBoundary returns up to maxBytes of s starting at offset, snapping
// both cuts to rune boundaries so a page split across several reads never
// yields invalid UTF-8.
func sliceAtRuneBoundary(s string, offset, maxBytes int) (chunk string, start int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(s) {
		offset = len(s)
	}
	for offset > 0 && offset < len(s) && !utf8.RuneStart(s[offset]) {
		offset--
	}
	chunk = s[offset:]
	if len(chunk) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(chunk[cut]) {
			cut--
		}
		if cut == 0 {
			// maxBytes is smaller than the rune starting at offset. Return that
			// whole rune anyway: an empty chunk would leave offset unchanged and
			// loop a client that follows next_offset forever.
			_, cut = utf8.DecodeRuneInString(chunk)
		}
		chunk = chunk[:cut]
	}
	return chunk, offset
}

// handleReadPage serves a page's markdown out of the stored crawl. This is the
// counterpart to get_page, which always re-fetches over the network.
func (s *Server) handleReadPage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{"site_key": argString, "url": argString, "anchor": argString, "max_bytes": argInteger, "offset": argInteger}); errResult != nil {
		return errResult, nil
	}
	siteKey := request.GetString("site_key", "")
	if siteKey == "" {
		return mcp.NewToolResultError("site_key parameter is required"), nil
	}
	urlStr := request.GetString("url", "")
	if urlStr == "" {
		return mcp.NewToolResultError("url parameter is required"), nil
	}
	siteCfg, errResult := s.resolveSiteOrError(siteKey)
	if errResult != nil {
		return errResult, nil
	}

	maxBytes, offset := pageWindow(request)

	jsonlPath, jsonlEnabled := s.siteJSONLPath(siteKey, siteCfg)
	if !jsonlEnabled {
		return mcp.NewToolResultError(fmt.Sprintf(jsonlDisabledHint, siteKey)), nil
	}

	record, err := findPageRecord(jsonlPath, urlStr)
	if err != nil {
		if os.IsNotExist(err) {
			return mcp.NewToolResultError(fmt.Sprintf(
				"no crawl output found for site '%s'. Run crawl_site first.", siteKey)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("failed to read crawl output for site '%s': %v", siteKey, err)), nil
	}
	if record == nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"url '%s' is not in the stored crawl for site '%s'. Use list_pages to see what was crawled, "+
				"or get_page to fetch it live.", clipInput(urlStr), siteKey)), nil
	}

	content := record.Content
	var sectionEnd *int
	if anchor := strings.TrimPrefix(request.GetString("anchor", ""), "#"); anchor != "" {
		secStart, secEnd, ok := chunk.Section(content, anchor)
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("anchor %q is not a heading on %s; its anchors are: %s",
				clipInput(anchor), record.URL, strings.Join(chunk.Anchors(content), ", "))), nil
		}
		content = content[:secEnd]
		offset = max(offset, secStart)
		sectionEnd = &secEnd
	}

	part, start := sliceAtRuneBoundary(content, offset, maxBytes)
	end := start + len(part)
	headings := record.Headings
	if headings == nil {
		headings = []string{}
	}

	res := readPageResult{
		SiteKey:       siteKey,
		URL:           record.URL,
		Title:         record.Title,
		Content:       part,
		ContentLength: len(part),
		TotalLength:   len(record.Content),
		Offset:        start,
		SectionEnd:    sectionEnd,
		Truncated:     end < len(content),
		Depth:         record.Depth,
		CrawledAt:     record.CrawledAt,
		ContentHash:   record.ContentHash,
		Headings:      headings,
		Source:        "stored_crawl",
	}
	if res.Truncated {
		res.NextOffset = &end
	}
	return structuredResult(res), nil
}

const (
	maxSearchResults = 50
	// Generous for a natural-language question; bounds FTS5 parse and relaxation work.
	maxSearchQueryBytes = 1024
)

func (s *Server) handleSearchDocs(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if errResult := checkArgTypes(request, map[string]argKind{"query": argString, "site_key": argString, "limit": argInteger}); errResult != nil {
		return errResult, nil
	}
	query := strings.TrimSpace(request.GetString("query", ""))
	if query == "" {
		return mcp.NewToolResultError("query parameter is required"), nil
	}
	if len(query) > maxSearchQueryBytes {
		return mcp.NewToolResultError(fmt.Sprintf("query too long (%d bytes, max %d)", len(query), maxSearchQueryBytes)), nil
	}
	siteKey := request.GetString("site_key", "")
	if siteKey != "" {
		if _, errResult := s.resolveSiteOrError(siteKey); errResult != nil {
			return errResult, nil
		}
	}
	limit := request.GetInt("limit", 10)
	if limit <= 0 {
		limit = 10
	}
	if limit > maxSearchResults {
		limit = maxSearchResults
	}
	if s.idx == nil {
		return mcp.NewToolResultError("crawl-history index is disabled (state_dir is unset), so full-text search is unavailable"), nil
	}

	results, err := s.idx.SearchChunks(ctx, query, siteKey, limit)
	if errors.Is(err, index.ErrNoSearchableTerms) {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	res := searchDocsResult{
		Query:       query,
		SiteKey:     siteKey,
		Count:       len(results),
		Results:     results,
		NextActions: "Use read_page with a result's site_key and url to read the full page.",
	}
	if len(results) == 0 {
		hint := "No matches. Broaden the query, or check the corpus with list_sites and list_pages."
		if siteKey == "" {
			if off := s.unindexedOptOuts(ctx); len(off) > 0 {
				hint += " Not searched, enable_jsonl_output is false: " + strings.Join(off, ", ") +
					". Remove the setting (it defaults to true) and re-run crawl_site for them."
			}
		} else if has, hasErr := s.idx.SiteHasChunks(ctx, siteKey); hasErr == nil && !has {
			if s.jsonlDisabled(siteKey) {
				hint = fmt.Sprintf(jsonlNotIndexedHint, siteKey)
			} else {
				hint = "This site has no indexed content yet. Run crawl_site to build its corpus " +
					"(existing crawls are indexed automatically shortly after server start)."
			}
		}
		res.NextActions = hint
	}
	return structuredResult(res), nil
}
