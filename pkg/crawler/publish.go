package crawler

import (
	"context"
	"log/slog"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

// CommitRecord identifies a finished crawl for the history index.
type CommitRecord struct {
	CrawlStartedAt time.Time
	CrawlEndedAt   time.Time
	Mode           index.Mode
}

// publishCrawl brings the index in line with the site's live JSONL: it records
// rec in crawl history (when rec is non-nil) and rebuilds the search chunks.
// With JSONL output disabled it clears the site instead. Errors are logged,
// not returned, so index trouble can never fail a crawl.
func publishCrawl(ctx context.Context, idx *index.Index, siteKey, jsonlPath string, enableJSONL bool, rec *CommitRecord, log *slog.Logger) {
	if idx == nil {
		return
	}
	if !enableJSONL {
		if rec != nil {
			if err := idx.DeleteSiteHistory(ctx, siteKey); err != nil {
				log.Warn("index: failed to remove crawl history of a site with JSONL output disabled", "err", err)
			}
		}
		if err := idx.DeleteSiteChunks(ctx, siteKey); err != nil {
			log.Warn("index: failed to remove chunks of a site with JSONL output disabled", "err", err)
		}
		return
	}
	if jsonlPath == "" {
		return
	}
	if rec != nil {
		recordCrawl(ctx, idx, siteKey, jsonlPath, *rec, log)
	}
	if err := IndexChunksFromJSONL(ctx, idx, siteKey, jsonlPath, log); err != nil {
		log.Warn("index: failed to refresh chunk index", "err", err)
	}
}

func recordCrawl(ctx context.Context, idx *index.Index, siteKey, jsonlPath string, rec CommitRecord, log *slog.Logger) {
	pages, err := readJSONLPagesForIndex(jsonlPath)
	if err != nil {
		log.Warn("index: failed to read JSONL for history capture", "path", jsonlPath, "err", err)
		return
	}
	if err := idx.RecordCrawl(ctx, index.CrawlRecord{
		SiteKey:        siteKey,
		CrawlStartedAt: rec.CrawlStartedAt,
		CrawlEndedAt:   rec.CrawlEndedAt,
		Mode:           rec.Mode,
		Pages:          pages,
	}); err != nil {
		log.Warn("index: failed to record crawl", "err", err)
	}
}
