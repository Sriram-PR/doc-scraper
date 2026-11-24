package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

// lastCrawlTailBytes bounds the tail read; a crawl_meta record is a few
// hundred bytes.
const lastCrawlTailBytes = 64 * 1024

// lastCrawlEnded returns the end time of the last crawl_meta record in
// jsonlPath that has a valid one, or the zero time. Finalize writes crawl_meta
// as the last line, so the tail usually answers; otherwise the whole file is
// scanned.
func lastCrawlEnded(jsonlPath string) time.Time {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	if t, ok := tailCrawlEnded(f); ok {
		return t
	}
	return scanCrawlEnded(f)
}

// tailCrawlEnded reads the end time from the file's last non-blank line when
// that line is a crawl_meta record with a valid one.
func tailCrawlEnded(f *os.File) (time.Time, bool) {
	info, err := f.Stat()
	if err != nil {
		return time.Time{}, false
	}
	start := max(info.Size()-lastCrawlTailBytes, 0)
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil {
		return time.Time{}, false
	}
	buf = bytes.TrimRightFunc(buf, unicode.IsSpace)
	i := bytes.LastIndexByte(buf, '\n')
	if i < 0 && start > 0 {
		return time.Time{}, false
	}
	var meta models.CrawlMetaJSONL
	if json.Unmarshal(bytes.TrimSpace(buf[i+1:]), &meta) != nil || meta.RecordType != models.RecordTypeCrawlMeta {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, meta.CrawlEndedAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func scanCrawlEnded(r io.Reader) time.Time {
	var lastEnded time.Time
	scanner := crawler.NewJSONLScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, models.RecordTypeCrawlMeta) {
			continue
		}
		var meta models.CrawlMetaJSONL
		if err := json.Unmarshal([]byte(line), &meta); err != nil {
			continue
		}
		if meta.RecordType != models.RecordTypeCrawlMeta {
			continue
		}
		if t, err := time.Parse(time.RFC3339, meta.CrawlEndedAt); err == nil {
			lastEnded = t
		}
	}
	return lastEnded
}
