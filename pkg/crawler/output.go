package crawler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/process"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

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
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
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

// Close flushes buffered records, appends crawl_meta, closes the file, then
// writes llms.txt/llms-full.txt from the resulting JSONL.
func (om *OutputManager) Close() error {
	om.flushBufferedJSONL()
	om.writeCrawlMetaRecord()
	om.closeJSONLFile()
	om.writeLLMsTxtFiles()
	return nil
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
	meta := models.CrawlMetaJSONL{
		RecordType:     models.RecordTypeCrawlMeta,
		SiteKey:        om.siteKey,
		AllowedDomain:  om.siteCfg.AllowedDomain,
		CrawlStartedAt: om.crawlStartTime.Format(time.RFC3339),
		CrawlEndedAt:   time.Now().Format(time.RFC3339),
		TotalPages:     int(om.pagesRecorded.Load()),
	}
	if err := writeJSONLLine(om.jsonlFile, meta); err != nil {
		om.log.Error("Failed to write crawl_meta record", "path", om.jsonlFilePath, "err", err)
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
