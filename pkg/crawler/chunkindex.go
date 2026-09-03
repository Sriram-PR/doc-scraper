package crawler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"

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

	existing, err := idx.ChunkedHashes(ctx, siteKey)
	if err != nil {
		return err
	}

	f, err := os.Open(jsonlPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	current := make(map[string]struct{}, len(existing))
	var indexed, skipped int
	scanner := newJSONLScanner(f)
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
		if existing[p.URL] == p.ContentHash && p.ContentHash != "" {
			skipped++
			continue
		}
		if err := idx.ReplaceChunks(ctx, siteKey, p.URL, p.Title, p.ContentHash, chunk.Split(p.Content)); err != nil {
			return err
		}
		indexed++
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	if err := idx.PruneChunks(ctx, siteKey, current); err != nil {
		return err
	}
	log.Debug("chunk index refreshed", "site", siteKey, "pages_indexed", indexed, "pages_unchanged", skipped)
	return nil
}
