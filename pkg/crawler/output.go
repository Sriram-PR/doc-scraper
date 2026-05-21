// FILE: pkg/crawler/output.go
package crawler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/process"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

// OutputManager owns all output file handles and metadata collection for a crawl.
type OutputManager struct {
	log           *logrus.Entry
	resolved      *config.ResolvedSiteConfig
	siteCfg       *config.SiteConfig // retained for YAML metadata marshaling
	siteKey       string
	siteOutputDir string

	// TSV mapping
	mappingFile     *os.File
	mappingFileMu   sync.Mutex
	mappingFilePath string

	// JSONL output. When bufferOutput is true (fresh crawls), records are
	// collected into collectedPageJSONL and written sorted by URL at Close()
	// for deterministic, diffable output. When false (resume crawls), records
	// stream directly to disk under jsonlFileMu so we don't fight the existing
	// content of the file.
	jsonlFile          *os.File
	jsonlFileMu        sync.Mutex
	jsonlFilePath      string
	collectedPageJSONL []models.PageJSONL

	// Chunks output. Same buffer-vs-stream behavior as JSONL above; on
	// buffered close, sorted by (URL, ChunkIndex).
	chunksFile       *os.File
	chunksFileMu     sync.Mutex
	chunksFilePath   string
	collectedChunks  []models.ChunkJSONL

	// bufferOutput selects between buffer-and-sort (fresh crawls, deterministic
	// output) and stream-write (resume crawls). Set by OpenFiles.
	bufferOutput bool

	// YAML metadata
	collectedPageMetadata []models.PageMetadata
	metadataMutex         sync.Mutex
	crawlStartTime        time.Time
}

// NewOutputManager creates an OutputManager without opening files.
// Call OpenFiles after the output directory is ready (e.g. after cleanSiteOutputDir).
func NewOutputManager(log *logrus.Entry, resolved *config.ResolvedSiteConfig, siteCfg *config.SiteConfig, siteKey, siteOutputDir string) *OutputManager {
	return &OutputManager{
		log:                   log,
		resolved:              resolved,
		siteCfg:               siteCfg,
		siteKey:               siteKey,
		siteOutputDir:         siteOutputDir,
		collectedPageMetadata: make([]models.PageMetadata, 0),
	}
}

// OpenFiles opens all configured output files (TSV, JSONL, chunks).
// Must be called after the output directory exists and has been cleaned if needed.
func (om *OutputManager) OpenFiles(resume bool) {
	// Fresh crawls buffer JSONL and chunk records in memory and write them
	// sorted at Close(). Resume crawls stream directly to disk so we don't
	// overwrite existing content of the resumed-into file.
	om.bufferOutput = !resume

	// --- Initialize Simple TSV Mapping File (if enabled) ---
	if om.resolved.EnableOutputMapping {
		om.mappingFilePath = filepath.Join(om.siteOutputDir, om.resolved.OutputMappingFilename)
		om.log.Infof("Simple TSV URL-to-FilePath mapping enabled. Output file: %s", om.mappingFilePath)
		om.mappingFile = openOutputFile(om.log, om.mappingFilePath, "TSV mapping", resume)
	} else {
		om.log.Info("Simple TSV URL-to-FilePath mapping is disabled.")
	}

	// --- Initialize JSONL Output File (if enabled) ---
	if om.resolved.EnableJSONLOutput {
		om.jsonlFilePath = filepath.Join(om.siteOutputDir, om.resolved.JSONLOutputFilename)
		om.log.Infof("JSONL output enabled. Output file: %s", om.jsonlFilePath)
		om.jsonlFile = openOutputFile(om.log, om.jsonlFilePath, "JSONL", resume)
	} else {
		om.log.Info("JSONL output is disabled.")
	}

	// --- Initialize Chunks Output File (if chunking enabled) ---
	if om.resolved.ChunkingEnabled {
		om.chunksFilePath = filepath.Join(om.siteOutputDir, om.resolved.ChunkingOutputFilename)
		om.log.Infof("Chunking enabled. Output file: %s", om.chunksFilePath)
		om.chunksFile = openOutputFile(om.log, om.chunksFilePath, "chunks", resume)
	} else {
		om.log.Info("Chunking output is disabled.")
	}
}

