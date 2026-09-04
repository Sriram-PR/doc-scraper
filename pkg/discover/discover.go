// Package discover probes a documentation site with a handful of polite HTTP
// requests and derives a draft crawl configuration: framework and content
// selector, crawl scope, corpus size, and version/locale duplication warnings.
package discover

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/detect"
)

const maxFetchBytes = 8 << 20

// Report holds everything learned about a site from the discovery fetches.
type Report struct {
	SeedURL   string
	FinalURL  *url.URL
	CrossHost bool
	PageTitle string
	Doc       *goquery.Document
	Detection detect.DetectionResult
	Locale    LocaleInfo
	Version   VersionInfo
	Robots    RobotsInfo
	Sitemap   SitemapInfo
	LlmsTxt   LlmsTxtInfo
	Scope     ScopeInfo
	Warnings  []string
}

// Discoverer performs the discovery fetches. Client should come from
// fetch.NewClient so SSRF guarding and timeouts match crawl behavior.
type Discoverer struct {
	Client    *http.Client
	UserAgent string
	Log       *slog.Logger
}

// Run probes rawURL and assembles a Report. Request budget: robots.txt, the
// seed page, llms.txt, the sitemap, and at most two sitemap-index children.
func (d *Discoverer) Run(ctx context.Context, rawURL string) (*Report, error) {
	seed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || seed.Host == "" || (seed.Scheme != "http" && seed.Scheme != "https") {
		return nil, fmt.Errorf("invalid URL %q: need an absolute http(s) URL", rawURL)
	}
	r := &Report{SeedURL: seed.String()}

	r.Robots = d.fetchRobots(ctx, seed)
	if r.Robots.SeedDisallowed {
		return nil, fmt.Errorf("robots.txt at %s://%s disallows crawling %s; refusing to add this site", seed.Scheme, seed.Host, seed.Path)
	}

	doc, finalURL, err := d.fetchPage(ctx, seed)
	if err != nil {
		return nil, err
	}
	r.Doc = doc
	r.FinalURL = finalURL
	r.CrossHost = finalURL.Hostname() != seed.Hostname()
	if r.CrossHost {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s redirected to %s; the draft is anchored on the final host", seed.Hostname(), finalURL.Hostname()))
		r.Robots = d.fetchRobots(ctx, finalURL)
		if r.Robots.SeedDisallowed {
			return nil, fmt.Errorf("robots.txt on %s disallows crawling %s; refusing to add this site", finalURL.Hostname(), finalURL.Path)
		}
	}

	analyzeSeedPage(r)
	r.LlmsTxt = d.fetchLlmsTxt(ctx, r.FinalURL)
	r.Sitemap = d.fetchSitemap(ctx, r.FinalURL, r.Robots.Sitemaps)
	analyzeScope(r)
	return r, nil
}

func (d *Discoverer) get(ctx context.Context, u string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	if d.UserAgent != "" {
		req.Header.Set("User-Agent", d.UserAgent)
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func (d *Discoverer) fetchPage(ctx context.Context, seed *url.URL) (*goquery.Document, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, seed.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	if d.UserAgent != "" {
		req.Header.Set("User-Agent", d.UserAgent)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching %s: %w", seed, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, nil, fmt.Errorf("%s returned HTTP %d: the site is blocking automated requests (bot protection), not a configuration problem", seed, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, nil, fmt.Errorf("%s returned HTTP %d", seed, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", seed, err)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", seed, err)
	}
	finalURL := seed
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL
	}
	return doc, finalURL, nil
}
