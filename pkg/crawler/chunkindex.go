package crawler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

// IndexChunksFromJSONL streams a site's finalized JSONL and refreshes its
// full-text chunks: pages whose content hash already matches the indexed
// chunks are skipped, changed and new pages are re-split, and chunks for
// pages no longer present are pruned. Shared by crawl finalization and the
// MCP server's upgrade backfill; one page's content is in memory at a time.
func IndexChunksFromJSONL(ctx context.Context, idx *index.Index, siteKey, jsonlPath string, log *slog.Logger) error {
	if idx == nil || jsonlPath == "" {
		return nil
	}

	existing, err := idx.ChunkedPages(ctx, siteKey)
	if err != nil {
		return err
	}

	f, err := os.Open(jsonlPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if len(existing) == 0 {
				return nil
			}
			return idx.DeleteSiteChunks(ctx, siteKey)
		}
		return err
	}
	defer func() { _ = f.Close() }()

	current := make(map[string]struct{}, len(existing))
	var indexed, skipped int
	start := time.Now()
	lastProgress := start
	w := idx.NewChunkWriter(ctx, siteKey)
	defer w.Abort()
	scanner := NewJSONLScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, pageRecordMarker) {
			continue
		}
		var p models.PageJSONL
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		if p.RecordType != models.RecordTypePage || p.URL == "" {
			continue
		}
		current[p.URL] = struct{}{}
		if existing[p.URL].ContentHash == p.ContentHash && p.ContentHash != "" {
			skipped++
			continue
		}
		if indexed == 0 {
			log.Info("Indexing pages for search")
		}
		if err := w.Put(p.URL, p.Title, p.ContentHash, chunk.Split(p.Content)); err != nil {
			return err
		}
		indexed++
		if time.Since(lastProgress) >= indexProgressInterval {
			lastProgress = time.Now()
			log.Info("Indexing pages for search", "pages_indexed", indexed, "pages_unchanged", skipped)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	removed, err := idx.PruneChunks(ctx, siteKey, current)
	if err != nil {
		return err
	}
	level := slog.LevelDebug
	if indexed > 0 || removed > 0 {
		level = slog.LevelInfo
	}
	log.Log(ctx, level, "Indexed pages for search", "pages_indexed", indexed,
		"pages_unchanged", skipped, "pages_removed", removed, "duration", time.Since(start).Round(time.Millisecond).String())
	return nil
}

// indexProgressInterval spaces the progress lines of a long indexing run.
var indexProgressInterval = 2 * time.Second