// openOutputFile opens an output file for writing, with append or truncate based on resume mode.
// Returns nil on error (caller should treat nil as "output disabled").
func openOutputFile(log *logrus.Entry, path, label string, resume bool) *os.File {
	openFlags := os.O_CREATE | os.O_WRONLY
	if resume {
		log.Infof("Resume mode: Appending to %s file: %s", label, path)
		openFlags |= os.O_APPEND
	} else {
		log.Infof("Non-resume mode: Truncating %s file: %s", label, path)
		openFlags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, openFlags, 0644)
	if err != nil {
		log.Errorf("Failed to open/create %s file '%s': %v. %s output will be disabled.", label, path, err, label)
		return nil
	}
	return file
}

// Close syncs and closes all output files and writes the YAML metadata file.
// For fresh crawls (bufferOutput=true), JSONL and chunk records buffered in
// memory are sorted (by URL, and by URL+ChunkIndex respectively) and written
// out before the file handles are closed. Resume crawls have already streamed
// their records during the crawl and just need a flush.
func (om *OutputManager) Close() error {
	om.flushBufferedJSONL()
	om.flushBufferedChunks()
	om.closeMappingFile()
	om.closeJSONLFile()
	om.closeChunksFile()
	return om.writeMetadataYAML()
}

// PagesSaved returns the number of pages whose metadata has been collected.
func (om *OutputManager) PagesSaved() int {
	om.metadataMutex.Lock()
	defer om.metadataMutex.Unlock()
	return len(om.collectedPageMetadata)
}

