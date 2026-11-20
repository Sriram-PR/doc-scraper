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
// full-text chunks: pages whose content hash and title match the indexed
// chunks are skipped, changed and new pages are re-split, every page is
// re-split when the site was indexed with an older chunk.FormatVersion, and
// chunks for pages no longer present are pruned. Shared by crawl finalization
// and the MCP server's upgrade backfill; one page's content is in memory at a
// time.
func IndexChunksFromJSONL(ctx context.Context, idx *index.Index, siteKey, jsonlPath string, log *slog.Logger) error {
	if idx == nil || jsonlPath == "" {
		return nil
	}

	existing, err := idx.ChunkedPages(ctx, siteKey)
	if err != nil {
		return err
	}
	format, err := idx.ChunkFormat(ctx, siteKey)
	if err != nil {
		return err
	}
	skip := existing
	if format != chunk.FormatVersion {
		skip = nil
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
		if alreadyIndexed(skip, &p) {
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
	if err := idx.SetChunkFormat(ctx, siteKey, chunk.FormatVersion); err != nil {
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

// alreadyIndexed reports whether p's chunks were written from the same content
// and title.
func alreadyIndexed(indexed map[string]index.ChunkedPage, p *models.PageJSONL) bool {
	prev, ok := indexed[p.URL]
	return ok && p.ContentHash != "" && prev.ContentHash == p.ContentHash && prev.Title == p.Title
}

// indexProgressInterval spaces the progress lines of a long indexing run.
var indexProgressInterval = 2 * time.Second
