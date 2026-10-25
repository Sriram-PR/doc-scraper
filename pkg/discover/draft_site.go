package discover

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

// DraftRequest describes one draft: a URL to discover, or a Base entry to
// start from without any discovery requests.
type DraftRequest struct {
	URL        string
	SiteKey    string
	Base       *config.SiteConfig
	Overrides  map[string]any
	PreviewURL string
	Selector   string
}

// DraftResult is a drafted entry and how one page extracts with it.
type DraftResult struct {
	Draft   *Draft
	Preview Preview
}

// DraftSite builds a draft and previews it. With URL it runs discovery; with
// only Base it starts from that entry. Overrides apply last, so a
// content_selector in them wins over Selector. The preview page is
// PreviewURL, else the sample page discovery used, else the seed (URL) or the
// entry's first start URL (Base).
func (d *Discoverer) DraftSite(ctx context.Context, req DraftRequest) (*DraftResult, error) {
	var draft *Draft
	var doc *goquery.Document
	var pageURL *url.URL
	sampled := false
	switch {
	case req.URL != "":
		report, err := d.Run(ctx, req.URL)
		if err != nil {
			return nil, err
		}
		draft = BuildDraft(report, req.Selector)
		doc, pageURL = report.Doc, report.FinalURL
		if report.Sample != nil {
			doc, pageURL, sampled = report.Sample.Doc, report.Sample.URL, true
		}
	case req.Base != nil:
		draft = &Draft{Site: *req.Base}
	default:
		return nil, errors.New("draft needs a URL or an existing site entry")
	}
	if req.SiteKey != "" {
		draft.SiteKey = req.SiteKey
	}
	if len(req.Overrides) > 0 {
		site, err := config.MergeSiteFields(draft.Site, req.Overrides)
		if err != nil {
			return nil, err
		}
		draft.Site = site
	}

	target := req.PreviewURL
	if target == "" && doc == nil {
		if len(draft.Site.StartURLs) == 0 {
			return nil, errors.New("the entry has no start_urls to preview")
		}
		target = draft.Site.StartURLs[0]
	}
	if target != "" {
		u, err := parseHTTPURL(target)
		if err != nil {
			return nil, err
		}
		if doc, pageURL, err = d.fetchPage(ctx, u); err != nil {
			return nil, err
		}
		sampled = false
	}
	preview := ExtractPreview(doc, pageURL, &draft.Site, d.Log)
	preview.Sampled = sampled
	return &DraftResult{Draft: draft, Preview: preview}, nil
}

func parseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		if len(raw) > 200 {
			raw = strings.ToValidUTF8(raw[:200], "") + "..."
		}
		return nil, fmt.Errorf("invalid URL %q: need an absolute http(s) URL", raw)
	}
	return u, nil
}