// RecordPageOutput handles all post-save output: TSV write, YAML metadata collection,
// JSONL write, and chunks write. Called after content is successfully saved to disk.
// markdownBytes is the already-written markdown content, passed through to avoid re-reading the file.
//
// One timestamp is captured per page (crawledAt) and shared by every record
// emitted for that page so YAML/JSONL/chunks rows for the same page have
// identical timestamps and content hashes can be diffed cleanly across runs.
func (om *OutputManager) RecordPageOutput(finalURL, normalizedURL, savedContentPath string, markdownBytes []byte, pageTitle string, currentDepth, imageCount int, taskLog *logrus.Entry) {
	crawledAt := time.Now()
	crawledAtStr := crawledAt.Format(time.RFC3339)

	// Write to TSV mapping file
	om.writeToMappingFile(finalURL, savedContentPath, taskLog)

	// Check if we need markdown content for metadata or JSONL output
	enableYAML := om.resolved.EnableMetadataYAML
	enableJSONL := om.resolved.EnableJSONLOutput

	var contentHash string
	if (enableYAML || enableJSONL) && len(markdownBytes) > 0 {
		contentHash = utils.CalculateStringSHA256(string(markdownBytes))
	}

	// Collect YAML Page Metadata
	if enableYAML {
		relativeLocalPath, relErr := filepath.Rel(om.siteOutputDir, savedContentPath)
		if relErr != nil {
			taskLog.Warnf("Could not make path relative for metadata.yaml (Base: '%s', Target: '%s'): %v",
				om.siteOutputDir, savedContentPath, relErr)
			relativeLocalPath = savedContentPath
		}

		pageMeta := models.PageMetadata{
			OriginalURL:   finalURL,
			NormalizedURL: normalizedURL,
			LocalFilePath: filepath.ToSlash(relativeLocalPath),
			Title:         pageTitle,
			Depth:         currentDepth,
			ProcessedAt:   crawledAt,
			ContentHash:   contentHash,
			ImageCount:    imageCount,
		}

		om.metadataMutex.Lock()
		om.collectedPageMetadata = append(om.collectedPageMetadata, pageMeta)
		om.metadataMutex.Unlock()
	}

	// JSONL output (buffer for fresh crawls, stream for resume)
	if enableJSONL && om.jsonlFile != nil && markdownBytes != nil {
		headings := process.ExtractHeadings(markdownBytes)
		links, images := extractLinksAndImages(string(markdownBytes))

		pageJSONL := models.PageJSONL{
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

	// Chunks output (buffer for fresh crawls, stream for resume)
	if om.resolved.ChunkingEnabled && om.chunksFile != nil && markdownBytes != nil {
		chunkCfg := process.ChunkerConfig{
			MaxChunkSize: om.resolved.ChunkingMaxSize,
			ChunkOverlap: om.resolved.ChunkingOverlap,
		}

		chunks, chunkErr := process.ChunkMarkdown(string(markdownBytes), chunkCfg)
		if chunkErr != nil {
			taskLog.Warnf("Failed to chunk markdown content: %v", chunkErr)
		} else if len(chunks) > 0 {
			chunkJSONLs := make([]models.ChunkJSONL, len(chunks))
			for i, chunk := range chunks {
				chunkJSONLs[i] = models.ChunkJSONL{
					URL:              finalURL,
					ChunkIndex:       i,
					Content:          chunk.Content,
					HeadingHierarchy: chunk.HeadingHierarchy,
					PageTitle:        pageTitle,
					CrawledAt:        crawledAtStr,
				}
			}
			om.recordChunks(chunkJSONLs, taskLog)
			taskLog.Debugf("Wrote %d chunks for page", len(chunks))
		}
	}
}

// closeMappingFile closes the simple TSV mapping file, if it was opened.
func (om *OutputManager) closeMappingFile() {
	om.mappingFileMu.Lock()
	defer om.mappingFileMu.Unlock()

	if om.mappingFile != nil {
		om.log.Infof("Syncing and closing TSV mapping file: %s", om.mappingFilePath)
		if err := om.mappingFile.Sync(); err != nil {
			om.log.Errorf("Error syncing TSV mapping file '%s': %v", om.mappingFilePath, err)
		}
		if err := om.mappingFile.Close(); err != nil {
			om.log.Errorf("Error closing TSV mapping file '%s': %v", om.mappingFilePath, err)
		}
		om.mappingFile = nil
	}
}

// closeJSONLFile closes the JSONL output file handle if it was opened.
func (om *OutputManager) closeJSONLFile() {
	om.jsonlFileMu.Lock()
	defer om.jsonlFileMu.Unlock()

	if om.jsonlFile != nil {
		om.log.Infof("Syncing and closing JSONL output file: %s", om.jsonlFilePath)
		if err := om.jsonlFile.Sync(); err != nil {
			om.log.Errorf("Error syncing JSONL file '%s': %v", om.jsonlFilePath, err)
		}
		if err := om.jsonlFile.Close(); err != nil {
			om.log.Errorf("Error closing JSONL file '%s': %v", om.jsonlFilePath, err)
		}
		om.jsonlFile = nil
	}
}

// closeChunksFile closes the chunks output file handle if it was opened.
func (om *OutputManager) closeChunksFile() {
	om.chunksFileMu.Lock()
	defer om.chunksFileMu.Unlock()

	if om.chunksFile != nil {
		om.log.Infof("Syncing and closing chunks output file: %s", om.chunksFilePath)
		if err := om.chunksFile.Sync(); err != nil {
			om.log.Errorf("Error syncing chunks file '%s': %v", om.chunksFilePath, err)
		}
		if err := om.chunksFile.Close(); err != nil {
			om.log.Errorf("Error closing chunks file '%s': %v", om.chunksFilePath, err)
		}
		om.chunksFile = nil
	}
}

// writeToMappingFile writes a line to the simple TSV mapping file (if enabled and open).
func (om *OutputManager) writeToMappingFile(pageURL, absoluteFilePath string, taskLog *logrus.Entry) {
	om.mappingFileMu.Lock()
	defer om.mappingFileMu.Unlock()

	if om.mappingFile == nil {
		return
	}

	line := fmt.Sprintf("%s\t%s\n", pageURL, absoluteFilePath)
	if _, err := om.mappingFile.WriteString(line); err != nil {
		taskLog.WithFields(logrus.Fields{
			"tsv_mapping_file": om.mappingFilePath,
			"line_content":     strings.TrimSpace(line),
		}).Errorf("Failed to write to TSV mapping file: %v", err)
	}
}

// recordJSONL either buffers the page record for deterministic flush at Close()
// (fresh crawl) or streams it straight to disk (resume crawl).
func (om *OutputManager) recordJSONL(page models.PageJSONL, taskLog *logrus.Entry) {
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
		taskLog.WithField("jsonl_file", om.jsonlFilePath).Errorf("Failed to write to JSONL file: %v", err)
	}
}

// recordChunks either buffers chunk records for deterministic flush at Close()
// (fresh crawl) or streams them straight to disk (resume crawl).
func (om *OutputManager) recordChunks(chunks []models.ChunkJSONL, taskLog *logrus.Entry) {
	om.chunksFileMu.Lock()
	defer om.chunksFileMu.Unlock()

	if om.chunksFile == nil {
		return
	}
	if om.bufferOutput {
		om.collectedChunks = append(om.collectedChunks, chunks...)
		return
	}
	for _, chunk := range chunks {
		if err := writeJSONLLine(om.chunksFile, chunk); err != nil {
			taskLog.WithField("chunks_file", om.chunksFilePath).Errorf("Failed to write to chunks file: %v", err)
		}
	}
}

// flushBufferedJSONL writes all buffered page records to the JSONL file in URL
// order. No-op for resume crawls (records were streamed during the crawl) and
// when JSONL output is disabled.
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
			om.log.Errorf("Failed to flush JSONL record for %s: %v", page.URL, err)
		}
	}
	om.log.Infof("Flushed %d sorted JSONL records to %s", len(om.collectedPageJSONL), om.jsonlFilePath)
	om.collectedPageJSONL = nil
}

