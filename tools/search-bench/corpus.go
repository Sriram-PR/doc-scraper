package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

// searchLimit is the search_docs maximum, deep enough to rank pages past the
// default chunk window.
const searchLimit = 50

type fingerprint struct {
	Pages  int    `json:"pages"`
	Chunks int    `json:"chunks"`
	SHA256 string `json:"sha256"`
}

type corpus struct {
	siteKey string
	tmp     string
	idx     *index.Index
	pages   map[string]struct{}
	fp      fingerprint
}

// loadCorpus indexes a frozen <dir>/<siteKey>/pages.jsonl into a fresh index
// in a temp dir, so bm25 statistics come from this corpus alone.
func loadCorpus(ctx context.Context, dir, siteKey string) (*corpus, error) {
	path := filepath.Join(dir, siteKey, "pages.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("corpus %s: %w (crawl it with: doc-scraper crawl -config tools/search-bench/crawl.yaml -site %s)", siteKey, err, siteKey)
	}
	pages, chunks, err := scanPages(data)
	if err != nil {
		return nil, fmt.Errorf("corpus %s: %w", siteKey, err)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("corpus %s: %s has no page records", siteKey, path)
	}
	sum := sha256.Sum256(data)

	tmp, err := os.MkdirTemp("", "search-bench-*")
	if err != nil {
		return nil, err
	}
	idx, err := index.Open(filepath.Join(tmp, "index.db"), 0, slog.New(slog.DiscardHandler))
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	c := &corpus{
		siteKey: siteKey,
		tmp:     tmp,
		idx:     idx,
		pages:   pages,
		fp:      fingerprint{Pages: len(pages), Chunks: chunks, SHA256: hex.EncodeToString(sum[:])},
	}
	if err := crawler.IndexChunksFromJSONL(ctx, idx, siteKey, path, slog.New(slog.DiscardHandler)); err != nil {
		_ = c.close()
		return nil, fmt.Errorf("corpus %s: index: %w", siteKey, err)
	}
	return c, nil
}

// pageRecordMarker is the byte pre-filter crawler.IndexChunksFromJSONL applies
// before decoding; a record without it is never indexed.
var pageRecordMarker = []byte(`"record_type":"page"`)

// scanPages mirrors the page filter in crawler.IndexChunksFromJSONL; a URL
// listed twice counts once, with its last record's chunks, as in the index.
func scanPages(data []byte) (map[string]struct{}, int, error) {
	chunksByURL := make(map[string]int)
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		if !bytes.Contains(sc.Bytes(), pageRecordMarker) {
			continue
		}
		var p models.PageJSONL
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			continue
		}
		if p.RecordType != models.RecordTypePage || p.URL == "" {
			continue
		}
		chunksByURL[p.URL] = len(chunk.Split(p.Content))
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	pages := make(map[string]struct{}, len(chunksByURL))
	total := 0
	for u, n := range chunksByURL {
		pages[u] = struct{}{}
		total += n
	}
	return pages, total, nil
}

func (c *corpus) search(ctx context.Context, q string) ([]hit, error) {
	res, err := c.idx.SearchChunks(ctx, q, c.siteKey, searchLimit)
	if err != nil {
		return nil, err
	}
	hits := make([]hit, len(res))
	for i, r := range res {
		hits[i] = hit{url: r.URL, anchor: r.Anchor}
	}
	return hits, nil
}

func (c *corpus) close() error {
	return errors.Join(c.idx.Close(), os.RemoveAll(c.tmp))
}
