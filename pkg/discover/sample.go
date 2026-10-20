package discover

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/detect"
)

var nonHTMLExt = map[string]struct{}{".pdf": {}, ".xml": {}, ".json": {}, ".txt": {}, ".zip": {}}

// sampleCandidates lists the docs pages other than the seed to validate the
// content selector on, in order: the seed page's first in-scope link, which on
// a landing page usually points at the docs, then the shallowest in-scope
// sitemap page.
func sampleCandidates(r *Report) []*url.URL {
	var out []*url.URL
	for _, u := range []*url.URL{firstSeedLink(r), shallowestSitemapPage(r)} {
		if u == nil {
			continue
		}
		if len(out) > 0 && strings.TrimSuffix(out[0].Path, "/") == strings.TrimSuffix(u.Path, "/") {
			continue
		}
		out = append(out, u)
	}
	return out
}

func firstSeedLink(r *Report) *url.URL {
	if r.Doc == nil {
		return nil
	}
	var found *url.URL
	r.Doc.Find("body a[href]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href, _ := s.Attr("href")
		u, err := r.FinalURL.Parse(href)
		if err != nil {
			return true
		}
		u.Fragment = ""
		if sampleable(r, u) {
			found = u
			return false
		}
		return true
	})
	return found
}

func shallowestSitemapPage(r *Report) *url.URL {
	var best *url.URL
	bestDepth := 0
	for _, raw := range r.Sitemap.URLs {
		u, err := url.Parse(raw)
		if err != nil || !sampleable(r, u) {
			continue
		}
		depth := len(pathSegments(strings.TrimPrefix(u.Path, r.Scope.Prefix)))
		if best == nil || depth < bestDepth || (depth == bestDepth && u.Path < best.Path) {
			best, bestDepth = u, depth
		}
	}
	return best
}

func sampleable(r *Report, u *url.URL) bool {
	if (u.Scheme != "http" && u.Scheme != "https") || !sameSite(u.Hostname(), r.FinalURL.Hostname()) {
		return false
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, r.Scope.Prefix) {
		return false
	}
	if strings.TrimSuffix(p, "/") == strings.TrimSuffix(r.FinalURL.Path, "/") {
		return false
	}
	for _, tree := range append(append([]string{}, r.Scope.SiblingVersions...), r.Scope.SiblingLocales...) {
		if strings.HasPrefix(p, tree) {
			return false
		}
	}
	for _, rule := range r.Robots.Disallows {
		if robotsRuleMatches(rule, p) {
			return false
		}
	}
	_, skip := nonHTMLExt[strings.ToLower(path.Ext(p))]
	return !skip
}

// robotsRuleMatches applies robots.txt path matching: a rule matches paths it
// prefixes, * matches any run of characters, and a trailing $ anchors the end.
func robotsRuleMatches(rule, p string) bool {
	anchored := strings.HasSuffix(rule, "$")
	rule = strings.TrimSuffix(rule, "$")
	expr := "^" + strings.ReplaceAll(regexp.QuoteMeta(rule), `\*`, ".*")
	if anchored {
		expr += "$"
	}
	re, err := regexp.Compile(expr)
	return err == nil && re.MatchString(p)
}

// SamplePage is the docs page a content selector was validated on when the
// seed page failed validation.
type SamplePage struct {
	URL *url.URL
	Doc *goquery.Document
}

const littleContentWarning = "the page has very little content; a crawl of it may capture little or nothing"

// confirmOnSample re-runs detection on other docs pages when the seed
// recognized a framework but its selector failed validation there, as landing
// layouts do. The first candidate that validates the same framework's selector
// is adopted.
func (d *Discoverer) confirmOnSample(ctx context.Context, r *Report) {
	det := r.Detection
	if !det.Fallback || det.Confidence == detect.ConfidenceJSRendered ||
		det.Framework == detect.FrameworkUnknown || det.Framework == detect.FrameworkJSShell {
		return
	}
	candidates := sampleCandidates(r)
	if len(candidates) == 0 {
		r.SampleNote = "no other page in scope to check the selector on"
		return
	}
	var notes []string
	for _, u := range candidates {
		res, sample, note := d.checkSample(ctx, r, u)
		if sample == nil {
			notes = append(notes, fmt.Sprintf("checked %s: %s", u.Path, note))
			continue
		}
		r.Detection = res
		r.Sample = sample
		r.Warnings = slices.DeleteFunc(r.Warnings, func(w string) bool { return w == littleContentWarning })
		return
	}
	r.SampleNote = strings.Join(notes, "; ")
}

// checkSample fetches u and returns its detection and page when the seed's
// framework selector validates there, or a note saying why not.
func (d *Discoverer) checkSample(ctx context.Context, r *Report, u *url.URL) (detect.DetectionResult, *SamplePage, string) {
	doc, finalURL, err := d.fetchPage(ctx, u)
	switch {
	case err != nil:
		return detect.DetectionResult{}, nil, err.Error()
	case !sameSite(finalURL.Hostname(), r.FinalURL.Hostname()):
		return detect.DetectionResult{}, nil, "redirected to " + finalURL.Hostname()
	case !sampleable(r, finalURL):
		return detect.DetectionResult{}, nil, "redirected to " + finalURL.Path
	}
	res := detect.DetectPage(doc)
	switch {
	case res.Framework != r.Detection.Framework:
		return res, nil, fmt.Sprintf("detected %s there", res.Framework)
	case res.Fallback:
		return res, nil, fmt.Sprintf("the %s selector matched nothing there either", r.Detection.Framework)
	}
	return res, &SamplePage{URL: finalURL, Doc: doc}, ""
}
