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

// samplePage picks a docs page other than the seed to validate the content
// selector on: the shallowest in-scope sitemap page, or, with no sitemap, the
// first in-scope link in the seed page's body. It returns nil when none
// qualifies.
func samplePage(r *Report) *url.URL {
	if len(r.Sitemap.URLs) > 0 {
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

// confirmOnSample re-runs detection on one other docs page when the seed
// recognized a framework but its selector failed validation there, as landing
// layouts do. The sample's result is adopted only for the same framework with
// a validated selector.
func (d *Discoverer) confirmOnSample(ctx context.Context, r *Report) {
	det := r.Detection
	if !det.Fallback || det.Confidence == detect.ConfidenceJSRendered ||
		det.Framework == detect.FrameworkUnknown || det.Framework == detect.FrameworkJSShell {
		return
	}
	u := samplePage(r)
	if u == nil {
		r.SampleNote = "no other page in scope to check the selector on"
		return
	}
	doc, finalURL, err := d.fetchPage(ctx, u)
	if err != nil {
		r.SampleNote = fmt.Sprintf("checked %s: %v", u.Path, err)
		return
	}
	if !sameSite(finalURL.Hostname(), r.FinalURL.Hostname()) {
		r.SampleNote = fmt.Sprintf("checked %s: redirected to %s", u.Path, finalURL.Hostname())
		return
	}
	res := detect.DetectPage(doc)
	switch {
	case res.Framework != det.Framework:
		r.SampleNote = fmt.Sprintf("checked %s: detected %s there", u.Path, res.Framework)
		return
	case res.Fallback:
		r.SampleNote = fmt.Sprintf("checked %s: the %s selector matched nothing there either", u.Path, det.Framework)
		return
	}
	r.Detection = res
	r.Sample = &SamplePage{URL: finalURL, Doc: doc}
	r.Warnings = slices.DeleteFunc(r.Warnings, func(w string) bool { return w == littleContentWarning })
}
