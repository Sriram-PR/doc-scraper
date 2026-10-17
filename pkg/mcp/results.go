package mcp

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

// Each tool's result struct is both its structuredContent and, through
// outputSchemaFor, its declared outputSchema, so the two cannot drift.

// outputSchemaJSON reflects T into an open JSON schema. Reflection closes every
// object (additionalProperties:false); that is dropped so adding a field later
// does not break clients validating against a cached tools/list.
func outputSchemaJSON[T any]() json.RawMessage {
	var tool mcp.Tool
	mcp.WithOutputSchema[T]()(&tool)
	if tool.OutputSchema.Type == "" {
		panic(fmt.Sprintf("mcp: cannot generate output schema for %T", *new(T)))
	}
	b, err := json.Marshal(tool.OutputSchema)
	if err != nil {
		panic(fmt.Sprintf("mcp: marshal output schema for %T: %v", *new(T), err))
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		panic(fmt.Sprintf("mcp: decode output schema for %T: %v", *new(T), err))
	}
	stripClosedObjects(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("mcp: re-marshal output schema for %T: %v", *new(T), err))
	}
	return out
}

func stripClosedObjects(v any) {
	switch x := v.(type) {
	case map[string]any:
		if ap, ok := x["additionalProperties"].(bool); ok && !ap {
			delete(x, "additionalProperties")
		}
		for _, e := range x {
			stripClosedObjects(e)
		}
	case []any:
		for _, e := range x {
			stripClosedObjects(e)
		}
	}
}

func outputSchemaFor[T any]() mcp.ToolOption {
	return mcp.WithRawOutputSchema(outputSchemaJSON[T]())
}

func structuredResult(v any) *mcp.CallToolResult {
	return mcp.NewToolResultStructuredOnly(v)
}

type siteSummary struct {
	Key            string `json:"key"`
	Domain         string `json:"domain"`
	PathPrefix     string `json:"path_prefix"`
	StartURLsCount int    `json:"start_urls_count"`
	MaxDepth       int    `json:"max_depth"`
	LastCrawled    string `json:"last_crawled,omitempty"`
	Status         string `json:"status,omitempty" jsonschema_description:"\"running\" while a crawl is active; absent otherwise"`
}

type listSitesResult struct {
	Sites      []siteSummary `json:"sites"`
	ConfigPath string        `json:"config_path"`
	TotalSites int           `json:"total_sites"`
}

type getPageResult struct {
	URL           string `json:"url"`
	Title         string `json:"title"`
	Content       string `json:"content" jsonschema_description:"Page content converted to markdown"`
	ContentLength int    `json:"content_length"`
	FetchTimeMs   int64  `json:"fetch_time_ms"`
}

type cancelCrawlResult struct {
	JobID     string    `json:"job_id"`
	Cancelled bool      `json:"cancelled" jsonschema_description:"false when the job was already in a terminal state"`
	SiteKey   string    `json:"site_key"`
	Status    JobStatus `json:"status"`
	Message   string    `json:"message"`
}

type searchDocsResult struct {
	Query       string               `json:"query"`
	SiteKey     string               `json:"site_key,omitempty" jsonschema_description:"Present when the search was limited to one site"`
	Count       int                  `json:"count"`
	Results     []index.SearchResult `json:"results"`
	NextActions string               `json:"next_actions"`
}

type listPagesResult struct {
	SiteKey    string          `json:"site_key"`
	Pages      []pageListEntry `json:"pages"`
	Total      int             `json:"total"`
	Offset     int             `json:"offset"`
	MaxResults int             `json:"max_results"`
	Returned   int             `json:"returned"`
	Message    string          `json:"message,omitempty" jsonschema_description:"Present when the site has no crawl output yet"`
}

type readPageResult struct {
	SiteKey       string   `json:"site_key"`
	URL           string   `json:"url"`
	Title         string   `json:"title"`
	Content       string   `json:"content"`
	ContentLength int      `json:"content_length" jsonschema_description:"Bytes of content in this response"`
	TotalLength   int      `json:"total_length" jsonschema_description:"Bytes of the whole stored page"`
	Offset        int      `json:"offset"`
	Truncated     bool     `json:"truncated"`
	Depth         int      `json:"depth"`
	CrawledAt     string   `json:"crawled_at"`
	ContentHash   string   `json:"content_hash"`
	Headings      []string `json:"headings"`
	Source        string   `json:"source"`
	NextOffset    *int     `json:"next_offset,omitempty" jsonschema_description:"Pass as offset to read the next part; present only when truncated"`
}

