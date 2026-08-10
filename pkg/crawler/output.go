package crawler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"

	"context"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/process"
	"github.com/Sriram-PR/doc-scraper/pkg/storage/index"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

const (
	jsonlScannerInitBuf = 64 * 1024
	jsonlScannerMaxLine = 10 * 1024 * 1024 // page records are single lines and can be large
)

// newJSONLScanner returns a scanner sized for the long single-line JSON records
// the crawler writes, so a big page does not trip bufio's default line cap.
func newJSONLScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, jsonlScannerInitBuf), jsonlScannerMaxLine)
	return scanner
}

// OutputManager owns the JSONL output file and the crawl_meta summary.
type OutputManager struct {
	log           *slog.Logger
	resolved      *config.ResolvedSiteConfig
	siteCfg       *config.SiteConfig
	siteKey       string
	siteOutputDir string

	jsonlFile          *os.File
	jsonlFileMu        sync.Mutex
	jsonlFilePath      string
	collectedPageJSONL []models.PageJSONL

	// bufferOutput=true (fresh crawls) buffers records and writes them sorted
	// by URL at Close() for deterministic, diffable output. false (resume)
	// streams records straight to disk so we don't overwrite existing content.
	bufferOutput bool

	// Set by the crawler before Run; both feed the crawl_meta record at Close.
	crawlStartTime time.Time
	pagesRecorded  atomic.Int64

	// Optional crawl-history index. SetIndex wires both fields; if idx is nil
	// the index write at Close is skipped.
	idx       *index.Index
	indexMode index.Mode
}

// SetIndex attaches a crawl-history index handle and the mode label that will
// be persisted for this crawl. nil idx disables the index write at Close.
func (om *OutputManager) SetIndex(idx *index.Index, mode index.Mode) {
	om.idx = idx
	om.indexMode = mode
}

// NewOutputManager creates an OutputManager. Call OpenFiles once the output
// directory exists.
func NewOutputManager(log *slog.Logger, resolved *config.ResolvedSiteConfig, siteCfg *config.SiteConfig, siteKey, siteOutputDir string) *OutputManager {
	return &OutputManager{
		log:           log,
		resolved:      resolved,
		siteCfg:       siteCfg,
		siteKey:       siteKey,
		siteOutputDir: siteOutputDir,
	}
}

// OpenFiles opens the JSONL output file when enabled. Must run after the
// output directory exists and has been cleaned if needed.
func (om *OutputManager) OpenFiles(resume bool) {
	om.bufferOutput = !resume

	if om.resolved.EnableJSONLOutput {
		om.jsonlFilePath = filepath.Join(om.siteOutputDir, om.resolved.JSONLOutputFilename)
		om.log.Info("JSONL output enabled", "path", om.jsonlFilePath)
		if resume {
			priorPages, err := stripLeftoverCrawlMeta(om.jsonlFilePath)
			if err != nil {
				om.log.Warn("Resume: failed to rewrite JSONL without leftover crawl_meta records, file may contain duplicates", "path", om.jsonlFilePath, "err", err)
			} else if priorPages > 0 {
				om.pagesRecorded.Store(priorPages)
				om.log.Info("Resume: counted prior page records and stripped any leftover crawl_meta", "prior_pages", priorPages)
			}
		}
		om.jsonlFile = openOutputFile(om.log, om.jsonlFilePath, "JSONL", resume)
	} else {
		om.log.Info("JSONL output is disabled.")
	}
}

// openOutputFile opens path for writing; appends in resume mode, truncates
// otherwise. Returns nil on error (treat as "output disabled").
func openOutputFile(log *slog.Logger, path, label string, resume bool) *os.File {
	openFlags := os.O_CREATE | os.O_WRONLY
	if resume {
		log.Info("Resume mode: appending to file", "label", label, "path", path)
		openFlags |= os.O_APPEND
	} else {
		log.Info("Non-resume mode: truncating file", "label", label, "path", path)
		openFlags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, openFlags, 0644)
	if err != nil {
		log.Error("Failed to open/create output file; output for this label will be disabled", "label", label, "path", path, "err", err)
		return nil
	}
	return file
}