// flushBufferedChunks writes all buffered chunk records to the chunks file in
// (URL, ChunkIndex) order. No-op for resume crawls and when chunking is
// disabled.
func (om *OutputManager) flushBufferedChunks() {
	om.chunksFileMu.Lock()
	defer om.chunksFileMu.Unlock()

	if !om.bufferOutput || om.chunksFile == nil || len(om.collectedChunks) == 0 {
		return
	}

	sort.Slice(om.collectedChunks, func(i, j int) bool {
		if om.collectedChunks[i].URL != om.collectedChunks[j].URL {
			return om.collectedChunks[i].URL < om.collectedChunks[j].URL
		}
		return om.collectedChunks[i].ChunkIndex < om.collectedChunks[j].ChunkIndex
	})

	for _, chunk := range om.collectedChunks {
		if err := writeJSONLLine(om.chunksFile, chunk); err != nil {
			om.log.Errorf("Failed to flush chunk record for %s#%d: %v", chunk.URL, chunk.ChunkIndex, err)
		}
	}
	om.log.Infof("Flushed %d sorted chunk records to %s", len(om.collectedChunks), om.chunksFilePath)
	om.collectedChunks = nil
}

// writeJSONLLine marshals a value to JSON and writes it as one line.
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

// writeMetadataYAML writes all collected page metadata to a YAML file.
func (om *OutputManager) writeMetadataYAML() error {
	if !om.resolved.EnableMetadataYAML {
		om.log.Info("YAML metadata output is disabled.")
		return nil
	}

	yamlFilePath := filepath.Join(om.siteOutputDir, om.resolved.MetadataYAMLFilename)

	om.log.Infof("Preparing to write crawl metadata to: %s", yamlFilePath)

	var siteConfigMap map[string]interface{}
	siteConfigBytes, errCfgMarshal := yaml.Marshal(om.siteCfg)
	if errCfgMarshal != nil {
		om.log.Warnf("Could not marshal site_configuration for YAML metadata: %v", errCfgMarshal)
	} else {
		if errCfgUnmarshal := yaml.Unmarshal(siteConfigBytes, &siteConfigMap); errCfgUnmarshal != nil {
			om.log.Warnf("Could not unmarshal site_configuration into map for YAML metadata: %v", errCfgUnmarshal)
			siteConfigMap = nil
		}
	}

	om.metadataMutex.Lock()
	pagesToMarshal := make([]models.PageMetadata, len(om.collectedPageMetadata))
	copy(pagesToMarshal, om.collectedPageMetadata)
	om.metadataMutex.Unlock()

	metadata := models.CrawlMetadata{
		SiteKey:           om.siteKey,
		AllowedDomain:     om.siteCfg.AllowedDomain,
		CrawlStartTime:    om.crawlStartTime,
		CrawlEndTime:      time.Now(),
		TotalPagesSaved:   len(pagesToMarshal),
		SiteConfiguration: siteConfigMap,
		Pages:             pagesToMarshal,
	}

	yamlData, errMarshal := yaml.Marshal(&metadata)
	if errMarshal != nil {
		om.log.Errorf("Failed to marshal crawl metadata to YAML: %v", errMarshal)
		return fmt.Errorf("failed to marshal crawl metadata to YAML for site '%s': %w", om.siteKey, errMarshal)
	}

	errWrite := os.WriteFile(yamlFilePath, yamlData, 0644)
	if errWrite != nil {
		om.log.Errorf("Failed to write metadata YAML file '%s': %v", yamlFilePath, errWrite)
		return fmt.Errorf("failed to write metadata YAML file '%s' for site '%s': %w", yamlFilePath, om.siteKey, errWrite)
	}

	om.log.Infof("Successfully wrote crawl metadata (%d pages) to %s", metadata.TotalPagesSaved, yamlFilePath)
	return nil
}
