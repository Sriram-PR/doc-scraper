package crawler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sriram-PR/doc-scraper/pkg/models"
)

const (
	llmsTxtFilename     = "llms.txt"
	llmsFullTxtFilename = "llms-full.txt"
)

// writeLLMsTxtFiles emits llms.txt (a navigable manifest of pages) and
// llms-full.txt (the concatenated full content of every page) into the site
// output directory, following the convention at https://llmstxt.org/.
//
// Source of truth is the already-flushed JSONL file: we stream it line-by-line
// to keep memory bounded for large crawls. crawl_meta records are skipped. The
// pass collects (title, url) pairs in memory for llms.txt while streaming each
// page's full content into llms-full.txt. No-op when JSONL output is disabled
// or the JSONL file is unreadable.
func (om *OutputManager) writeLLMsTxtFiles() {
	if om.jsonlFilePath == "" || om.siteOutputDir == "" {
		return
	}
	in, err := os.Open(om.jsonlFilePath)
	if err != nil {
		om.log.Warnf("llms.txt: cannot read JSONL %s: %v", om.jsonlFilePath, err)
		return
	}
	defer in.Close()

	fullPath := filepath.Join(om.siteOutputDir, llmsFullTxtFilename)
	fullFile, err := os.Create(fullPath)
	if err != nil {
		om.log.Warnf("llms.txt: cannot create %s: %v", fullPath, err)
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

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Cheap discriminator to avoid Unmarshal cost on non-page records
		// (we expect at most one crawl_meta record per file, but other consumers
		// may eventually add records).
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
		om.log.Warnf("llms.txt: scan error on %s: %v", om.jsonlFilePath, err)
		return
	}

	indexPath := filepath.Join(om.siteOutputDir, llmsTxtFilename)
	indexFile, err := os.Create(indexPath)
	if err != nil {
		om.log.Warnf("llms.txt: cannot create %s: %v", indexPath, err)
		return
	}
	defer indexFile.Close()

	fmt.Fprintf(indexFile, "# %s\n\n", om.siteKey)
	fmt.Fprintf(indexFile, "> Crawled documentation from %s. %d pages.\n\n", domain, len(pages))
	fmt.Fprintln(indexFile, "## Pages")
	for _, p := range pages {
		fmt.Fprintf(indexFile, "- [%s](%s)\n", escapeLinkText(p.title), p.url)
	}

	om.log.Infof("Wrote llms.txt (%d pages) and llms-full.txt to %s", len(pages), om.siteOutputDir)
}

// escapeLinkText escapes characters that would break the text portion of a
// markdown link: square brackets and backslashes. URLs are not escaped; well-
// formed URLs from the crawler do not contain bare ')' that would close the
// link.
func escapeLinkText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `[`, `\[`)
	s = strings.ReplaceAll(s, `]`, `\]`)
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// escapeHeading collapses embedded newlines so a multi-line page title cannot
// break the H1 line that anchors a section of llms-full.txt.
func escapeHeading(s string) string {
	return strings.ReplaceAll(s, "\n", " ")
}