// stripLeftoverCrawlMeta rewrites path in place, dropping every crawl_meta
// record so the resumed crawl can append fresh pages and a single
// authoritative crawl_meta at Close. Returns the page-record count seen so
// the caller can seed the cumulative page counter. A non-existent file is
// not an error (treated as no prior records).
func stripLeftoverCrawlMeta(path string) (int64, error) {
	in, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer in.Close()

	var kept bytes.Buffer
	var pages int64
	scanner := newJSONLScanner(in)
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.Contains(line, []byte(`"record_type":"crawl_meta"`)) {
			continue
		}
		if bytes.Contains(line, []byte(`"record_type":"page"`)) {
			pages++
		}
		kept.Write(line)
		kept.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan: %w", err)
	}
	if err := os.WriteFile(path, kept.Bytes(), 0644); err != nil {
		return 0, fmt.Errorf("rewrite: %w", err)
	}
	return pages, nil
}

// Close flushes buffered records, appends crawl_meta, closes the file, writes
// llms.txt/llms-full.txt from the resulting JSONL, and finally records this
// crawl into the history index (if attached).
func (om *OutputManager) Close() error {
	if om.bufferOutput {
		om.flushBufferedJSONL()
		om.writeCrawlMetaRecord()
		om.closeJSONLFile()
	} else {
		// Streamed (resume/incremental) mode appends records live during the
		// crawl, so a reprocessed page leaves both its stale and fresh record in
		// the file. Close the append handle, then rewrite the JSONL with one
		// record per URL so it stays the authoritative, de-duplicated source of
		// truth for llms.txt and the history index.
		om.closeJSONLFile()
		om.finalizeStreamedJSONL()
	}
	om.writeLLMsTxtFiles()
	om.writeToIndex()
	return nil
}

// writeToIndex rescans the just-closed JSONL for page records and records the
// crawl in the history index. Re-scan keeps the path uniform across fresh
// (buffered) and resume (streamed-append) modes; the on-disk JSONL is the
// authoritative source of truth after Close. Errors are logged, not returned,
// so a busted index can never break a successful crawl.
func (om *OutputManager) writeToIndex() {
	if om.idx == nil || om.jsonlFilePath == "" {
		return
	}
	pages, err := readJSONLPagesForIndex(om.jsonlFilePath)
	if err != nil {
		om.log.Warn("index: failed to read JSONL for history capture", "path", om.jsonlFilePath, "err", err)
		return
	}
	record := index.CrawlRecord{
		SiteKey:        om.siteKey,
		CrawlStartedAt: om.crawlStartTime,
		CrawlEndedAt:   time.Now(),
		Mode:           om.indexMode,
		Pages:          pages,
	}
	if err := om.idx.RecordCrawl(context.Background(), record); err != nil {
		om.log.Warn("index: failed to record crawl", "err", err)
	}
}

