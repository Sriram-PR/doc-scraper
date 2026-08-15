package crawler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

const (
	llmsTxtFilename     = "llms.txt"
	llmsFullTxtFilename = "llms-full.txt"
)

// writeLLMsTxtFiles emits llms.txt and llms-full.txt per https://llmstxt.org/,
// streaming from the already-flushed JSONL so memory stays bounded for large
// crawls. No-op if JSONL is disabled or unreadable.
func (om *OutputManager) writeLLMsTxtFiles() {
	if om.jsonlFilePath == "" || om.siteOutputDir == "" {
		return
	}
	in, err := os.Open(om.jsonlFilePath)
	if err != nil {
		om.log.Warn(fmt.Sprintf("llms.txt: cannot read JSONL %s: %v", om.jsonlFilePath, err))
		return
	}
	defer in.Close()

	fullPath := filepath.Join(om.siteOutputDir, llmsFullTxtFilename)
	fullFile, err := os.Create(fullPath)
	if err != nil {
		om.log.Warn(fmt.Sprintf("llms.txt: cannot create %s: %v", fullPath, err))
		return
	}
	defer fullFile.Close()

	domain := ""
	if om.siteCfg != nil {
		domain = om.siteCfg.AllowedDomain
	}
	fmt.Fprintf(fullFile, "# %s\n\n> Full crawled content from %s.\n\n", om.siteKey, domain)

	type pageRef struct{ title, url string }
	var pages []pageRef

	scanner := newJSONLScanner(in)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Cheap pre-filter to avoid Unmarshal cost on non-page records.
		if !strings.Contains(line, `"record_type":"page"`) {
			continue
		}
		var page models.PageJSONL
		if err := json.Unmarshal([]byte(line), &page); err != nil {
			continue
		}
		if page.RecordType != models.RecordTypePage {
			continue
		}

		title := strings.TrimSpace(page.Title)
		if title == "" {
			title = page.URL
		}
		pages = append(pages, pageRef{title: title, url: page.URL})

		fmt.Fprintf(fullFile, "---\n\n# %s\n\nURL: %s\n\n%s\n\n", escapeHeading(title), page.URL, page.Content)
	}
	if err := scanner.Err(); err != nil {
		om.log.Warn(fmt.Sprintf("llms.txt: scan error on %s: %v", om.jsonlFilePath, err))
		return
	}

	indexPath := filepath.Join(om.siteOutputDir, llmsTxtFilename)
	indexFile, err := os.Create(indexPath)
	if err != nil {
		om.log.Warn(fmt.Sprintf("llms.txt: cannot create %s: %v", indexPath, err))
		return
	}
	defer indexFile.Close()

	fmt.Fprintf(indexFile, "# %s\n\n", om.siteKey)
	fmt.Fprintf(indexFile, "> Crawled documentation from %s. %d pages.\n\n", domain, len(pages))
	fmt.Fprintln(indexFile, "## Pages")
	if len(pages) == 0 {
		fmt.Fprintln(indexFile, "_No pages were successfully crawled._")
	}
	for _, p := range pages {
		fmt.Fprintf(indexFile, "- [%s](%s)\n", escapeLinkText(p.title), p.url)
	}

	om.log.Info(fmt.Sprintf("Wrote llms.txt (%d pages) and llms-full.txt to %s", len(pages), om.siteOutputDir))
}

// escapeLinkText escapes brackets, backslashes, and newlines in markdown link
// text. URLs are assumed well-formed and are not escaped.
func escapeLinkText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `[`, `\[`)
	s = strings.ReplaceAll(s, `]`, `\]`)
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// escapeHeading collapses embedded newlines so a multi-line title stays on one H1.
func escapeHeading(s string) string {
	return strings.ReplaceAll(s, "\n", " ")
}