type jobSummary struct {
	JobID           string    `json:"job_id"`
	SiteKey         string    `json:"site_key"`
	Status          JobStatus `json:"status" jsonschema_description:"pending, running, completed, failed or cancelled"`
	StartedAt       string    `json:"started_at"`
	PagesProcessed  int64     `json:"pages_processed"`
	PagesQueued     int64     `json:"pages_queued"`
	PagesUnchanged  int64     `json:"pages_unchanged" jsonschema_description:"Incremental re-checks whose content had not changed; not counted in pages_processed"`
	Incremental     bool      `json:"incremental"`
	CompletedAt     string    `json:"completed_at,omitempty"`
	DurationSeconds *float64  `json:"duration_seconds,omitempty" jsonschema_description:"Present once completed_at is"`
	ErrorMessage    string    `json:"error_message,omitempty"`
}

func summarizeJob(j *Job) jobSummary {
	sum := jobSummary{
		JobID:          j.ID,
		SiteKey:        j.SiteKey,
		Status:         j.Status,
		StartedAt:      j.StartedAt.Format(time.RFC3339),
		PagesProcessed: j.PagesProcessed,
		PagesQueued:    j.PagesQueued,
		PagesUnchanged: j.PagesUnchanged,
		Incremental:    j.Incremental,
		ErrorMessage:   j.ErrorMessage,
	}
	if !j.CompletedAt.IsZero() {
		sum.CompletedAt = j.CompletedAt.Format(time.RFC3339)
		d := j.CompletedAt.Sub(j.StartedAt).Seconds()
		sum.DurationSeconds = &d
	}
	return sum
}

type crawlSiteResult struct {
	Status      string `json:"status" jsonschema_description:"started, or already_running when a crawl for the site is in progress"`
	Message     string `json:"message"`
	JobID       string `json:"job_id"`
	SiteKey     string `json:"site_key"`
	Incremental bool   `json:"incremental"`
}

type serverIdentity struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	ConfigPath string `json:"config_path"`
}

type serverSite struct {
	Key         string `json:"key"`
	Domain      string `json:"domain"`
	LastCrawled string `json:"last_crawled,omitempty"`
	Status      string `json:"status,omitempty" jsonschema_description:"\"running\" while a crawl is active; absent otherwise"`
}

type describeServerResult struct {
	Server      serverIdentity `json:"server"`
	Sites       []serverSite   `json:"sites"`
	RecentJobs  []jobSummary   `json:"recent_jobs" jsonschema_description:"Newest first, at most 20"`
	TotalSites  int            `json:"total_sites"`
	TotalJobs   int            `json:"total_jobs"`
	JobsCapped  bool           `json:"jobs_capped"`
	NextActions string         `json:"next_actions"`
}

type crawlSummary struct {
	CrawlID        int64  `json:"crawl_id"`
	CrawlStartedAt string `json:"crawl_started_at"`
	CrawlEndedAt   string `json:"crawl_ended_at"`
	TotalPages     int    `json:"total_pages"`
	Mode           string `json:"mode" jsonschema_description:"full or incremental"`
}

func summarizeCrawl(c *index.LatestCrawl) *crawlSummary {
	if c == nil {
		return nil
	}
	return &crawlSummary{
		CrawlID:        c.ID,
		CrawlStartedAt: c.CrawlStartedAt.Format(time.RFC3339),
		CrawlEndedAt:   c.CrawlEndedAt.Format(time.RFC3339),
		TotalPages:     c.TotalPages,
		Mode:           string(c.Mode),
	}
}

type freshnessResult struct {
	SiteKey        string        `json:"site_key"`
	OutputDir      string        `json:"output_dir"`
	OutputExists   bool          `json:"output_exists"`
	StateDirExists bool          `json:"state_dir_exists"`
	LastCrawl      *crawlSummary `json:"last_crawl,omitempty" jsonschema_description:"Latest recorded crawl; absent if none"`
	AgeSeconds     *int64        `json:"age_seconds,omitempty" jsonschema_description:"Seconds since last_crawl ended; present with last_crawl"`
	RunningJob     *jobSummary   `json:"running_job,omitempty"`
	NextActions    string        `json:"next_actions"`
}

type diffEntry struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	ContentHash string `json:"content_hash"`
	Depth       int    `json:"depth"`
	PriorHash   string `json:"prior_hash,omitempty" jsonschema_description:"Baseline hash; changed entries only"`
}

type diffCrawlResult struct {
	SiteKey        string        `json:"site_key"`
	Since          string        `json:"since"`
	Added          []diffEntry   `json:"added"`
	Removed        []diffEntry   `json:"removed"`
	Changed        []diffEntry   `json:"changed"`
	UnchangedCount int           `json:"unchanged_count"`
	Total          int           `json:"total"`
	Returned       int           `json:"returned"`
	Offset         int           `json:"offset"`
	MaxResults     int           `json:"max_results"`
	CurrentCrawl   *crawlSummary `json:"current_crawl,omitempty"`
	BaselineCrawl  *crawlSummary `json:"baseline_crawl,omitempty"`
	Note           string        `json:"note,omitempty" jsonschema_description:"Why the diff is empty or partial, when it is"`
}
