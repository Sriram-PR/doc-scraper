package discover

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/detect"
)

// LocaleInfo describes locale scoping discovered from the seed URL and page.
type LocaleInfo struct {
	PathSegment string
	Hreflangs   []string
}

// VersionInfo describes a versioned docs tree the seed URL sits inside.
type VersionInfo struct {
	Segment string
	IsAlias bool
}

var iso639 = map[string]struct{}{}

func init() {
	for _, c := range strings.Fields(
		"ar bn cs da de el en es fa fi fr he hi hu id it ja ko nl no pl pt ro ru sk sv th tr uk vi zh") {
		iso639[c] = struct{}{}
	}
}

var (
	localeSegmentRe = regexp.MustCompile(`^[a-z]{2}(?:[-_][a-zA-Z]{2,4})?$`)
	versionNumRe    = regexp.MustCompile(`^v?\d+(?:\.\d+)*(?:\.x)?$`)
)

var versionAliases = map[string]struct{}{
	"latest": {}, "stable": {}, "dev": {}, "next": {}, "current": {}, "master": {}, "main": {},
}

func isLocaleSegment(seg string) bool {
	if !localeSegmentRe.MatchString(seg) {
		return false
	}
	base, _, _ := strings.Cut(seg, "-")
	base, _, _ = strings.Cut(base, "_")
	_, ok := iso639[base]
	return ok
}

func isVersionSegment(seg string) (alias, ok bool) {
	lower := strings.ToLower(seg)
	if _, isAlias := versionAliases[lower]; isAlias {
		return true, true
	}
	return false, versionNumRe.MatchString(lower)
}

func analyzeSeedPage(r *Report) {
	r.PageTitle = strings.TrimSpace(r.Doc.Find("title").First().Text())
	r.Detection = detect.DetectPage(r.Doc)

	if canonical, ok := r.Doc.Find(`link[rel="canonical"]`).First().Attr("href"); ok {
		if cu, err := url.Parse(canonical); err == nil && cu.Host != "" && cu.Hostname() != r.FinalURL.Hostname() {
			r.Warnings = append(r.Warnings, "canonical URL points to "+cu.Hostname()+"; consider adding that host instead")
		}
	}

	r.Doc.Find(`link[rel="alternate"][hreflang]`).Each(func(_ int, s *goquery.Selection) {
		if hl, ok := s.Attr("hreflang"); ok {
			r.Locale.Hreflangs = append(r.Locale.Hreflangs, hl)
		}
	})

	for _, seg := range pathSegments(r.FinalURL.Path) {
		if r.Locale.PathSegment == "" && isLocaleSegment(seg) {
			r.Locale.PathSegment = seg
		}
		if r.Version.Segment == "" {
			if alias, ok := isVersionSegment(seg); ok {
				r.Version = VersionInfo{Segment: seg, IsAlias: alias}
			}
		}
	}

	bodyText := r.Doc.Find("body").Clone()
	bodyText.Find("script, style, noscript").Remove()
	tinyBody := len(strings.Join(strings.Fields(bodyText.Text()), " ")) < 200
	if r.Detection.Confidence == detect.ConfidenceJSRendered ||
		(r.Detection.Fallback && tinyBody) {
		r.Warnings = append(r.Warnings,
			"the page appears to be JavaScript-rendered ("+string(r.Detection.Framework)+"); a static crawl will likely capture little or no content")
	}
}

func pathSegments(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}
