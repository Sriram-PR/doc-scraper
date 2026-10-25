package discover

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/process"
)

// Preview reports how one page extracts with a draft entry, the fidelity
// numbers a user or agent needs to judge the draft.
type Preview struct {
	URL            string  `json:"url"`
	Sampled        bool    `json:"-"`
	Markdown       string  `json:"markdown"`
	Chars          int     `json:"chars"`
	PageTextRatio  float64 `json:"page_text_ratio"`
	CodeBlocksKept int     `json:"code_blocks_kept"`
	CodeBlocksSeen int     `json:"code_blocks_total"`
	Headings       int     `json:"headings"`
	NavLeak        bool    `json:"nav_leak"`
	Err            string  `json:"error,omitempty"`
}

// ExtractPreview extracts doc exactly as a crawl would (same SelectMainContent
// path, readability fallback included) and converts it to markdown.
func ExtractPreview(doc *goquery.Document, pageURL *url.URL, site *config.SiteConfig, log *slog.Logger) Preview {
	p := Preview{URL: pageURL.String()}
	cp := process.NewContentProcessor(nil, &config.AppConfig{}, log)
	selection, _, err := cp.SelectMainContent(doc, pageURL, site, log)
	if err != nil {
		p.Err = err.Error()
		return p
	}
	p.CodeBlocksSeen = doc.Find("pre").Length()
	p.CodeBlocksKept = selection.Find("pre").Length()
	if selection.Is("pre") {
		p.CodeBlocksKept++
	}
	p.Headings = selection.Find("h1, h2, h3, h4").Length()
	p.NavLeak = hasNavResidue(selection)

	pageText := textLen(doc.Find("body"))
	markdown, err := process.ToMarkdown(selection)
	if err != nil {
		p.Err = err.Error()
		return p
	}
	p.Markdown = markdown
	p.Chars = len(markdown)
	if pageText > 0 {
		p.PageTextRatio = float64(textLen(selection)) / float64(pageText)
	}
	return p
}

// hasNavResidue reports navigation left in the extracted content. nav and
// role=navigation always count; asides and sidebar-classed blocks count only
// when mostly link text, since docs themes render callouts as <aside>.
func hasNavResidue(sel *goquery.Selection) bool {
	if sel.Find("nav, [role='navigation']").Length() > 0 {
		return true
	}
	found := false
	sel.Find("aside, [class*='sidebar']").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		total := textLen(s)
		found = total > 0 && 2*textLen(s.Find("a")) >= total
		return !found
	})
	return found
}

func textLen(s interface{ Text() string }) int {
	return len(strings.Join(strings.Fields(s.Text()), " "))
}

// FirstLines keeps the first n lines of s and notes how many were cut.
func FirstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + "\n... (" + fmt.Sprint(len(lines)-n) + " more lines)\n"
}
