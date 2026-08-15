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

var pageRecordMarker = []byte(`"record_type":"page"`)

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

	jsonlFile     *os.File
	jsonlFileMu   sync.Mutex
	jsonlFilePath string

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

// OpenFiles opens the JSONL output file when enabled. Records stream straight to
// disk during the crawl; Close rewrites the file into its canonical deduped,
// sorted form. Must run after the output directory exists and has been cleaned
// if needed.
func (om *OutputManager) OpenFiles(resume bool) {
	if om.resolved.EnableJSONLOutput {
		om.jsonlFilePath = filepath.Join(om.siteOutputDir, om.resolved.JSONLOutputFilename)
		om.log.Info("JSONL output enabled", "path", om.jsonlFilePath)
		if resume {
			priorPages, err := countPriorPageRecords(om.jsonlFilePath)
			if err != nil {
				om.log.Warn("Resume: failed to count prior JSONL page records", "path", om.jsonlFilePath, "err", err)
			} else if priorPages > 0 {
				om.pagesRecorded.Store(priorPages)
				om.log.Info("Resume: counted prior page records", "prior_pages", priorPages)
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

// countPriorPageRecords counts existing page records so a resumed crawl can seed
// its cumulative counter. Streams the file so no page bodies are held in memory;
// a non-existent file yields zero. Leftover crawl_meta records need no stripping
// here -- finalizeJSONL drops them and writes a single authoritative one.
func countPriorPageRecords(path string) (int64, error) {
	in, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer in.Close()

	var pages int64
	scanner := newJSONLScanner(in)
	for scanner.Scan() {
		if bytes.Contains(scanner.Bytes(), pageRecordMarker) {
			pages++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan: %w", err)
	}
	return pages, nil
}

// Close closes the streamed JSONL handle, rewrites the file into its canonical
// deduped/sorted form with a single crawl_meta, writes llms.txt/llms-full.txt
// from that JSONL, and records the crawl in the history index (if attached).
func (om *OutputManager) Close() error {
	om.closeJSONLFile()
	om.finalizeJSONL()
	om.writeLLMsTxtFiles()
	om.writeToIndex()
	return nil
}

// writeToIndex rescans the just-finalized JSONL for page records and records the
// crawl in the history index. The on-disk JSONL is the authoritative source of
// truth after Close. Errors are logged, not returned, so a busted index can
// never break a successful crawl.
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
		if !bytes.Contains(line, pageRecordMarker) {
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

// recordJSONL streams one page record to the open JSONL file. Records go to disk
// as they are produced so the full corpus is never held in memory; Close
// rewrites the file into its deduped, sorted canonical form.
func (om *OutputManager) recordJSONL(page models.PageJSONL, taskLog *slog.Logger) {
	om.jsonlFileMu.Lock()
	defer om.jsonlFileMu.Unlock()

	if om.jsonlFile == nil {
		return
	}
	if err := writeJSONLLine(om.jsonlFile, page); err != nil {
		taskLog.Error("Failed to write to JSONL file", "jsonl_file", om.jsonlFilePath, "err", err)
	}
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

// pageSpan is the byte range of one page record within the streamed JSONL file.
type pageSpan struct {
	offset int64
	length int
}

// finalizeJSONL rewrites the streamed JSONL into its canonical form: one page
// record per URL (last occurrence wins, collapsing a resumed page's stale and
// fresh copies), sorted by URL, followed by a single crawl_meta. It indexes the
// file by byte offset and copies records through, so only URLs and offsets are
// held in memory, never the page bodies -- memory stays flat on large corpora.
// This keeps llms.txt/llms-full.txt free of stale content, corrects the page
// count, and lets the history-index insert (unique on crawl_id+url) succeed.
// Must run after the append handle is closed. Errors are logged, never fatal.
func (om *OutputManager) finalizeJSONL() {
	if om.jsonlFilePath == "" {
		return
	}
	spans, err := indexLastPageOffsets(om.jsonlFilePath)
	if err != nil {
		om.log.Warn("Failed to index JSONL for finalize; appending crawl_meta over existing file", "path", om.jsonlFilePath, "err", err)
		om.appendCrawlMeta()
		return
	}
	om.pagesRecorded.Store(int64(len(spans)))

	if err := om.rewriteFinalizedJSONL(spans); err != nil {
		om.log.Error("Failed to write finalized JSONL", "path", om.jsonlFilePath, "err", err)
		return
	}
	om.log.Info("Wrote crawl_meta record", "total_pages", len(spans), "path", om.jsonlFilePath)
}

// indexLastPageOffsets scans the JSONL and returns, per URL, the byte span of
// that URL's last page record. Only URLs and spans are retained, never bodies.
func indexLastPageOffsets(path string) (map[string]pageSpan, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]pageSpan{}, nil
		}
		return nil, err
	}
	defer f.Close()

	spans := make(map[string]pageSpan)
	reader := bufio.NewReaderSize(f, jsonlScannerInitBuf)
	var offset int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if bytes.Contains(line, pageRecordMarker) {
				var hdr struct {
					RecordType string `json:"record_type"`
					URL        string `json:"url"`
				}
				if json.Unmarshal(line, &hdr) == nil && hdr.RecordType == models.RecordTypePage {
					spans[hdr.URL] = pageSpan{offset: offset, length: len(line)}
				}
			}
			offset += int64(len(line))
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read: %w", readErr)
		}
	}
	return spans, nil
}

// rewriteFinalizedJSONL writes the deduped, URL-sorted page records plus a fresh
// crawl_meta to a temp file (copying each record by offset so page bodies never
// all sit in memory at once) and atomically renames it over the JSONL.
func (om *OutputManager) rewriteFinalizedJSONL(spans map[string]pageSpan) error {
	src, err := os.Open(om.jsonlFilePath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer src.Close()

	tmpPath := om.jsonlFilePath + ".tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open temp: %w", err)
	}
	w := bufio.NewWriter(tmp)

	urls := make([]string, 0, len(spans))
	for u := range spans {
		urls = append(urls, u)
	}
	sort.Strings(urls)

	buf := make([]byte, 0, jsonlScannerInitBuf)
	for _, u := range urls {
		s := spans[u]
		if cap(buf) < s.length {
			buf = make([]byte, s.length)
		}
		rec := buf[:s.length]
		if _, rErr := src.ReadAt(rec, s.offset); rErr != nil {
			om.log.Error("Failed to read JSONL record during finalize", "url", u, "err", rErr)
			continue
		}
		// Normalize to exactly one trailing newline; the final streamed record
		// may lack one if the crawl was interrupted mid-write.
		if _, wErr := w.Write(bytes.TrimRight(rec, "\n")); wErr != nil {
			return finalizeWriteErr(tmp, tmpPath, wErr)
		}
		if wErr := w.WriteByte('\n'); wErr != nil {
			return finalizeWriteErr(tmp, tmpPath, wErr)
		}
	}

	if meta, mErr := json.Marshal(om.buildCrawlMeta()); mErr != nil {
		om.log.Error("Failed to marshal crawl_meta during finalize", "err", mErr)
	} else {
		if _, wErr := w.Write(append(meta, '\n')); wErr != nil {
			return finalizeWriteErr(tmp, tmpPath, wErr)
		}
	}

	if err := w.Flush(); err != nil {
		return finalizeWriteErr(tmp, tmpPath, fmt.Errorf("flush temp: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		om.log.Warn("Failed to sync finalized JSONL temp", "err", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, om.jsonlFilePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

func finalizeWriteErr(tmp *os.File, tmpPath string, cause error) error {
	_ = tmp.Close()
	_ = os.Remove(tmpPath)
	return cause
}

// appendCrawlMeta appends a single crawl_meta record, used as a fallback when
// the finalize rewrite cannot index the file. Best-effort.
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