// readJSONLPagesForIndex extracts a minimal per-page row for the index. Skips
// crawl_meta and any malformed line. Title/depth/content_hash come straight
// from the JSONL record; content body is not loaded into memory.
func readJSONLPagesForIndex(path string) ([]index.PageRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	out := make([]index.PageRecord, 0, 256)
	scanner := newJSONLScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(`"record_type":"page"`)) {
			continue
		}
		var p models.PageJSONL
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		if p.RecordType != models.RecordTypePage {
			continue
		}
		out = append(out, index.PageRecord{
			URL:         p.URL,
			Title:       p.Title,
			ContentHash: p.ContentHash,
			Depth:       p.Depth,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// PagesSaved returns the number of pages recorded during the crawl.
func (om *OutputManager) PagesSaved() int {
	return int(om.pagesRecorded.Load())
}

// RecordPageOutput emits a JSONL record for a saved page. markdownBytes is
// passed through to avoid re-reading the file from disk.
func (om *OutputManager) RecordPageOutput(finalURL string, markdownBytes []byte, pageTitle string, currentDepth int, taskLog *slog.Logger) {
	om.pagesRecorded.Add(1)
	crawledAtStr := time.Now().Format(time.RFC3339)

	if om.resolved.EnableJSONLOutput && om.jsonlFile != nil && markdownBytes != nil {
		var contentHash string
		if len(markdownBytes) > 0 {
			contentHash = utils.CalculateStringSHA256(string(markdownBytes))
		}
		headings := process.ExtractHeadings(markdownBytes)
		links, images := extractLinksAndImages(string(markdownBytes))

		pageJSONL := models.PageJSONL{
			RecordType:  models.RecordTypePage,
			URL:         finalURL,
			Title:       pageTitle,
			Content:     string(markdownBytes),
			Headings:    headings,
			Links:       links,
			Images:      images,
			ContentHash: contentHash,
			CrawledAt:   crawledAtStr,
			Depth:       currentDepth,
		}
		om.recordJSONL(pageJSONL, taskLog)
	}
}

func (om *OutputManager) closeJSONLFile() {
	om.jsonlFileMu.Lock()
	defer om.jsonlFileMu.Unlock()

	if om.jsonlFile != nil {
		om.log.Info("Syncing and closing JSONL output file", "path", om.jsonlFilePath)
		if err := om.jsonlFile.Sync(); err != nil {
			om.log.Error("Error syncing JSONL file", "path", om.jsonlFilePath, "err", err)
		}
		if err := om.jsonlFile.Close(); err != nil {
			om.log.Error("Error closing JSONL file", "path", om.jsonlFilePath, "err", err)
		}
		om.jsonlFile = nil
	}
}

func (om *OutputManager) recordJSONL(page models.PageJSONL, taskLog *slog.Logger) {
	om.jsonlFileMu.Lock()
	defer om.jsonlFileMu.Unlock()

	if om.jsonlFile == nil {
		return
	}
	if om.bufferOutput {
		om.collectedPageJSONL = append(om.collectedPageJSONL, page)
		return
	}
	if err := writeJSONLLine(om.jsonlFile, page); err != nil {
		taskLog.Error("Failed to write to JSONL file", "jsonl_file", om.jsonlFilePath, "err", err)
	}
}

func (om *OutputManager) flushBufferedJSONL() {
	om.jsonlFileMu.Lock()
	defer om.jsonlFileMu.Unlock()

	if !om.bufferOutput || om.jsonlFile == nil || len(om.collectedPageJSONL) == 0 {
		return
	}

	sort.Slice(om.collectedPageJSONL, func(i, j int) bool {
		return om.collectedPageJSONL[i].URL < om.collectedPageJSONL[j].URL
	})

	for _, page := range om.collectedPageJSONL {
		if err := writeJSONLLine(om.jsonlFile, page); err != nil {
			om.log.Error("Failed to flush JSONL record", "url", page.URL, "err", err)
		}
	}
	om.log.Info("Flushed sorted JSONL records", "count", len(om.collectedPageJSONL), "path", om.jsonlFilePath)
	om.collectedPageJSONL = nil
}

// writeCrawlMetaRecord appends the crawl summary as the final JSONL line.
// On resume, OpenFiles strips any leftover crawl_meta records so the line
// written here is the only one in the file.
func (om *OutputManager) writeCrawlMetaRecord() {
	om.jsonlFileMu.Lock()
	defer om.jsonlFileMu.Unlock()

	if om.jsonlFile == nil {
		return
	}
	meta := om.buildCrawlMeta()
	if err := writeJSONLLine(om.jsonlFile, meta); err != nil {
		om.log.Error("Failed to write crawl_meta record", "path", om.jsonlFilePath, "err", err)
		return
	}
	om.log.Info("Wrote crawl_meta record", "total_pages", meta.TotalPages, "path", om.jsonlFilePath)
}

func (om *OutputManager) buildCrawlMeta() models.CrawlMetaJSONL {
	return models.CrawlMetaJSONL{
		RecordType:     models.RecordTypeCrawlMeta,
		SiteKey:        om.siteKey,
		AllowedDomain:  om.siteCfg.AllowedDomain,
		CrawlStartedAt: om.crawlStartTime.Format(time.RFC3339),
		CrawlEndedAt:   time.Now().Format(time.RFC3339),
		TotalPages:     int(om.pagesRecorded.Load()),
	}
}

// finalizeStreamedJSONL rewrites the streamed (resume/incremental) JSONL so it
// holds one page record per URL (the freshest, since a reprocessed page's new
// record is appended after its stale one) plus a single crawl_meta, sorted by
// URL to match fresh-crawl output. This collapses the stale+fresh duplicate,
// keeps llms.txt/llms-full.txt free of stale content, corrects the page count,
// and lets the history-index insert (unique on crawl_id+url) succeed. Must run
// after the append handle is closed. Errors are logged, never fatal.
func (om *OutputManager) finalizeStreamedJSONL() {
	if om.jsonlFilePath == "" {
		return
	}
	pages, err := dedupePageRecords(om.jsonlFilePath)
	if err != nil {
		om.log.Warn("Resume: failed to dedupe JSONL page records; appending crawl_meta over the existing file", "path", om.jsonlFilePath, "err", err)
		om.appendCrawlMeta()
		return
	}
	om.pagesRecorded.Store(int64(len(pages)))

	var buf bytes.Buffer
	for _, page := range pages {
		line, mErr := json.Marshal(page)
		if mErr != nil {
			om.log.Error("Failed to marshal JSONL page during dedupe", "url", page.URL, "err", mErr)
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if meta, mErr := json.Marshal(om.buildCrawlMeta()); mErr != nil {
		om.log.Error("Failed to marshal crawl_meta during dedupe", "err", mErr)
	} else {
		buf.Write(meta)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(om.jsonlFilePath, buf.Bytes(), 0644); err != nil {
		om.log.Error("Failed to write finalized JSONL", "path", om.jsonlFilePath, "err", err)
		return
	}
	om.log.Info("Wrote crawl_meta record", "total_pages", len(pages), "path", om.jsonlFilePath)
}

// dedupePageRecords reads a JSONL file and returns its page records with one
// entry per URL (the last occurrence wins), sorted by URL. crawl_meta and
// unparseable lines are skipped. A non-existent file yields no records.
func dedupePageRecords(path string) ([]models.PageJSONL, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	latest := make(map[string]models.PageJSONL)
	scanner := newJSONLScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(`"record_type":"page"`)) {
			continue
		}
		var p models.PageJSONL
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		if p.RecordType != models.RecordTypePage {
			continue
		}
		latest[p.URL] = p
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	pages := make([]models.PageJSONL, 0, len(latest))
	for _, p := range latest {
		pages = append(pages, p)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].URL < pages[j].URL })
	return pages, nil
}

// appendCrawlMeta appends a single crawl_meta record, used as a fallback when
// the dedupe rewrite cannot read the file. Best-effort.
func (om *OutputManager) appendCrawlMeta() {
	f, err := os.OpenFile(om.jsonlFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		om.log.Error("Failed to open JSONL to append crawl_meta", "path", om.jsonlFilePath, "err", err)
		return
	}
	defer f.Close()
	meta := om.buildCrawlMeta()
	if err := writeJSONLLine(f, meta); err != nil {
		om.log.Error("Failed to append crawl_meta record", "path", om.jsonlFilePath, "err", err)
		return
	}
	om.log.Info("Wrote crawl_meta record", "total_pages", meta.TotalPages, "path", om.jsonlFilePath)
}

func writeJSONLLine(f *os.File, v interface{}) error {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if _, err := f.Write(append(jsonBytes, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
